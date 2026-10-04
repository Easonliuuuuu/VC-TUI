# Testing

vsfleet has complementary test tiers. Each tier answers a different question,
so passing one is not evidence that the others are unnecessary. The synthetic
lab and scenario harness are described in [testbed.md](testbed.md), and
[test-catalog.md](test-catalog.md) lists every suite, scenario, journey, and
fuzz target with the situation it simulates.

## Synthetic TUI scenarios

The repository-owned `scripts/testbed` harness drives the Bubble Tea model with
the same deterministic fixtures that developers can inspect in sandbox mode:

```sh
scripts/testbed list
scripts/testbed test
scripts/testbed test partial-failure
```

The scenario layer asserts user-visible semantics, model observations, and
read-only/credential-safety invariants. Five stable screens also have
ANSI-normalized render contracts at `60x20`, `100x30`, and `140x40`. Goldens
are changed only with the explicit `--update-goldens` flag.

## Linux PTY process tests

The tagged PTY suite is deliberately separate from the model-boundary
scenarios. It builds and runs the actual connected `cmd/vsfleet-testbed`
binary with terminal input, output, resize events, signals, loopback simulator
endpoints, and isolated on-disk state:

```sh
scripts/testbed pty --results-dir /tmp/vsfleet-pty
```

The nine journeys launch inventory and quit, prove SSH failure restoration and
Ctrl-C cancellation, cancel a credential prompt and continue, browse and
recursively search a datastore, traverse and leave every History pane, resize
from `60x20` to `140x40`, page through the VM performance dashboard and quit
with a query in flight, and interrupt an active capture. They assert semantic
output and clean exit behavior, not complete terminal byte snapshots. On
failure, CI uploads redacted process output, an ANSI-normalized transcript, an
event log, result metadata, and the isolated testbed state.

Fuzz seeds for the TUI and for the configuration, SSH config, `--where`, and
RVTools import parsers run with the normal Go suite; `fuzz.yml` runs a
five-minute campaign per target every week. Neither the headless scenarios nor PTY tests prove
behavior against a real vSphere deployment.

## Release checks

`scripts/check-release-pins.sh` runs in the Linux `build` job and fails when
the goreleaser base-image annotation drifts from the `Dockerfile` digest, or
when a pinned image version in the docs or the shipped CronJob differs from
the released version. The `release-snapshot` job runs the release goreleaser
configuration with `--snapshot --skip=publish,sign` and smoke-tests the
resulting binaries and image. It is heavy (six cross-compiles plus two image
builds), so leave it to CI rather than running it on a small workstation.

## Unit and package tests

The untagged suite covers pure domain behavior, persistence, topology joins,
health/readiness rules, CLI contracts, transport, and the read-only vSphere
inventory layer:

```sh
go test -race ./...
go vet ./...
```

These tests are fast and run on every operating system in the normal CI
`build` job. The Linux run also publishes a whole-suite coverage report:

```sh
go test -race -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
go tool cover -html=coverage.out -o coverage.html
```

`-coverpkg=./...` is deliberate: the in-process simulator tests below are in
the separate `tests` package and exercise the real CLI plus its internal
packages. A package-local profile would not credit that coverage. CI reports
the result and retains the raw profile and HTML report as artifacts; it does
not enforce a percentage threshold.

## In-process simulator tests

The existing `tests/` package starts `govmomi/simulator` in the test process.
It drives the real CLI through direct, SOCKS5, and HTTP proxy routes and is
the right tier for command behavior, credentials, timeout handling, and
read-only SOAP auditing without external dependencies.

The license-metadata tests (`internal/vsphere/license*_test.go`,
`internal/report/license_test.go`, `tests/cli_license_test.go`) run against
synthetic, visibly labelled licenses seeded into the in-process simulator. They
plant distinctive fake license keys and assert those strings appear in no CLI
output, JSON, XLSX, CSV or byte of the history database; that a denied or empty
license answer becomes `unavailable` coverage; that a default assessment
collects nothing; and that re-export is deterministic and needs no
configuration. They prove the mapping and redaction, not real-vSphere license
behavior.

