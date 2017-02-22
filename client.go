package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"

	circonusapi "github.com/circonus-labs/circonus-gometrics/api"
	"github.com/circonus-labs/circonus-gometrics/api/config"
	consulapi "github.com/hashicorp/consul/api"
	"github.com/hashicorp/errwrap"
	nomadapi "github.com/hashicorp/nomad/api"
)

type client struct {
	circonusClient *circonusapi.API
	consulClient   *consulapi.Client
	excludeTargets map[string]bool
	nomadClient    *nomadapi.Client

	prefixSearch bool
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
	return found
}

func (c *client) ExcludedTargets() []string {
	targets := make([]string, 0, len(c.excludeTargets))
	for k, v := range c.excludeTargets {
		if v {
			targets = append(targets, k)
		}
	}
	return targets
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
	}

	return hosts, nil
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

	return hosts, nil
}

func (c *client) Validate() error {
	if c.circonusClient == nil {
		return fmt.Errorf("Circonus client can not be nil")
	}

	if c.consulClient == nil {
		return fmt.Errorf("Consul client can not be nil")
	}

	return nil
}
