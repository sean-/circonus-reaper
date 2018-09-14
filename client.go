package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"

	circonusapi "github.com/circonus-labs/circonus-gometrics/api"
	"github.com/circonus-labs/circonus-gometrics/api/config"
	consulapi "github.com/hashicorp/consul/api"
	"github.com/hashicorp/errwrap"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/pkg/errors"
	"github.com/ryanuber/columnize"
)

var (
	// Stats counters
	disabledTargets               uint
	excludedTargets               uint
	disabledMetrics               uint
	enabledMetrics                uint
	numLiveAllocs                 uint
	numNomadClients               uint
	numActiveNomadAllocMetrics    uint
	numAvailableNomadAllocMetrics uint

	checkBundleCIDRE = regexp.MustCompile(config.CheckBundleCIDRegex)
)

type client struct {
	mode           string
	circonusClient *circonusapi.API

	metricQuery string

	consulClient   *consulapi.Client
	excludeRegexps []*regexp.Regexp
	excludeTargets map[string]bool
	nomadClient    *nomadapi.Client

	circonusTargetsCache []string
	consulHostCache      []string

	dryRun       bool
	prefixSearch bool
}

func (c *client) DeactivateNomadCompletedAllocs() error {
	nomadNameToID, err := c.buildNomadNameIDCache()
	if err != nil {
		return errwrap.Wrapf("unable to populate Nomad Node to ID cache: {{err}}", err)
	}

	consulHosts, err := c.GetConsulHosts()
	if err != nil {
		return errwrap.Wrapf("unable to query Consul hosts: {{err}}", err)
	}

	circonusTargets, err := c.GetCirconusTargets()
	if err != nil {
		return errwrap.Wrapf("unable to get Circonus targets: {{err}}", err)
	}

	consulOnly, circonusOnly, consulAndCirconusHosts := findSets(consulHosts, circonusTargets)
	_, _, _ = consulOnly, circonusOnly, consulAndCirconusHosts

	// Disable all metrics associated with an inactive Nomad allocation.  Search
	// domain is limited to hosts that are in both Circonus and Consul.
	for _, host := range consulAndCirconusHosts {
		var nodeID string
		if id, found := nomadNameToID[host]; found {
			nodeID = id
		} else {
			log.Printf("INFO: ignoring non-nomad client %q", host)
			continue
		}

		if c.ExcludeTarget(host) {
			log.Printf("INFO: skipping nomad client %q (excluded target)", host)
			continue
		}

		// 2) Pull the nomad allocs for a given target
		log.Printf("TRACE: searching nomad client %q", host)
		allocIDs, err := c.FindAllocIDsByNodeID(nodeID)
		if err != nil {
			log.Printf("ERROR: unable to find node ID from host %q: %v", host, err)
			continue
		}
		numLiveAllocs += uint(len(allocIDs))

		checkBundles, err := c.FindCheckBundlesByTarget(host)
		if err != nil {
			log.Printf("ERROR: unable to find checks for target %q: %v", host, err)
			continue
		}

		nomadAllocRE := regexp.MustCompile(fmt.Sprintf("(?i)^nomad`%s`client`allocs`.*`%s`", host, `([\da-f]{8}-[\da-f]{4}-[\da-f]{4}-[\da-f]{4}-[\da-f]{12})`))

		for _, checkBundle := range checkBundles {
			checkBundleMD := checkBundleCIDRE.FindStringSubmatch(checkBundle.CID)
			if checkBundleMD == nil || len(checkBundleMD) < 3 {
				log.Printf("ERROR: unable to extract CID from %q", checkBundle.CID)
				continue
			}
			checkBundleID := checkBundleMD[2]

			checkBundleMetricIDStr := fmt.Sprintf("%s/%s", config.CheckBundleMetricsPrefix, checkBundleID)
			cbm, err := c.circonusClient.FetchCheckBundleMetrics(circonusapi.CIDType(&checkBundleMetricIDStr))
			if err != nil {
				log.Printf("ERROR: unable to fetch check bundle metrics for target/cid %q/%q: %v", host, checkBundle.CID, err)
				continue
			}

			if cbm != nil {
				var dirtyCheckBundle bool

				for i := range cbm.Metrics {
					allocMD := nomadAllocRE.FindStringSubmatch(cbm.Metrics[i].Name)
					if allocMD == nil || len(allocMD) < 2 {
						//log.Printf("ERROR: unable to extract CID from %q", checkBundle.CID)
						continue
					}
					allocID := strings.ToLower(allocMD[1])

					// alloc ID is active on the nomad client
					if _, found := allocIDs[allocID]; found {
						numActiveNomadAllocMetrics++
						switch cbm.Metrics[i].Status {
						case "active":
							//log.Printf("TRACE: skipping active alloc %q", cbm.Metrics[i].Name)
							// noop
						case "available":
							log.Printf("INFO: toggling metric %q/%q to active", checkBundleMetricIDStr, cbm.Metrics[i].Name)
							dirtyCheckBundle = true
							cbm.Metrics[i].Status = "active"
							enabledMetrics++
						default:
							panic(fmt.Sprintf("not sure what to do: %q / %#v", cbm.Metrics[i].Status, cbm.Metrics[i]))
						}
						continue
					}

					// alloc ID is no longer active on the nomad client but its metrics are
					numAvailableNomadAllocMetrics++
					switch cbm.Metrics[i].Status {
					case "active":
						log.Printf("INFO: toggling metric %q/%q to available", checkBundleMetricIDStr, cbm.Metrics[i].Name)
						cbm.Metrics[i].Status = "available"
						dirtyCheckBundle = true
						disabledMetrics++
					case "available":
						//log.Printf("TRACE: skipping active alloc %q", cbm.Metrics[i].Name)
						// noop
					default:
						panic(fmt.Sprintf("not sure what to do: %q / %#v", cbm.Metrics[i].Status, cbm.Metrics[i]))
					}
				}

				// Update the checkbundle metrics
				if dirtyCheckBundle {
					if c.dryRun {
						log.Printf("INFO: dry-run: about to update %q's check_bundle_metric %q", host, cbm.CID)
						continue
					} else {
						log.Printf("INFO: about to update %q's check_bundle_metric %q", host, cbm.CID)
					}

					if _, err := c.circonusClient.UpdateCheckBundleMetrics(cbm); err != nil {
						log.Printf("ERROR: unable to update check bundle metrics for CID %q: %v", cbm.CID, err)

						// NOTE(sean@): treat errors as soft because we want to try updating
						// check_bundle_metrics for all targets vs getting hung up on a
						// single target that may be failing for some reason.
						continue
					}
				}
			}
		}
	}

	return nil
}

