# `circonus-reaper`

The `circonus-reaper`:

- deactivates check bundles that were targeting hosts that are no longer present
  in Consul but are known to Circonus
- deactivates individual metrics in check bundles that belong to Nomad
  allocations that are no longer scheduled

## Installation

`go get -u github.com/sean-/circonus-reaper`

## Usage

```
Usage of circonus-reaper:
  -circonus-api-key string
    	Circonus API Key (CIRCONUS_API_KEY)
  -circonus-app-name string
    	Name to use as the application name in the Circonus API Token UI (default "reaper")
  -consul-addr string
    	Consul Agent Address (default "127.0.0.1:8500")
  -dry-run
    	Do not make any actual changes
  -exclude-target value
    	Targets to exclude (may be set more than once)
  -nomad-addr string
    	Nomad Agent Address (default "http://127.0.0.1:4646")
```

### Example Usage

```
$ circonus-reaper \
    -consul-addr=consul.service.consul:8500 \
    -exclude-target=127.0.0.1 \
    -exclude-target=q._caql \
    -exclude-target=rabbitmq.service.consul \
    -exclude-target=pg.example._aws \
    -nomad-addr=http://nomad.service.consul:4646/
```
