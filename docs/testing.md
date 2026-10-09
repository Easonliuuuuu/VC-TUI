# Testing

Run the tiers relevant to your change. Unit tests check logic; simulator tests
check collection and CLI behavior; TUI scenarios check the model; PTY journeys
check the running terminal process. The [test catalogue](test-catalog.md)
lists suites and fixtures; [testbed setup](testbed.md) describes profile
boundaries. Automated fixtures are synthetic and do not prove real-vSphere
behavior.

## Unit and package tests

Run the untagged suite and static checks:

```sh
go test -race ./...
go vet ./...
```

The CI `build` job runs these on Linux, macOS, and Windows. Linux also records
whole-suite coverage, without enforcing a percentage threshold:

```sh
go test -race -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
go tool cover -html=coverage.out -o coverage.html
```

Keep `-coverpkg=./...`: the separate `tests` package exercises the CLI and
internal packages through the simulator. Package-local coverage would miss
those calls. CI retains the profile and HTML report.

## In-process simulator tests

The untagged `tests/` suite starts `govmomi/simulator` inside the test process
and drives the real CLI through direct, SOCKS5, and HTTP(S) routes. It covers
credentials, deadlines, partial collection, offline exports, and read-only
SOAP auditing. It runs with the unit suite above.

License tests use labelled synthetic records and planted fake keys to check
redaction, coverage, default-off collection, and deterministic offline export.
File-inventory tests check bounds, denial, truncation, coverage, byte sizes,
stable export, and that file inventory cannot change health or orphan verdicts.
Neither validates real server behavior or RVTools values. See the recorded
[file-inventory comparison and remaining limits](exports.md#columns-and-compatibility)
for real-datastore evidence.

<span id="219"></span>

## Synthetic TUI scenarios

Use the checked-in harness to drive the Bubble Tea model with deterministic
fixtures:

```sh
scripts/testbed list
scripts/testbed test
scripts/testbed test partial-failure
```

Scenarios check visible behavior, model observations, and read-only and
credential-safety invariants. Critical screens also have ANSI-normalized
goldens at `60x20`, `100x30`, and `140x40`. Review rendering changes before
using the explicit `--update-goldens` flag; see [render contracts](testbed.md#render-contracts).

## Linux PTY process tests

```sh
scripts/testbed pty --results-dir /tmp/vsfleet-pty
```

The tagged suite builds and runs `cmd/vsfleet-testbed` in a pseudo-terminal
with key input, resize events, signals, loopback endpoints, and isolated state.
It checks semantic output and clean exits rather than complete byte snapshots.
The [journey matrix](test-catalog.md#pty-journeys) records coverage. Failure
artifacts include redacted process output, a normalized transcript, event logs,
result metadata, and testbed state. PTY validation is Linux-only.

## Out-of-process vcsim integration

Each endpoint runs in an independent `cmd/vsfleet-vcsim` process on a
kernel-assigned loopback port. The suite fetches the TLS certificate thumbprint
to check readiness and captures process output for failure artifacts. The
launcher uses the govmomi version in `go.mod`; v0.56.0 provides the simulator
library but no installable `github.com/vmware/govmomi/vcsim` module.

```sh
go build -o /tmp/vsfleet-vcsim ./cmd/vsfleet-vcsim
VSFLEET_VCSIM_BIN=/tmp/vsfleet-vcsim \
VSFLEET_VCSIM_REQUIRED=1 \
VSFLEET_VCSIM_LOG_DIR=/tmp/vsfleet-vcsim-logs \
go test -tags integration -race -run '^TestVCSIM' ./tests/... -timeout 20m
```

### Fixture catalogue

See the [fixture matrix](test-catalog.md#fixture-catalogue) and
[integration cases](test-catalog.md#multi-vcenter-vcsim-integration).
Fixture topology is deterministic, but endpoint identities are independent.
vcsim performance samples vary, so tests assert coverage, provenance, and
status rather than sample values.

## Fuzzing

Seeds run with `go test ./...`. `fuzz.yml` runs five minutes per target
nightly and one minute on PRs that touch fuzzing. Start a campaign manually
from **Actions → Fuzz → Run workflow**. Commit failing inputs under the
package's `testdata/fuzz/<Target>/` so they become regression seeds. The
[target matrix](test-catalog.md#fuzz-targets) lists inputs and invariants.

## Kubernetes end-to-end

Create the kind cluster and load `vsfleet:ci-test` and
`vsfleet-vcsim:ci-test` images first, as the `kubernetes-e2e` CI job does.
The script assumes those prerequisites exist:

```sh
scripts/test-kubernetes.sh
```

The `kubernetes-e2e` CI job runs the shipped CronJob in kind against two vcsim
Services. It checks ConfigMap/Secret mounts, PVC ownership, DNS, persistence,
and partial-capture exits. See the [case matrix](test-catalog.md#kubernetes-end-to-end)
and script for prerequisites. These checks validate container deployment with
synthetic endpoints.

## Release checks

`scripts/check-release-pins.sh` runs in the Linux `build` job to catch base
image, annotation, and released-version drift. `release-snapshot` uses the
release goreleaser configuration with `--snapshot --skip=publish,sign`, then
smoke-tests the binaries and image. Leave its six cross-compiles and two image
builds to CI on a small workstation. The [release matrix](test-catalog.md#release-snapshot-and-pins)
describes the checks.

## What vcsim does not prove

Simulators test process isolation, collection, identity joins, persistence,
partial coverage, and exercised TLS/credential paths. Real validation is still
needed for physical storage, ESXi kernel behavior, VMXNET3/UPT, SR-IOV, vGPU,
RDM, patch-release differences, performance counters, and migration.
Automated tests also leave real desktop keyrings, macOS/Windows terminal
process behavior, and the published signed image unverified.

## Real vCenter validation

vsfleet has been tested against vCenter Server 8.0.3. This does not establish
acceptance of every feature. Record the server versions, fixture conditions,
and results for each manual acceptance check in its issue or release PR.

### Datastore browser acceptance

Relationship-aware datastore browsing still needs acceptance against a real
read-only account on a nested VMFS or NFS datastore. Record vCenter and ESXi
versions and check:

- A base VMDK, snapshot-chain file, template disk, shared/multi-reference disk,
  and unreferenced descriptor.
- Missing `Datastore.Browse` and partially unreadable VM configuration.
- Path metadata, UNKNOWN/partial rendering, cancellation, and exact
  VM/template jumps.
- No datastore or VM mutation.

Until those results are recorded, real-vSphere acceptance remains open.
