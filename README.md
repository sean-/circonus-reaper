# `circonus-reaper`

The `circonus-reaper` deactivates metrics that are not explicitly excluded as an
`-exclude-target`.

Specifically:

- metrics attached to hosts that are no longer known by Consul are deactivated
- metrics attached to Nomad allocations that are not running are deactivated
  (including periodic jobs)

## Installation

`go get -u github.com/sean-/circonus-reaper`

## Example Usage

```
$ circonus-reaper \
    -consul-addr=consul.service.consul:8500 \
    -exclude-target=127.0.0.1 \
    -exclude-target=1.2.3.4 \
    -exclude-target=q._caql \
    -exclude-target=rabbitmq.service.consul \
    -exclude-target=pg.example._aws \
    -nomad-addr=http://nomad.service.consul:4646/
```