func (c *client) DeactivateMatchingQuery() error {
	log.Printf("DEBUG: query: %q", c.metricQuery)
	searchQuery := circonusapi.SearchQueryType(c.metricQuery)
	filter := circonusapi.SearchFilterType{
		"size": []string{"1000"},
	}

	metricsToDisable, err := c.circonusClient.SearchMetrics(&searchQuery, &filter)
	if err != nil {
		return errors.Wrapf(err, "unable to search for target metrics %q: %v", c.metricQuery, err)
	}

	// map[CheckBundleCID]map[metric.MetricName]struct{}
	checkBundles := make(map[string]map[string]struct{}, 0)
	if metricsToDisable != nil {
		for _, metric := range *metricsToDisable {
			if _, found := checkBundles[metric.CheckBundleCID]; !found {
				checkBundles[metric.CheckBundleCID] = make(map[string]struct{})
			}
			checkBundles[metric.CheckBundleCID][metric.MetricName] = struct{}{}
		}
	}

	for cbid, cb := range checkBundles {
		log.Printf("DEBUG: check bundle %q", cbid)
		var dirty bool
		checkBundle, err := c.circonusClient.FetchCheckBundle(circonusapi.CIDType(&cbid))
		if err != nil {
			return errors.Wrapf(err, "unable to fetch checkbundle %q", cbid)
		}

		// Build a list of metrics that we
		for i, metric := range checkBundle.Metrics {
			if _, found := cb[metric.Name]; found {
				dirty = true
				checkBundle.Metrics[i].Status = "available"
				disabledMetrics++
				log.Printf("INFO: toggling metric %q/%q to available", cbid, metric.Name)
			}
		}

		if dirty && !c.dryRun {
			if _, err := c.circonusClient.UpdateCheckBundle(checkBundle); err != nil {
				return errors.Wrapf(err, "unable to update checkbundle %q", cbid)
			}
		}
	}

	return nil
}

