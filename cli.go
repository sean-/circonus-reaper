package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
)

type stringSliceArg []string

// String prints the list of arguments as a comma separated string
func (s *stringSliceArg) String() string {
	return strings.Join(*s, ", ")
}

// Set adds a value to the StringSliceArg
func (s *stringSliceArg) Set(str string) error {
	re := regexp.MustCompile(`^[\S]+$`)
	if !re.MatchString(str) {
		return fmt.Errorf("Invalid hostname: %q", str)
	}

	*s = append(*s, str)

	return nil
}

type cliConfig struct {
	circonusAPIKey  *string
	circonusAppName *string
	consulAddr      *string
	dryRun          bool
	excludedTargets []string
	nomadAddr       *string
}

func parseCLI() (*cliConfig, error) {
	var circonusAPIKey string
	flag.StringVar(&circonusAPIKey, "circonus-api-key", "", "Circonus API Key (CIRCONUS_API_KEY)")

	var circonusAppName string
	flag.StringVar(&circonusAppName, "circonus-app-name", "reaper", "Name to use as the application name in the Circonus API Token UI")

	var consulAddr string
	flag.StringVar(&consulAddr, "consul-addr", "127.0.0.1:8500", "Consul Agent Address")

	var excludeTargetArg stringSliceArg
	flag.Var(&excludeTargetArg, "exclude-target", "Targets to exclude (may be set more than once)")

	var dryRun bool
	flag.BoolVar(&dryRun, "dry-run", false, "Do not make any actual changes")

	var nomadAddr string
	flag.StringVar(&nomadAddr, "nomad-addr", "http://127.0.0.1:4646", "Nomad Agent Address")

	flag.Parse()

	if circonusAPIKey == "" {
		circonusAPIKey = os.Getenv("CIRCONUS_API_KEY")
	}

	return &cliConfig{
		circonusAPIKey:  &circonusAPIKey,
		circonusAppName: &circonusAppName,
		consulAddr:      &consulAddr,
		dryRun:          dryRun,
		excludedTargets: excludeTargetArg,
		nomadAddr:       &nomadAddr,
	}, nil
}
