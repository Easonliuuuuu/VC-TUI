# Test catalogue

This reference lists automated suites, cases, and CI coverage. See
[Testing](testing.md) for execution and limitations and
[Testbed](testbed.md) for fixture setup. Every fixture is synthetic; no suite
connects to a real vCenter.

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

Untagged `_test.go` files run on Linux, macOS, and Windows with `-race`.
Packages needing a vCenter use an in-process simulator.

| Package | What it covers | Situations simulated |
| --- | --- | --- |
| `internal/assessment` | History, diffs, trends, capacity, timelines, policy, perf, migrations | interruption, writer leases, renamed/moved VMs, missing contexts, failed collections, old schemas |
| `internal/cli` | Commands, flags, exits, JSON, examples | invalid inputs, offline import/export, no clobbering, add-context credential choices without a keyring |
| `internal/config` | Load/save validation, SSH routes, thumbprints | future versions, duplicate names, dangling `via`, route preservation |
| `internal/contextops` | CLI/TUI context saving | keyring cleanup only after save and when requested; shared keys preserved; cleanup failure cannot undo edit |
| `internal/credentials` | Keyring, prompt, env, file, exec sources | lookup-only probing, isolated resolvers, unset/empty variables, missing/failing/silent/slow helpers, no unattended prompts, concise failure reasons |
| `internal/decommission` | The VM decommission check | strict blockers, unknown coverage, ambiguous identity, unresolved dependencies |
| `internal/demo` | Offline demo estate | determinism, realistic size, referential integrity, duplicate names, five history runs |
| `internal/health` | Health, readiness, orphan/zombie disks | thresholds, empty/partial/truncated evidence, duplicate datastore names, schema gates, passthrough |
| `internal/humanize`, `internal/limiter`, `internal/uistate` | Formatting, bounded concurrency, persisted TUI state | cancellation while waiting for a slot, corrupt or missing state files, untrustworthy saved SSH destinations |
| `internal/metareport` | Saved metadata reports | unreadable sources produce undetermined membership; failed collections cannot imply removal |
| `internal/network` | Cross-vCenter network comparison | VLAN gaps, MTU mismatches, a blind distributed switch, ambiguous cluster names |
| `internal/perf` | Performance summaries and sizing classification | missing samples that are not zero, too few samples, periodic peaks vs sustained low use, contention |
| `internal/query` | `--where` predicates | numeric comparisons, type errors, named tag/custom fields |
| `internal/report` | XLSX/CSV, sharing, licenses, `vFileInfo`, `vSource` | deterministic export, fail-closed pseudonymization, key redaction, old evidence, row limits |
| `internal/rvimport` | RVTools import | missing/extra fields, duplicate identities, size limits, malformed/repeated imports, no mutation client |
| `internal/search` | Cross-vCenter search matching | case-insensitive substring matching, kind restriction |
| `internal/session` | Operation deadlines and streaming idle timeouts | long work that keeps streaming, silent streams, timeout errors naming their stage |
| `internal/sizing` | Destination sizing (N-1, growth, storage) | partial coverage that is never *fit*, shared RDMs counted once, missing assumptions reported as unknown |
| `internal/sshalias` | OpenSSH alias discovery | nested/cyclic includes, depth/file/byte/candidate limits, stale aliases |
| `internal/testbed` | The loopback testbed lab itself | authenticated routes start, and wrong fixture credentials are rejected |
| `internal/topology` | Topology and blast-radius queries | shared datastores across contexts, same-named objects that must stay distinct, blind contexts that downgrade results |
| `internal/transport` | Direct, SOCKS5, and HTTP(S) proxy dialers | offline proxies, ambient proxy variables that must be ignored |
| `internal/tui` | Browse/detail, dashboards, history, datastore browser, prompts, context forms | stale replies, narrow renders, failed reloads, refresh tiers, demo restrictions, SSH precedence, unavailable keyring, missing credential diagnosis/edit |
| `internal/vsphere` | Read-only simulator inventory | paging, failed/denied collections, provenance, perf gaps, key-free license metadata |
| `cmd/vsfleet-demo` | The demo binary | seeded assessment history is wired in |

## In-process simulator tests

The untagged `tests/` suite drives the CLI against an in-process
`govmomi/simulator` in the `build` job.

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