func (c *client) DeactivateUnknownHosts() error {
	consulHosts, err := c.GetConsulHosts()
	if err != nil {
		return errwrap.Wrapf("unable to query Consul hosts: {{err}}", err)
	}

	circonusTargets, err := c.GetCirconusTargets()
	if err != nil {
		return errwrap.Wrapf("unable to get Circonus targets: {{err}}", err)
	}

	consulOnly, circonusOnly, consulAndCirconusHosts := findSets(consulHosts, circonusTargets)
	_, _, _ = consulOnly, circonusOnly, consulAndCirconusHosts

	extraHosts := make([]string, 0, len(circonusOnly))
	for _, host := range circonusOnly {
		if c.ExcludeTarget(host) {
			log.Printf("INFO: skipping check bundle deactivation for excluded target %q", host)
			excludedTargets++
			continue
		}
		log.Printf("INFO: deactivating check bundles for target %q", host)
		disabledTargets++
		extraHosts = append(extraHosts, host)
	}

	// Disable all metrics for a given host that doesn't exist in consul
	if !c.dryRun {
		for _, host := range extraHosts {
			if err = c.DisableTargetChecks(host); err != nil {
				log.Printf("ERROR: unable to disable checks on targets %q: %v", host, err)

				// NOTE(sean@): treat errors as soft because we want to try deactivating
				// check_bundles for all targets vs getting hung up on a single target
				// that may be failing for some reason.
				continue
			}
		}
	}

	return nil
}

func (c *client) DeleteCheckBundle(checkBundle *circonusapi.CheckBundle) error {
	return fmt.Errorf("DeleteCheckBundle not implemented")
}

func (c *client) DisableTargetChecks(target string) error {
	checkBundles, err := c.FindCheckBundlesByTarget(target)
	if err != nil {
		return errwrap.Wrapf(fmt.Sprintf("unable to find checks for target %q: {{err}}", target), err)
	}

	for _, checkBundle := range checkBundles {
		if c.ExcludeTarget(checkBundle.Target) {
			log.Printf("INFO: skipping %q %q", checkBundle.Target, checkBundle.CID)
			return nil
		}

		log.Printf("INFO: about to delete %q %q", checkBundle.Target, checkBundle.CID)
		if err := c.DeleteCheckBundle(checkBundle); err != nil {
			return errwrap.Wrapf(fmt.Sprintf("unable to delete check bundle %q: {{err}}", checkBundle.CID), err)
		}
	}

	return nil
}

func (c *client) ExcludeTarget(host string) bool {
	_, found := c.excludeTargets[host]
	if found {
		return true
	}

	for _, re := range c.excludeRegexps {
		if b := re.MatchString(host); b {
			return true
		}
	}

	return false
}

func (c *client) FindAllocIDsByNodeID(nodeID string) (map[string]struct{}, error) {
	queryOpts := &nomadapi.QueryOptions{
		AllowStale: true,
	}
	allocList, _, err := c.nomadClient.Nodes().Allocations(nodeID, queryOpts)
	if err != nil {
		return nil, errwrap.Wrapf("unable to query nomad allocations: {{err}}", err)
	}

	allocIDs := make(map[string]struct{}, len(allocList))
	for _, alloc := range allocList {
		allocIDs[alloc.ID] = struct{}{}
	}

	return allocIDs, nil
}

func (c *client) FindCheckBundlesByTarget(host string) ([]*circonusapi.CheckBundle, error) {
	v := url.Values{}

	if c.prefixSearch {
		host = fmt.Sprintf("%s%s", host, "*")
	}
	v.Set("search", fmt.Sprintf("(active:1)(host:%q)", host))

	v.Set("size", "1000")

	u := url.URL{}
	u.Path = config.CheckBundlePrefix
	u.RawQuery = v.Encode()

	respJSON, err := c.circonusClient.Get(u.String())
	if err != nil {
		return nil, errwrap.Wrapf("unable to fetch search results: {{err}}", err)
	}

	checkBundles := []*circonusapi.CheckBundle{}

	err = json.Unmarshal(respJSON, &checkBundles)
	if err != nil {
		return nil, errwrap.Wrapf("unable to decode response: {{err}}", err)
	}

	return checkBundles, nil
}

func (c *client) GetCirconusTargets() ([]string, error) {
	if c.circonusTargetsCache != nil {
		return c.circonusTargetsCache, nil
	}

	searchQuery := circonusapi.SearchQueryType("(active:1)")
	filterCriteria := map[string][]string{
	/* "available": nil, */
	}
	checkBundles, err := c.circonusClient.SearchCheckBundles(&searchQuery, &filterCriteria)
	if err != nil {
		return nil, errwrap.Wrapf("unable to search Circonus: {{err}}", err)
	}

	var hosts []string
	if checkBundles != nil {
		hosts = make([]string, 0, len(*checkBundles))

		hostMap := make(map[string]struct{}, len(*checkBundles))
		for _, checkBundle := range *checkBundles {
			hostMap[checkBundle.Target] = struct{}{}
		}

		for host := range hostMap {
			hosts = append(hosts, host)
		}

		c.circonusTargetsCache = hosts
	}

	return c.circonusTargetsCache, nil
}

