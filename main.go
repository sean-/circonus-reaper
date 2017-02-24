package main

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	circonusapi "github.com/circonus-labs/circonus-gometrics/api"
	"github.com/circonus-labs/circonus-gometrics/api/config"
	consulapi "github.com/hashicorp/consul/api"
	"github.com/hashicorp/errwrap"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/ryanuber/columnize"
)

func main() {
	// Stats counters
	var disabledTargets, disabledMetrics, enabledMetrics, numLiveAllocs, numActiveNomadAllocMetrics, numAvailableNomadAllocMetrics uint

	cliConfig, err := parseCLI()
	if err != nil {
		log.Printf("ERROR: parsing CLI: %v", err)
		os.Exit(1)
	}

	client, err := setup(cliConfig)
	if err != nil {
		log.Printf("ERROR: setting up client: %v", err)
		os.Exit(1)
	}

	consulHosts, err := client.GetConsulHosts()
	if err != nil {
		log.Printf("ERROR: getting Consul hosts: %v", err)
		os.Exit(1)
	}

	circonusTargets, err := client.GetCirconusTargets()
	if err != nil {
		log.Printf("ERROR: getting Circonus targets: %v", err)
		os.Exit(1)
	}

	consulOnly, circonusOnly, consulAndCirconusHosts := findSets(consulHosts, circonusTargets)
	_, _, _ = consulOnly, circonusOnly, consulAndCirconusHosts

	extraHosts, _, _ := findSets(circonusOnly, client.ExcludedTargets())

	// Disable all metrics for a given host that doesn't exist in consul
	for _, host := range extraHosts {
		if cliConfig.dryRun {
			log.Printf("INFO: dry-run: disabling checks on target host %q", host)
			disabledTargets++
			continue
		} else {
			log.Printf("INFO: disabling checks on target host %q", host)
		}

		if err = client.DisableTargetChecks(host); err != nil {
			log.Printf("ERROR: unable to disable checks on targets %q: %v", host, err)
			continue
		}
		disabledTargets++
	}

	nomadNameToID, err := buildNomadNameIDCache(client)
	if err != nil {
		log.Printf("ERROR: unable to populate Nomad Node to ID cache: %v", err)
		os.Exit(1)
	}

	checkBundleCIDRE := regexp.MustCompile(config.CheckBundleCIDRegex)

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

		if client.ExcludeTarget(host) {
			log.Printf("INFO: skipping nomad client %q (excluded target)", host)
			continue
		}

		// 2) Pull the nomad allocs for a given target
		log.Printf("TRACE: searching nomad client %q", host)
		allocIDs, err := client.FindAllocIDsByNodeID(nodeID)
		if err != nil {
			log.Printf("ERROR: unable to find node ID from host %q: %v", host, err)
			continue
		}
		numLiveAllocs += uint(len(allocIDs))

		checkBundles, err := client.FindCheckBundlesByTarget(host)
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
			cbm, err := client.circonusClient.FetchCheckBundleMetrics(circonusapi.CIDType(&checkBundleMetricIDStr))
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
					if cliConfig.dryRun {
						log.Printf("INFO: dry-run: about to update %q's check_bundle_metric %q", host, cbm.CID)
						continue
					} else {
						log.Printf("INFO: about to update %q's check_bundle_metric %q", host, cbm.CID)
					}

					if _, err := client.circonusClient.UpdateCheckBundleMetrics(cbm); err != nil {
						log.Printf("ERROR: unable to update check bundle metrics for CID %q: %v", cbm.CID, err)
						continue
					}
				}
			}
		}
	}

	fmt.Println("Summary:")
	mode := "live"
	if cliConfig.dryRun {
		mode = "dry-run"
	}
	output := []string{
		fmt.Sprintf("Disabled Targets %s | %d", mode, disabledTargets),
		fmt.Sprintf("Disabled Metrics %s | %d", mode, disabledMetrics),
		fmt.Sprintf("Enabled Metrics %s | %d", mode, enabledMetrics),
		fmt.Sprintf("Number of non-Consul Hosts | %d", len(extraHosts)),
		fmt.Sprintf("Number of Nomad Clients | %d", len(nomadNameToID)),
		fmt.Sprintf("Number of Circonus and Consul Hosts | %d", len(consulAndCirconusHosts)),
		fmt.Sprintf("Number of live allocs | %d", numLiveAllocs),
		fmt.Sprintf("Number of active nomad alloc metrics | %d", numActiveNomadAllocMetrics),
		fmt.Sprintf("Number of available nomad alloc metrics | %d", numAvailableNomadAllocMetrics),
	}
	result := columnize.SimpleFormat(output)
	fmt.Println(result)
}

func buildNomadNameIDCache(c *client) (map[string]string, error) {
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

func setup(cli *cliConfig) (*client, error) {
	circonusClient, err := setupCirconusClient(cli)
	if err != nil {
		return nil, errwrap.Wrapf("unable to setup Circonus client: {{err}}", err)
	}

	consulClient, err := setupConsulClient(cli)
	if err != nil {
		return nil, errwrap.Wrapf("unable to setup Nomad client: {{err}}", err)
	}

	nomadClient, err := setupNomadClient(cli)
	if err != nil {
		return nil, errwrap.Wrapf("unable to setup Nomad client: {{err}}", err)
	}

	c := &client{
		circonusClient: circonusClient,
		consulClient:   consulClient,
		nomadClient:    nomadClient,
		excludeRegexps: cli.excludeRegexps,
	}

	c.excludeTargets = make(map[string]bool, len(cli.excludedTargets))
	for _, v := range cli.excludedTargets {
		c.excludeTargets[v] = true
	}

	if err := c.Validate(); err != nil {
		return nil, errwrap.Wrapf("client state does not validate: {{err}}", err)
	}

	return c, nil
}

func setupCirconusClient(cli *cliConfig) (*circonusapi.API, error) {
	cfg := &circonusapi.Config{
		TokenApp: *cli.circonusAppName,
		TokenKey: *cli.circonusAPIKey,
	}

	c, err := circonusapi.NewClient(cfg)
	if err != nil {
		return nil, errwrap.Wrapf("unable to create a new Circonus client: {{err}}", err)
	}

	return c, nil
}

func setupConsulClient(cli *cliConfig) (*consulapi.Client, error) {
	consulConfig := consulapi.DefaultConfig()
	if cli.consulAddr != nil && *cli.consulAddr != "" {
		consulConfig.Address = *cli.consulAddr
	}

	consulClient, err := consulapi.NewClient(consulConfig)
	if err != nil {
		return nil, errwrap.Wrapf("unable to create new Consul client: {{err}}", err)
	}

	return consulClient, nil
}

func setupNomadClient(cli *cliConfig) (*nomadapi.Client, error) {
	nomadConfig := nomadapi.DefaultConfig()
	if cli.nomadAddr != nil && *cli.nomadAddr != "" {
		nomadConfig.Address = *cli.nomadAddr
	}

	nomadClient, err := nomadapi.NewClient(nomadConfig)
	if err != nil {
		return nil, errwrap.Wrapf("unable to create new Nomad client: {{err}}", err)
	}

	return nomadClient, nil
}