The opt-in datastore file inventory (`vFileInfo`) is covered at each layer
with synthetic files only: `internal/vsphere` drives the simulator for sizes,
types, denial, truncation, budget skipping and timeouts; `internal/report`
proves byte sizes, path mapping, stable order, byte-identical re-export and
every `vsfleetCoverage` state on a multi-datastore fixture;
`internal/health` proves the inventory never changes orphan or health
conclusions; and `tests/cli_file_inventory_test.go` proves that a default
capture never browses, that limit flags cannot enable browsing, that export
works from an empty configuration, and that denied and truncated scans are
visible in human and JSON output. None of that is real-vSphere evidence: the
comparison with an RVTools export of the same datastores is tracked on issue
#219.

## Out-of-process vcsim integration

The tagged suite starts one independent `cmd/vsfleet-vcsim` process per
endpoint. The launcher is built against the govmomi version in `go.mod` because
govmomi v0.56.0 ships the simulator library but not an installable
`github.com/vmware/govmomi/vcsim` module. Each process has a kernel-assigned
loopback port, TLS readiness is verified by fetching its certificate
thumbprint, and stdout/stderr are captured for CI failure artifacts.

Run it locally with:

```sh
go build -o /tmp/vsfleet-vcsim ./cmd/vsfleet-vcsim
VSFLEET_VCSIM_BIN=/tmp/vsfleet-vcsim \
VSFLEET_VCSIM_REQUIRED=1 \
VSFLEET_VCSIM_LOG_DIR=/tmp/vsfleet-vcsim-logs \
go test -tags integration -race -run '^TestVCSIM' ./tests/... -timeout 20m
```

### Fixture catalogue

The fixtures are deterministic flag sets; endpoint processes still receive
independent vCenter and VM identities.

| Fixture | Topology and purpose |
| --- | --- |
| `basic-multivcenter` | `vc-prod`: two datacenters, one cluster per datacenter, two hosts per cluster, three VMs per resource pool, three datastores, three port groups, and one vApp per cluster. `vc-edge`: one datacenter, one cluster, two hosts, one VM pool, one datastore, one port group, and one vApp. |
| `duplicate-names` | Two identical one-datacenter estates. Clustered VM names such as `DC0_C0_RP0_VM0` and local datastore names such as `LocalDS_0` collide deliberately across contexts. |
| `partial-failure` | Healthy and killable external endpoints plus a closed-port context from the existing failure helper. |
| `topology` | One datacenter, one cluster, two hosts, two VMs, two datastores, two port groups, and one vApp for exact hierarchy/attachment assertions. |
| `history` | The basic small shape, captured three times with power and naming mutations between captures. |

The existing fixtures also carry the offline and collection scenarios: a lost
context between two captures (`partial-failure`, diff and VM history report it
as not covered rather than removed), destination sizing over complete and
partial runs (`basic-multivcenter`), scoped pseudonymized exports
(`duplicate-names`) and `assessment perf collect` with one endpoint taken down
(`basic-multivcenter`). vcsim returns randomised performance samples, so the
perf scenario asserts contexts, coverage, provenance and status, never values.

## What vcsim does not prove

vcsim proves process isolation, CLI behavior, inventory collection, topology
correlation, historical persistence, partial-coverage semantics, and the
certificate/credential plumbing exercised by these fixtures. It is not a
replacement for validation against real vSphere.

In particular, it does not prove physical storage paths, ESXi
kernel/storage semantics, real VMXNET3 or UPT behavior, SR-IOV/vGPU/RDM,
patch-release quirks, or actual VM migration. k3s remains deferred until a
concrete test requirement justifies adding it; containers are not a goal of
this integration tier.
