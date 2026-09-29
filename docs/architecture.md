# Architecture of vsfleet

This document provides a bird's-eye view of vsfleet's internal architecture,
system topology, design invariants, source code structure, and cross-cutting
concerns. It is intended for contributors and operators who want to understand
how vsfleet operates across multiple heterogeneous VMware vCenters.

---

## 1. Bird's-Eye Overview

Managing multiple VMware vCenters is traditionally fraught with two challenges:
1. **Network heterogeneity:** Different vCenters live in different security zones (direct local networks, jumpboxes, SOCKS5 tunnels, or HTTP/HTTPS CONNECT proxies).
2. **Operational friction:** Official tools (`govc`, PowerCLI, vSphere Web Client) either target one vCenter at a time or require heavy scripting that fails completely when a single endpoint is unreachable.

vsfleet solves this by decoupling **contexts** (endpoints, credentials, transports, and TLS policies) from operations (CLI queries, estate-wide searches, and interactive terminal browsing).

```mermaid
flowchart TD
    subgraph UI ["Frontends"]
        CLI["CLI Commands (internal/cli)"]
        TUI["Bubble Tea TUI (internal/tui)"]
    end

    subgraph Core ["Orchestration & Cache"]
        CFG["Config & Context Store (internal/config)"]
        SM["Session Manager (internal/session)"]
        CACHE["Inventory Cache (internal/session)"]
        LIM["Limiter & Concurrency (internal/limiter)"]
        SEARCH["Search Engine (internal/search)"]
        ASSESS["Assessment Ledger (internal/assessment + SQLite)"]
    end

    subgraph Adapters ["Transport & Security"]
        CRED["Credential Resolver (internal/credentials)"]
        TRANS["Transport Layer (internal/transport)"]
    end

    subgraph Targets ["vCenter Endpoints"]
        VC1["vCenter Direct (Lab)"]
        VC2["vCenter via SOCKS5 (Customer A)"]
        VC3["vCenter via HTTP CONNECT (Customer B)"]
        DEMO["Synthetic Backend (internal/demo)"]
    end

    CLI --> SM
    TUI --> SM
    CLI --> CFG
    TUI --> CFG
    TUI --> SEARCH
    CLI --> SEARCH
    TUI --> ASSESS
    CLI --> ASSESS

    SM --> CACHE
    SM --> LIM
    SM --> CRED
    SM --> TRANS

    CRED -->|OS Keyring / Prompt| SM
    TRANS -->|Direct TCP| VC1
    TRANS -->|SOCKS5 + Remote DNS| VC2
    TRANS -->|HTTP/HTTPS Proxy| VC3
    TUI -.->|Presentation Mode| DEMO
```

---

## 2. Core Design Invariants

Every change to vsfleet must uphold these four architectural invariants:

### 1. Strict Read-Only Safety
vsfleet is an inspection and diagnostic tool. Under no circumstances should
write, power-state mutation, snapshot creation/reversion, provisioning, or
deletion RPCs be introduced to [`internal/vsphere`](https://github.com/Easonliuuuuu/vsfleet/tree/main/internal/vsphere). Operators must be able to run
vsfleet in production environments with absolute certainty that no state will
be altered.

### 2. Fault Isolation & Partial Success
An outage or misconfiguration in one vCenter must never cause a multi-context
operation to abort.
* In the CLI: If 3 of 4 vCenters respond and 1 times out, results from the 3
  healthy vCenters are displayed, and the failure for the 4th is reported
  separately with diagnostic context.
* In the TUI: Failed contexts are marked clearly in the status gutter, while
  cached inventory for healthy contexts remains interactive.
* In inventory listing: If one resource kind (e.g., datastores) fails to query
  on an overloaded vCenter, available resources (e.g., VMs and hosts) are still
  returned.

### 3. Zero Credential Persistence on Disk
Plaintext passwords are never written to `config.toml` or state files.
* Credentials are stored in the platform's native secret service via
  [zalando/go-keyring](https://github.com/zalando/go-keyring) (Linux Secret Service, macOS Keychain, Windows Credential Manager).
* When run on headless systems without a secret store, credentials gracefully
  fall back to interactive prompt mode (`prompt`) without failing the workflow.
* Password prompts are masked, and credentials held in memory are discarded when
  sessions are terminated.

### 4. Context Isolation & Deterministic Invalidation
A context name is a label for a configuration set, not an identity:
* Editing a context (e.g., updating its proxy, username, or TLS mode)
  immediately terminates existing client sessions and purges associated cached
  inventory.
* Removing a context sends an explicit logout to the vCenter session rather than
  leaving orphaned sessions on the server.

### 5. Historical Observations Are Immutable and Coverage-Aware
The assessment ledger is a local observation store, not a second source of
truth. Each explicit capture records the VM and snapshot state returned by
vCenter at that point in time. A diff identifies VMs by managed-object
reference, then instance UUID or BIOS UUID, and reports moves only when both
observations are unambiguous. Lifecycle claims are made only for vCenters that
completed collection in both runs; failed or missing coverage produces a
warning instead of false vanish/appear events.

Schema version 3 adds per-kind collection coverage and immutable host, cluster,
and datastore observations. Inventory schema version 11 adds datastore backing
identity and browse-truncation provenance, allowing estate-wide orphan checks
to distinguish shared storage from same-name independent datastores. Resource diffs use vCenter plus managed-object
reference identity and keep volatile utilization/state fields behind
`--include-runtime`. Trends aggregate estate totals before context/resource
drill-downs, and every JSON trend/report envelope carries a schema version.
Inventory schema version 12 persists network observations. Version 13 adds
target-neutral VM migration configuration (firmware, Secure Boot, CPU topology,
resource controls, and managed-by metadata) plus normalized vTPM, passthrough,
and floppy evidence; special-device payloads deliberately exclude sensitive
TPM certificate material. The headless
`internal/topology` graph joins those observations with VM, host, datastore,
distributed-switch, and resource-pool evidence for offline dependency queries.
Run labels, notes, pinning, and the renewable fenced lease from schema version 2
remain intact; the lease also serializes prune, backup, and restore operations.
The TUI's History hub switches between Changes, Trends, Runs, and Health, while all
destructive ledger maintenance stays explicit on the CLI.

Performance history follows the same rule from the other side. `assessment perf
collect` stores bounded summaries and provenance in ledger schema version 6
(`perf_windows`, `perf_vms`, `perf_summaries`), keyed by context and vCenter
identity and never by inventory run. Immutable inventory observations are not
edited or referenced, and pruning removes performance windows by age while
sparing each context's newest usable window. The pure semantics (counters, unit
normalisation, sample gating, sizing signal) live in `internal/perf` with no
vSphere imports; the only new wire operation is `QueryPerf`, sent through a
hand-rolled SOAP body in `internal/vsphere/perf_query.go` because the govmomi
`performance` package imports mutation-capable packages.

The `assessment export` command reads a selected finished run through one
SQLite read transaction and passes it to the `internal/report` writer. It never
loads configuration or credentials and never creates a session. Inventory
schema version 3 (distinct from the ledger's own schema version above) adds
the VMware Tools version and version status to VM payloads, backing the
`vTools` tab; version 6 adds normalized CD-ROM and USB device observations,
backing the `vCD` and `vUSB` export tabs; version 8 adds persisted
resource-pool observations for the
`vRP` export tab; version 17 records each context's `ServiceInstance` About record in the ledger's `context_sources` table (ledger schema 7) for the `vSource` tab, read only from the stored run at export time; runs before version 17, and contexts that never connected, have no row and are reported as not recorded or failed on `vsfleetCoverage` rather than given a guessed version; version 9 adds host storage and network sub-objects for the
`vHBA`, `vNIC`, `vSwitch`, `vPort`, `vSC_VMK` (renamed from `vSC+VMK`), and `vMultiPath` tabs. Older rows still populate the `vTools` running-status column,
with the version columns left blank and the gap noted on `vsfleetCoverage`; runs
before version 6 mark `vCD` and `vUSB` as not recorded, runs
before version 8 mark `vRP` as not recorded, and runs before version 9 mark the
six host configuration tabs as not recorded. Version 10 adds persisted
distributed virtual switch and port-group observations for `dvSwitch` and
`dvPort`; runs before version 10 mark those tabs as not recorded. Inventory
schema version 11 keeps the new datastore backing fields in the payload
without changing RVTools worksheet columns. Inventory schema version 14 adds
the `Local disk` column to `vMultiPath`; absent locality is preserved as an
unknown value for conservative health evaluation. Inventory schema version 18
adds the opt-in `license` collection (`assessment run --include-licenses`),
stored as key-free `vsphere.License` resource observations with a collection
status of `success`, `partial` or `unavailable`; a run that did not request it
has no such collection and its export has no license sheets. The vSphere layer
has no license-key field, so a key cannot reach the ledger, output or exports
(see [License metadata](licensing.md)). Inventory schema version 19 adds the
opt-in `Datastore.FileInventory` record (status, row limit, truncation
provenance and file rows) behind `vFileInfo`; it is a separate capture from the
VMDK browse evidence, and orphan and health evaluation never read it. A shared
`rvtoolsSheets` compatibility-sheet builder canonicalizes and validates the run
once and returns every supported worksheet (`vInfo`, `vCPU`, `vMemory`,
per-VM `vDisk`/`vPartition`/`vNetwork`/`vCD`/`vUSB`,
`vSnapshot`, `vTools`, `vSource`, `vRP`, `vCluster`, `vHost`, `vHBA`, `vNIC`, `vSwitch`,
`vPort`, `dvSwitch`, `dvPort`, `vSC_VMK`, `vDatastore`, `vMultiPath`, `vFileInfo`, `vHealth`,
`vsfleetCoverage`) in
tab order; the XLSX writer normalizes ZIP entry order and timestamps on top of
it, and the CSV writer renders the same tabs as one file per sheet, so both
formats are byte-identical across repeated exports of unchanged stored
evidence and agree with each other on content. `vHealth` is derived at export
time from persisted evidence; it is not a separately collected sheet.

`report.Profile` describes that same worksheet set column by column — cell
type, unit, and when a cell is left empty — and is what
`vsfleet compatibility report` renders. It is built by running the real sheet
builder over empty export data, so the description is derived from the exporter
rather than maintained alongside it, and a test fails the build if a worksheet
gains a column that nothing describes.

---

## 3. Source Code Organization

```
vsfleet/
├── cmd/
│   ├── vsfleet/           # Production CLI & TUI entrypoint
│   └── vsfleet-demo/      # Deterministic synthetic binary for the presentation testbed
├── docs/
│   ├── assets/            # README recording (.gif) and screenshot (.png)
│   └── architecture.md    # System design, invariants, and codemap (this document)
├── internal/
│   ├── assessment/        # SQLite run ledger, coverage-aware diffs, VM history
│   ├── cli/               # Cobra command handlers (root, context, doctor, vm, search, etc.)
│   ├── config/            # TOML config parser, context definitions, path discovery
│   ├── contextops/        # High-level context lifecycle (add, edit, remove, test)
│   ├── credentials/       # Keyring and interactive prompt credential providers
│   ├── demo/              # In-memory synthetic vCenter backend for offline demos
│   ├── humanize/          # Output humanization (bytes, durations, frequencies)
│   ├── limiter/           # Concurrency limiters and rate throttles
│   ├── perf/              # Offline VM performance semantics: counters, units, summaries, sizing signal
│   ├── search/            # Cross-vCenter estate search engine
│   ├── session/           # Session management, caching, and connection pooling
│   ├── transport/         # Network dialers (Direct, SOCKS5, HTTP/HTTPS CONNECT)
│   ├── topology/          # Deterministic cross-vCenter relationship graph and queries
│   ├── tui/               # Charmbracelet Bubble Tea terminal user interface
│   ├── uistate/           # Persistent UI view state (last visited tab, context)
│   ├── version/           # Build-time version metadata (injected by GoReleaser)
│   └── vsphere/           # VMware govmomi wrappers, stage diagnosis, inventory models
└── tests/                 # Integration tests with vSphere simulator and proxy tests
```

---

## 4. Subsystem Details

### Transport & Connectivity (`internal/transport`)
The transport layer provides a unified `net.Dialer` interface regardless of the
underlying route:
* **Direct**: Standard local TCP dialer with local DNS resolution.
* **SOCKS5**: Supports authenticated proxying and `--remote-dns` to resolve
  vCenter hosts within the destination network enclave.
* **HTTP / HTTPS CONNECT**: Tunnels TCP connections through standard forward
  proxies, including proxy basic authentication and TLS verification for HTTPS
  proxies.

### Diagnostic Pipeline (`internal/cli/doctor.go` & `internal/vsphere/stage.go`)
The `vsfleet doctor` command executes an 8-stage pipeline in strict sequence to
diagnose exact point-of-failure:
1. **Configuration**: Validates TOML schema and context properties.
2. **Credentials**: Confirms OS keyring accessibility or prompt availability.
3. **Route & Proxy**: Confirms proxy reachability and proxy authentication.
4. **DNS**: Resolves vCenter hostname (locally or remotely via proxy).
5. **TCP**: Establishes transport handshake to port 443.
6. **TLS**: Validates certificate chain or compares pinned SHA-256/SHA-1 thumbprint.
7. **Authentication**: Performs vSphere SSO authentication.
8. **API**: Executes basic vSphere ServiceContent probe.

### Background Polling & Cache (`internal/session`)
The TUI maintains a quiet background refresh model:
* Startup may reuse a session or stored keyring credential automatically, but
  an interactive credential boundary settles into `credentials required`
  until an explicit context selection or reload opens the masked prompt.
* Active context inventory is polled every 20 seconds.
* Inactive, successfully loaded contexts are polled every 200 seconds.
  Contexts waiting for interactive credentials require an explicit selection
  or reload; a background timer never takes over the terminal with a prompt.
* Unvisited contexts are never polled until explicitly brought into scope.
* Failures do not blank the screen: the cache retains stale inventory, visually
  noting the last-known timestamp and the connection issue.

---

## 5. Development & Testing Strategy

1. **Synthetic Presentation Backend (`cmd/vsfleet-demo`)**:
   Enables 100% offline development and styling of the Bubble Tea TUI. It generates
   realistic, deterministic inventory across three simulated vCenters with zero
   credentials or VMware dependencies.
2. **Connected Local Testbed (`cmd/vsfleet-testbed`, `internal/testbed`)**:
   Starts multiple authenticated `govmomi/simulator` SOAP endpoints and
   loopback-only SOCKS5/HTTP CONNECT routes, then wires the production session,
   transport, credential prompt, and assessment service into the TUI. Its
   config, history, and in-memory fixture credentials are isolated from the
   operator environment.
3. **In-Process VMware Simulator Tests (`tests/`)**:
   Integration tests run against `govmomi/simulator` and in-memory proxy
   listeners, allowing complete end-to-end tests in CI without external
   infrastructure.
4. **Repository-Owned TUI Scenario Harness (`scripts/testbed`)**:
   Deterministic named scenarios drive the real Bubble Tea model headlessly,
   retain semantic failure artifacts, and expose the same setup through a
   developer sandbox. Critical screens have selective ANSI-normalized goldens;
   native Go fuzz targets cover key/resize sequences, stale replies, and
   bounded rendering.
5. **Linux PTY Process Journeys (`internal/testbed/pty`)**:
   A small tagged suite launches the connected developer testbed as an actual
   terminal process. It validates input decoding, alternate-screen rendering,
   resize delivery, Ctrl-C cleanup, and exit status while retaining redacted
   failure artifacts and isolated state.