Tagged `integration`. Each loopback endpoint runs in its own
`cmd/vsfleet-vcsim` process and can be killed mid-test. Fixtures are listed
[below](#fixture-catalogue).

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

### Fixture catalogue

Deterministic topology flags create endpoints with independent vCenter and VM
identities.

| Fixture | Topology |
| --- | --- |
| `basic-multivcenter` | `vc-prod`: two datacenters, one cluster each, two hosts per cluster, three VMs per resource pool, three datastores, three port groups, one vApp per cluster. `vc-edge`: one datacenter/cluster, two hosts, one VM pool, datastore, port group, and vApp. |
| `duplicate-names` | Identical one-datacenter estates with colliding VM and datastore names. |
| `partial-failure` | Healthy and killable endpoints plus a closed-port context. |
| `topology` | One datacenter/cluster, two hosts, VMs, datastores, and port groups, one vApp. |
| `history` | Small basic estate, captured three times with power and name changes. |

## TUI scenarios

The harness sends keys and resizes to the TUI model and checks its view and
semantic `Observation`. Golden scenarios compare renders at `60x20`,
`100x30`, and `140x40` with
`internal/testbed/scenarios/testdata/golden/`. See [render contracts](testbed.md#render-contracts)
before updating goldens.

| Scenario | Profile | Drives | Asserts | Goldens |
| --- | --- | --- | --- | --- |
| `overview` | presentation | load all contexts with `R` | inventory renders while the failed site stays visible | yes |
| `partial-failure` | presentation | load all contexts | the failed `dr-site` context is still shown | no |
| `duplicate-names` | presentation | load all contexts | same-named resources render as context-qualified rows | no |
| `credential-cancel` | presentation | open the labelled prompt, then Esc | prompt wraps; cancellation closes it and reports the result without fixture secrets | yes |
| `stale-result` | presentation | initial load | context selected only; reordered replies are covered by fuzz/unit tests | no |
| `history-coverage-gap` | presentation | `H` | History opens; goldens record the gap | yes |
| `add-context-no-secret` | connected | start with in-memory keyring refs | production backend selects a context; password-free saves are checked by `TestContextopsSave*` and `TestFormPreservesANonInteractiveCredential`, not this scenario | no |
| `credential-source-missing` | connected | missing fixture env source, then diagnosis | failure names `VSFLEET_TESTBED_PASSWORD`; diagnosis says set/restart or edit with `e`; provider never reads real environment | yes |
| `datastore-browser` | presentation | open `nvme-01` browser | root `/`, datastore name, entries, and browser mode at each size | yes |
| `resize` | presentation | resize through `60x20`, `100x30`, `140x40` | inventory still renders and selection is kept | no |
| `vm-dashboard` | presentation | pages `0`–`4`, ranges `1h`→`30d`→`1h` at each size | bounded frames, whole page tabs, detail mode retained | yes |
| `vm-timeline` | presentation | `wiki-05` timeline, tabs `1`–`3` at each size | bounded frames, tab marks, live events labelled with their vCenter, stored tab never labelled live | yes |
| `network-switches` | presentation | both DVS-Production pages; DVS-Storage Wiring/vMotion | bounded frames, page tabs, switch and port-group names | yes |
| `host-network` | presentation | esxi-db-08 Network; move switch cursor; open DVS-Storage and return | bounded frames/tabs, missing switch and unattached NICs shown, Esc returns to host | yes |
| `vlan-map` | presentation | all-context VLAN map/where panel; pair compute-a on prod-vc and edge-vc | bounded frames, both context columns, lost-connectivity VLANs named | yes |
| `cluster-workspace` | presentation | compute-a Summary, Hosts & VMs (unfold host), Storage; compute-b | bounded frames/tabs, missing datastore named, low HA reserve flagged | yes |

## PTY journeys

Tagged `linux && pty`. Journeys run `cmd/vsfleet-testbed` in a pseudo-terminal
with isolated home/XDG directories and loopback simulators. Assertions use
normalized output; see [PTY execution and artifacts](testing.md#linux-pty-process-tests).

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

See [Fuzzing](testing.md#fuzzing) for seeds, campaigns, and regression inputs.

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

## Kubernetes end-to-end

The kind suite uses `deploy/kubernetes/cronjob.yaml` with the CI image and two
vcsim Deployments, `prod` and `edge`.

| Step | Situation | Asserts |
| --- | --- | --- |
| security contract | the CronJob as shipped | non-root, read-only root filesystem, no privilege escalation, all capabilities dropped, `fsGroup` 65532 |
| `vsfleet-complete` | both vCenters up | assessment status `complete`, 2 of 2 contexts |
| `vsfleet-history` | `assessment list` on the persistent volume | the earlier run was persisted |
| `vsfleet-partial` | `edge` scaled to zero, `--fail-on-partial` | the job fails with exit code 3 and the result is `partial`, 1 of 2 contexts |
| `vsfleet-readiness` | `assessment readiness latest` | the verdict is not `ready` and unresolved items are listed |

This tier checks ConfigMap mounts, Secret-backed `file:` credentials, PVC
ownership, and Service DNS.

## Release snapshot and pins

`release-snapshot` runs `goreleaser release --snapshot --skip=publish,sign`
with the release Go version and configuration. It strips the multi-platform
`index,` annotation prefix because buildx rejects it for loaded per-platform
images. It checks six OS/architecture archives, Linux binary `--version`,
and amd64 image `--version`/`--help`.

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

See [validation limits](testing.md#what-vcsim-does-not-prove) and
[real-vCenter acceptance](testing.md#real-vcenter-validation).

## Keeping this page current

Add a row with each new scenario, journey, fuzz target, vcsim test, or CI job.
`TestCatalogueDocumentsEveryScenarioAndFuzzTarget` in
`internal/testbed/scenarios` enforces scenario/fuzz rows and fuzz workflow entries.
