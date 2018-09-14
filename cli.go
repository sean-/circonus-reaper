package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/hashicorp/errwrap"
	"github.com/pkg/errors"
)

type cliConfig struct {
	circonusAPIKey  *string
	circonusAppName *string
	circonusAPIURL  *string
	consulAddr      *string
	dryRun          bool
	excludedTargets []string
	excludeRegexps  []*regexp.Regexp
	nomadAddr       *string
	mode            string
	metricQuery     string
}

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

func parseCLI() (*cliConfig, error) {
	var circonusAPIKey string
	flag.StringVar(&circonusAPIKey, "circonus-api-key", "", "Circonus API Key (CIRCONUS_API_KEY)")

	var circonusAppName string
	flag.StringVar(&circonusAppName, "circonus-app-name", "reaper", "Name to use as the application name in the Circonus API Token UI")

	var circonusAPIURL string
	flag.StringVar(&circonusAPIURL, "circonus-url", "", "URL for the Circonus API")

	var consulAddr string
	flag.StringVar(&consulAddr, "consul-addr", "127.0.0.1:8500", "Consul Agent Address")

	var excludeRegexpsArg stringSliceArg
	flag.Var(&excludeRegexpsArg, "exclude-regexp", "Regexp for a targets to exclude (may be set more than once)")

	var excludeTargetArg stringSliceArg
	flag.Var(&excludeTargetArg, "exclude-target", "Targets to exclude (may be set more than once)")

	var dryRun bool
	flag.BoolVar(&dryRun, "dry-run", false, "Do not make any actual changes")

	var nomadAddr string
	flag.StringVar(&nomadAddr, "nomad-addr", "http://127.0.0.1:4646", "Nomad Agent Address")

	var mode string
	flag.StringVar(&mode, "mode", "", `Pick a mode to operate in ("query","consul/nomad")`)

	var metricQuery string
	flag.StringVar(&metricQuery, "query", "", "Circonus search query of metrics to disable")

	flag.Parse()

	if circonusAPIKey == "" {
		circonusAPIKey = os.Getenv("CIRCONUS_API_KEY")
	}

	if circonusAPIURL == "" {
		circonusAPIURL = os.Getenv("CIRCONUS_API_URL")
	}

	excludeRegexps := make([]*regexp.Regexp, 0, len(excludeRegexpsArg))
	for _, reArg := range excludeRegexpsArg {
		re, err := regexp.Compile(reArg)
		if err != nil {
			return nil, errwrap.Wrapf(fmt.Sprintf("unable to compile regexp %q: {{err}}", reArg), err)
		}
		excludeRegexps = append(excludeRegexps, re)
	}

	switch mode {
	case "query", "consul/nomad":
	default:
		return nil, errors.Errorf("unknown mode: %q", mode)
	}

	return &cliConfig{
		circonusAPIKey:  &circonusAPIKey,
		circonusAppName: &circonusAppName,
		circonusAPIURL:  &circonusAPIURL,
		consulAddr:      &consulAddr,
		dryRun:          dryRun,
		excludeRegexps:  excludeRegexps,
		excludedTargets: excludeTargetArg,
		nomadAddr:       &nomadAddr,
		mode:            mode,
		metricQuery:     metricQuery,
	}, nil
}