func (c *client) GetCirconusTargetMetrics(target string) ([]string, error) {
	searchQuery := circonusapi.SearchQueryType(fmt.Sprintf("(host:%q)(active:1)", target))
	filter := circonusapi.SearchFilterType(nil)

	metrics, err := c.circonusClient.SearchMetrics(&searchQuery, &filter)
	if err != nil {
		return nil, errwrap.Wrapf("unable to search for target metrics: {{err}}", err)
	}

	var metricCIDs []string
	if metrics != nil {
		metricCIDs = make([]string, 0, len(*metrics))
		for _, metric := range *metrics {
			metricCIDs = append(metricCIDs, metric.CID)
		}
	}

	return metricCIDs, nil
}

func (c *client) GetConsulHosts() ([]string, error) {
	if c.consulHostCache != nil {
		return c.consulHostCache, nil
	}

	queryOpts := &consulapi.QueryOptions{
		AllowStale: true,
	}
	nodes, _, err := c.consulClient.Catalog().Nodes(queryOpts)
	if err != nil {
		return nil, errwrap.Wrapf("unable to query consul catalog nodes: {{err}}", err)
	}

	hosts := make([]string, 0, len(nodes))
	for _, node := range nodes {
		hosts = append(hosts, node.Node)
	}

	c.consulHostCache = hosts

	return c.consulHostCache, nil
}

func (c *client) PrintStats() {
	fmt.Println("Summary:")
	mode := "live"
	if c.dryRun {
		mode = "dry-run"
	}
	output := []string{
		fmt.Sprintf("Disabled Targets %s | %d", mode, disabledTargets),
		fmt.Sprintf("Excluded Targets %s | %d", mode, excludedTargets),
		fmt.Sprintf("Disabled Metrics %s | %d", mode, disabledMetrics),
		fmt.Sprintf("Enabled Metrics %s | %d", mode, enabledMetrics),
		fmt.Sprintf("Number of Nomad Clients | %d", numNomadClients),
		fmt.Sprintf("Number of live allocs | %d", numLiveAllocs),
		fmt.Sprintf("Number of active nomad alloc metrics | %d", numActiveNomadAllocMetrics),
		fmt.Sprintf("Number of available nomad alloc metrics | %d", numAvailableNomadAllocMetrics),
	}
	result := columnize.SimpleFormat(output)
	fmt.Println(result)
}

func (c *client) Validate() error {
	if c.circonusClient == nil {
		return fmt.Errorf("Circonus client can not be nil")
	}

	switch c.mode {
	case "query":
	case "consul/nomad":
		if c.consulClient == nil {
			return fmt.Errorf("Consul client can not be nil")
		}
	default:
		return fmt.Errorf("unsupported mode: %q", c.mode)
	}

	return nil
}

func (c *client) buildNomadNameIDCache() (map[string]string, error) {
	queryOpts := &nomadapi.QueryOptions{
		AllowStale: true,
	}
	nodes, _, err := c.nomadClient.Nodes().List(queryOpts)
	if err != nil {
		return nil, errwrap.Wrapf("unable to query Nomad nodes: {{err}}", err)
	}

	nodeCache := make(map[string]string, len(nodes))
	for _, node := range nodes {
		nodeCache[node.Name] = node.ID
	}

	numNomadClients = uint(len(nodeCache))

	return nodeCache, nil
}

func findSets(a, b []string) (aOnly, bOnly, union []string) {
	vals := make(map[string]byte, len(a)+len(b))

	for _, v := range a {
		vals[v] = 'a'
	}

	for _, v := range b {
		if _, found := vals[v]; found {
			vals[v] = 'u'
		} else {
			vals[v] = 'b'
		}
	}

	mapToSlice := func(m map[string]byte, selector byte) []string {
		l := make([]string, 0, len(m))
		for k, v := range m {
			if v == selector || selector == 'u' {
				l = append(l, k)
			}
		}
		return l
	}

	return mapToSlice(vals, 'a'), mapToSlice(vals, 'b'), mapToSlice(vals, 'u')
}
