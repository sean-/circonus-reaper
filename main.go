package main

import (
	"log"
	"os"

	circonusapi "github.com/circonus-labs/circonus-gometrics/api"
	consulapi "github.com/hashicorp/consul/api"
	"github.com/hashicorp/errwrap"
	nomadapi "github.com/hashicorp/nomad/api"
)

func main() {
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

	if err := client.DeactivateUnknownHosts(); err != nil {
		log.Printf("ERROR: unable to deactivate unknown hosts: %v", err)
		os.Exit(1)
	}

	if err := client.DeactivateNomadCompletedAllocs(); err != nil {
		log.Printf("ERROR: unable to deactivate completed nomad allocs: %v", err)
		os.Exit(1)
	}

	client.PrintStats()
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
		dryRun:         cli.dryRun,
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
