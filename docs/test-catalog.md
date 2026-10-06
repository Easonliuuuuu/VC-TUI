# Test Catalogue

This page is the map of every automated test in vsfleet: what each suite runs,
which situation it simulates, what it asserts, and where it runs in CI. Read
[Testing](testing.md) for how to run each tier and why the tiers are kept
separate, and [Synthetic Testbed](testbed.md) for the fixtures they share.

Every fixture is synthetic. No suite talks to a real vCenter, so none of them
is evidence about real vSphere behavior; [what nothing here
proves](#what-nothing-here-proves) lists the gaps.

## At a glance

| Tier | Command | CI job | Runs | Simulates |
| --- | --- | --- | --- | --- |
| [Unit and package](#unit-and-package-tests) | `go test -race ./...` | `build` (Linux, macOS, Windows) | every push and PR | pure logic, persistence, CLI wiring, one in-process simulator per test |
| [In-process simulator](#in-process-simulator-tests) | same command, `tests/` package | `build` | every push and PR | the real CLI against `govmomi/simulator`, direct and through SOCKS5/HTTP proxies |
| [Multi-vCenter integration](#multi-vcenter-vcsim-integration) | `go test -tags integration -run '^TestVCSIM' ./tests/...` | `integration-vcsim` | every push and PR | several independent vCenter processes, one of which can be killed |
| [TUI scenarios](#tui-scenarios) | `scripts/testbed test` | `tui-scenarios` (Linux, macOS, Windows) | every push and PR | the TUI model driven key by key, with render goldens |
| [PTY journeys](#pty-journeys) | `scripts/testbed pty` | `tui-pty` (Linux) | every push and PR | the real testbed binary in a real terminal |
| [Fuzzing](#fuzz-targets) | seeds: `go test ./...`; campaigns: `go test -fuzz` | seeds in `build`; campaigns in `fuzz.yml` | seeds always; 5 min per target nightly, 1 min on PRs that touch fuzzing | arbitrary keys, messages, files, and expressions |
| [Kubernetes end-to-end](#kubernetes-end-to-end) | `scripts/test-kubernetes.sh` | `kubernetes-e2e` | every push and PR | the shipped CronJob in a kind cluster against two vcsim Services |
| [Release snapshot](#release-snapshot-and-pins) | `goreleaser release --snapshot` | `release-snapshot` | every push and PR | the full release build without publishing |
| [Release pins](#release-snapshot-and-pins) | `scripts/check-release-pins.sh` | `build` (Linux) | every push and PR | drift between Dockerfile, goreleaser, docs, and the CronJob |

The `build` job also runs `gofmt`, `go mod tidy`, `go vet`, the labeler
workflow tests (`node --test scripts/test-labeler.cjs`) and a coverage report.
`staticcheck` runs as its own job, and `docs.yml` builds this site with
`mkdocs build --strict`.

## Unit and package tests

Untagged `_test.go` files next to the code. They run on all three operating
systems with `-race`. Packages that need a vCenter start
`govmomi/simulator` in-process; nothing here starts a separate process.

| Package | What it covers | Situations simulated |
| --- | --- | --- |
| `internal/assessment` | The history store, diffs, trends, capacity reports, timelines, policy, perf windows, schema migrations | interrupted captures, concurrent writers fenced by a lease, a VM renamed or moved across vCenters, a context missing from one run, failed collections that must not read as removals, migrating older database schemas |
| `internal/cli` | Command wiring, flags, exit codes, JSON shapes, help text | unknown contexts, `--all-contexts` overrides, non-finite thresholds, RVTools import dry runs and repeats, offline exports that must not clobber files, every command having runnable examples |
| `internal/config` | Loading, validating, and saving configuration; SSH routes; thumbprints | future config versions, duplicate names, dangling `via` provenance, routes that must survive a save |
| `internal/credentials` | `keyring:`, `prompt:`, `env:`, `file:` and `exec:` credential references | an unavailable keyring falling back to the prompt, unset vs empty variables, failing, silent, missing, or slow helpers, non-interactive references that must never prompt |
| `internal/decommission` | The VM decommission check | strict blockers, unknown coverage, ambiguous identity, unresolved dependencies |
| `internal/demo` | The offline demo estate | determinism, production-like size, referential integrity, shared names across contexts, five differing history runs |
| `internal/health` | Health rules, readiness, orphan and zombie disk detection | threshold boundaries, empty state treated as unknown, partial or truncated datastore browsing, same-named datastores, schema-gated rules, passthrough devices |
| `internal/humanize`, `internal/limiter`, `internal/uistate` | Formatting, bounded concurrency, persisted TUI state | cancellation while waiting for a slot, corrupt or missing state files, untrustworthy saved SSH destinations |
| `internal/metareport` | Saved tag/custom-attribute reports | unreadable metadata sources giving *undetermined* rather than *no*, failed collections not reported as removals |
| `internal/network` | Cross-vCenter network comparison | VLAN gaps, MTU mismatches, a blind distributed switch, ambiguous cluster names |
| `internal/perf` | Performance summaries and sizing classification | missing samples that are not zero, too few samples, periodic peaks vs sustained low use, contention |
| `internal/query` | The `--where` predicate language | numeric vs lexical comparison, type errors, tag and custom-attribute predicates that need a name |
| `internal/report` | RVTools-compatible XLSX/CSV export, sharing, licensing, `vFileInfo`, `vSource` | byte-identical re-export, pseudonymized sharing that must fail closed, license keys that must never appear, old runs without newer evidence, worksheet row limits |
| `internal/rvimport` | Importing RVTools workbooks | missing or extra worksheets and columns, duplicate VM identities, oversized workbooks refused before opening, malformed files, repeat imports, imports that can never reach a mutation-capable client |
| `internal/search` | Cross-vCenter search matching | case-insensitive substring matching, kind restriction |
| `internal/session` | Operation deadlines and streaming idle timeouts | long work that keeps streaming, silent streams, timeout errors naming their stage |
| `internal/sizing` | Destination sizing (N-1, growth, storage) | partial coverage that is never *fit*, shared RDMs counted once, missing assumptions reported as unknown |
| `internal/sshalias` | Discovering SSH aliases in `~/.ssh/config` | nested and cyclic `Include`, depth/file/byte/candidate limits, stale aliases pointing at another IP |
| `internal/testbed` | The loopback testbed lab itself | authenticated routes start, and wrong fixture credentials are rejected |
| `internal/topology` | Topology and blast-radius queries | shared datastores across contexts, same-named objects that must stay distinct, blind contexts that downgrade results |
| `internal/transport` | Direct, SOCKS5, and HTTP(S) proxy dialers | offline proxies, ambient proxy variables that must be ignored |
| `internal/tui` | The Bubble Tea model: browse, detail, VM dashboard, history panes, datastore browser, credential and SSH prompts, context forms | stale async replies, narrow terminals, failed reloads keeping stale data, background refresh tiers, demo mode disabling external actions, SSH routing and alias precedence |
| `internal/vsphere` | Read-only inventory collection against the simulator | paging, partial failure of one kind, permission denial recorded as provenance, perf counters with gaps or without samples, license metadata without keys |
| `cmd/vsfleet-demo` | The demo binary | seeded assessment history is wired in |

## In-process simulator tests

The untagged files in `tests/` drive the real CLI end to end against a
`govmomi/simulator` started inside the test process. They run in the `build`
job alongside the unit tests.

| File | Situation | Asserts |
| --- | --- | --- |
| `cli_test.go` | context lifecycle, cross-vCenter search, one vCenter failing | inventory still lists, failures are isolated, `vsfleet` with no subcommand opens the UI |
| `cli_credentials_test.go`, `cli_keyring_test.go`, `contextops_test.go` | `env:`, `file:`, `exec:` credentials, keyring storage, `context add` with `--password-stdin` | non-interactive misses never read stdin, read-only schemes reject `--password-stdin`, a failed connection test blocks saving unless overridden |
| `socks5_test.go`, `httpproxy_test.go` | SOCKS5 with and without remote DNS, HTTP/HTTPS proxies, basic auth | wrong proxy credentials fail, offline proxies fail clearly, untrusted HTTPS proxy certificates are rejected, TLS thumbprint mismatch and discovery |
| `timeout_test.go` | a vCenter that stops answering mid-enumeration | `--timeout` bounds the whole listing |
| `session_invalidation_test.go` | a context edited or removed while a session is open | the edited context talks to the new vCenter; removal closes the session |
| `readonly_test.go` | every command run against a SOAP-auditing simulator | no command issues a mutating call, and no mutation-capable govmomi package is imported |
| `cli_exitcode_test.go` | complete and partial captures | partial exits 0 by default and 3 with `--fail-on-partial` |
| `cli_show_test.go`, `cli_snapshot_test.go`, `cli_network_test.go`, `cli_topology_test.go`, `cli_dvswitch_resourcepool_test.go`, `cli_host_inventory_test.go`, `cli_host_subresource_test.go` | `show`, snapshots, networks, topology, distributed switches, resource pools, host storage and network sheets | stable identities in JSON, ambiguity across contexts until narrowed, host-scoped device evidence that never leaks across hosts |
| `cli_datastore_files_test.go`, `cli_file_inventory_test.go` | datastore browsing and the opt-in file inventory | a default capture never browses, limit flags do not enable browsing, denial and truncation are visible in human and JSON output, export is byte-identical |
| `cli_license_test.go`, `cli_vsource_test.go` | license metadata and `vSource` attribution | planted license keys appear nowhere; denied or empty license answers read as unavailable |

## Multi-vCenter vcsim integration

Tagged `integration`. Each endpoint is a separate `cmd/vsfleet-vcsim` process
on its own loopback port, so a test can kill one mid-run. The fixtures are
described in [Testing](testing.md#fixture-catalogue).

| Test | Fixture | Situation | Asserts |
| --- | --- | --- | --- |
| `TestVCSIMMultiContextInventoryAndProvenance` | `topology` | two vCenters listed together | every row carries its context and vCenter identity |
| `TestVCSIMDuplicateNamesStayDistinct` | `topology` | identical names in two vCenters | rows never merge |
| `TestVCSIMContextIsolation` | `topology` | per-context credentials and sessions | one context never uses another's session |
| `TestVCSIMCLIJSONAndExitContract` | `topology` | the JSON and exit-code contract across processes | stable shapes and exit codes |
| `TestVCSIMTopologyAncestryAndAttachments` | `topology` | hierarchy and attachment queries | exact ancestry and datastore/network attachments |
| `TestVCSIMBlastRadiusNeverCrossesDuplicateNameContexts` | `topology` | blast radius with colliding names | impact never crosses into the other vCenter |
| `TestVCSIMHistoryDiffVMHistoryAndCapacity` | `history` | three captures with power and name changes between them | diff, VM history, and capacity follow the changes |
| `TestVCSIMDiffTreatsLostContextAsUnknownNotRemoved` | `history` | a context missing from the second capture | its VMs are *not covered*, not *removed* |
| `TestVCSIMPartialCoverageSurvivesProcessLoss` | `partial-failure` | an endpoint process killed during an assessment | the run is partial and the healthy context's data is kept |
| `TestVCSIMPerfCollectRecordsProvenanceAndFailures` | `basic-multivcenter` | `assessment perf collect` with one endpoint taken down | per-context status and provenance; values are not asserted because vcsim samples are random |
| `TestVCSIMSizingCompleteAndPartialRuns` | `basic-multivcenter` | destination sizing over complete and partial runs | partial input is never reported as *fit* |
| `TestVCSIMSharedExportPseudonymizesDuplicateNames` | `duplicate-names` | a scoped, pseudonymized export | colliding names stay distinguishable and originals never leak |

## TUI scenarios

`scripts/testbed test` runs each scenario through `cmd/vsfleet-harness`: it
builds the TUI model, sends keys and resize events directly, and checks the
view and the model's semantic `Observation`. Presentation scenarios use the
offline demo estate; connected scenarios start the loopback lab. Scenarios
marked with goldens are also rendered at `60x20`, `100x30`, and `140x40` and
compared with `internal/testbed/scenarios/testdata/golden/`. Update goldens
only with `--update-goldens`, and review the diff.

| Scenario | Profile | Drives | Asserts | Goldens |
| --- | --- | --- | --- | --- |
| `overview` | presentation | load all contexts with `R` | inventory renders while the failed site stays visible | yes |
| `partial-failure` | presentation | load all contexts | the failed `dr-site` context is still shown | no |
| `duplicate-names` | presentation | load all contexts | same-named resources render as context-qualified rows | no |
| `credential-cancel` | presentation | load the demo estate, explicitly reload through the prompt coordinator, capture the open prompt, then cancel it with Esc | the prompt is labelled for `prod-vc`, wraps at each golden size, closes after cancellation, reports the cancellation, and renders no fixture secret | yes |
| `stale-result` | presentation | initial load only | a context is selected. The reordered-reply case itself is covered by `FuzzStaleMessagesCannotReplaceNewerState` and unit tests | no |
| `history-coverage-gap` | presentation | open History with `H` | History opens; the coverage gap is recorded by the goldens | yes |
| `add-context-no-secret` | connected | boots the loopback lab with in-memory keyring references | a context is selected over the production backend. The harness does not yet inspect the saved configuration for a password; `TestContextopsSave*` and `TestFormPreservesANonInteractiveCredential` cover that | no |
| `datastore-browser` | presentation | open Datastores, select the populated `nvme-01` fixture, and open its file browser at the root | the browser stays in datastore mode at `/`, names the datastore, and renders directory entries at each golden size | yes |
| `resize` | presentation | resize through `60x20`, `100x30`, `140x40` | inventory still renders and selection is kept | no |
| `vm-dashboard` | presentation | open the first VM, then visit chart pages `0`–`4` and step ranges `1h`→`30d`→`1h` at each size | every frame fits the terminal's width and height, the selected page tab is shown whole, and the pane stays in detail mode | yes |
| `network-switches` | presentation | open the Networks tab, open DVS-Production and visit both workspace pages at each size; then fold it and open DVS-Storage's Wiring page on its vMotion port group | every frame fits the terminal, both page tabs are shown, and the wiring page names the switch and port group | yes |

## PTY journeys

Tagged `linux && pty`. Each journey builds `cmd/vsfleet-testbed`, starts it in
a real pseudo-terminal with an isolated `HOME`, XDG directories, and loopback
simulators, types like a user, and asserts on the normalized terminal output.
On failure CI uploads the redacted raw output, a plain transcript, an event
log, and the testbed state.

| Journey | Situation | Asserts |
| --- | --- | --- |
| launch inventory and exit cleanly | authenticate through the prompt, then quit | inventory appears; exit status 0 |
| SSH failure restores the TUI | SSH to a host whose `ssh` fails | the failure is reported and the TUI is usable afterwards |
| SSH hang cancels and restores the TUI | an `ssh` that never returns, cancelled with Ctrl-C | the handoff ends as failed and the TUI comes back |
| cancel credential prompt and keep using UI | Esc on the password prompt | the cancellation is reported; help and navigation still work |
| browse datastore find recursively and navigate back | datastore detail, browse, recursive `*.vmdk` find, open the result, walk back to the root | each directory level and the detail pane come back in order |
| switch history panes and return without leaked state | Tab through Changes, Trends, Runs, Health, then leave and reopen | each pane header appears, and inventory state is unchanged after leaving |
| resize narrow to wide without corrupting state | start at `60x20` in History, resize to `140x40`, keep navigating | panes keep working after the resize |
| VM dashboard pages, ranges, resize, and quit mid-query | open a VM against the simulator, switch to the Disk page, step to 24h, resize `60x20`→`140x40`, step to the unloaded 7d range and quit at once | real performance queries render; the page and range survive both resizes; the process exits 0 with a query very likely still in flight |
| ctrl-c cancels active background work | Ctrl-C while an assessment capture is running | exit status 0 |

Send one key per write in a journey: Bubble Tea reads back-to-back runes as a
single multi-rune key, so `"rq"` matches neither reload nor quit.

## Fuzz targets

Seed inputs run as ordinary tests in every `go test ./...`. The nightly
`fuzz.yml` workflow runs each target for 5 minutes in parallel. A pull request
that changes a fuzz target, its seeds, or the workflow gets a 1-minute run per
target, and **Actions → Fuzz → Run workflow** starts one by hand with any
duration. A failing
input lands in the package's `testdata/fuzz/<Target>/`; commit it there and it
becomes a permanent regression test.

| Target | Package | Input | Invariants |
| --- | --- | --- | --- |
| `FuzzTUIKeyResizeNeverPanics` | `internal/tui` | up to 256 arbitrary keys with arbitrary terminal sizes | the model never panics |
| `FuzzStaleMessagesCannotReplaceNewerState` | `internal/tui` | pairs of load generations | a reply from an older generation never paints over newer data |
| `FuzzRenderIsBoundedAndDeterministic` | `internal/tui` | terminal sizes | rendering twice gives the same frame and no line is wider than the terminal |
| `FuzzParseWorkbook` | `internal/rvimport` | a block of arbitrary cells (header row included) written into one worksheet of a real vsfleet-written workbook | `Parse` returns a result or an error, never a panic, and reports the same thing for the same workbook twice |
| `FuzzLoadSaveRoundTrip` | `internal/config` | arbitrary configuration text | anything `Load` accepts survives `Save` and loads back to the same contexts |
| `FuzzNormalizeThumbprintIsIdempotent` | `internal/config` | arbitrary thumbprint text | normalizing twice changes nothing |
| `FuzzDiscover` | `internal/sshalias` | an arbitrary `~/.ssh/config` plus one includable file, under small limits | only `ErrTruncated` as an error, never more aliases than the limit, only literal unique aliases. Includes that could leave the temporary home are skipped |
| `FuzzParseAndEvaluate` | `internal/query` | arbitrary `--where` expressions with tags and custom attributes readable or not | accepted unquoted predicates print back to an identical parse, `tag.`/`custom.` always carry a name, and `Evaluate` never contradicts `Match` |

`FuzzParseAndEvaluate` found that `custom.=""` used to be accepted as a
predicate on an attribute with an empty name. Its failing input is kept as a
seed.

## Kubernetes end-to-end

`scripts/test-kubernetes.sh` applies the checked-in
`deploy/kubernetes/cronjob.yaml` to a kind cluster, with the CI image swapped
in, against two vcsim Deployments (`prod` and `edge`). It then runs the job
four times with different arguments.

| Step | Situation | Asserts |
| --- | --- | --- |
| security contract | the CronJob as shipped | non-root, read-only root filesystem, no privilege escalation, all capabilities dropped, `fsGroup` 65532 |
| `vsfleet-complete` | both vCenters up | assessment status `complete`, 2 of 2 contexts |
| `vsfleet-history` | `assessment list` on the persistent volume | the earlier run was persisted |
| `vsfleet-partial` | `edge` scaled to zero, `--fail-on-partial` | the job fails with exit code 3 and the result is `partial`, 1 of 2 contexts |
| `vsfleet-readiness` | `assessment readiness latest` | the verdict is not `ready` and unresolved items are listed |

This tier is the only one that proves the container works with a mounted
ConfigMap, a Secret used as `file:` credentials, a PVC with the right
ownership, and Service DNS names.

## Release snapshot and pins

`release-snapshot` runs `goreleaser release --snapshot --skip=publish,sign`
with the same Go version and goreleaser configuration as `release.yml`,
except that the multi-platform `index,` annotation prefix is stripped because
buildx rejects it on the per-platform images a snapshot loads. It
checks that an archive exists for each of the six OS/architecture pairs, then
runs `--version` on the Linux binary and `--version`/`--help` in the amd64
container image. A broken `.goreleaser.yaml`, `Dockerfile`, or cross-compile
fails here instead of when a tag is pushed.

`scripts/check-release-pins.sh` fails when:

- `Dockerfile` and `Dockerfile.vcsim` use different base images, or the base
  image is not pinned by digest;
- the `base.name`/`base.digest` OCI annotations in `.goreleaser.yaml` differ
  from the `Dockerfile` (Dependabot bumps only the Dockerfiles);
- any `ghcr.io/easonliuuuuu/vsfleet:vX.Y.Z` pin in `README.md`, `docs/`, or
  `deploy/` differs from `.release-please-manifest.json`, or sits in a file
  that release-please does not bump;
- a release-please `extra-files` entry has no `x-release-please` marker.

## What nothing here proves

- Behavior against real vCenter or ESXi builds, patch-release quirks, or real
  performance counter values.
- Physical storage paths, real VMXNET3/UPT, SR-IOV, vGPU, RDM, or migration.
- Terminal behavior on macOS and Windows consoles: PTY journeys are
  Linux-only, and the scenarios run there only at the model boundary.
- Keyring backends on real desktops: tests use static or isolated keyrings.
- The published, signed image: the snapshot skips signing and publishing.

## Keeping this page current

When you add a scenario, PTY journey, fuzz target, vcsim test, or CI job, add
a row here in the same change. `TestCatalogueDocumentsEveryScenarioAndFuzzTarget`
in `internal/testbed/scenarios` fails when a scenario or fuzz target is
missing from this page or a fuzz target is missing from `fuzz.yml`.
