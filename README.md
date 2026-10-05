# vsfleet

<p align="center">
  <a href="https://github.com/Easonliuuuuu/vsfleet/actions/workflows/ci.yml"><img src="https://github.com/Easonliuuuuu/vsfleet/actions/workflows/ci.yml/badge.svg" alt="CI Status"></a>
  <a href="https://github.com/Easonliuuuuu/vsfleet/actions/workflows/docs.yml"><img src="https://github.com/Easonliuuuuu/vsfleet/actions/workflows/docs.yml/badge.svg" alt="Documentation"></a>
  <a href="https://github.com/Easonliuuuuu/vsfleet/releases"><img src="https://img.shields.io/github/v/release/Easonliuuuuu/vsfleet?include_prereleases&color=blue" alt="Latest Release"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/Easonliuuuuu/vsfleet" alt="Go Version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-yellow.svg" alt="License: MIT"></a>
</p>

<p align="center">
  <strong>Read-only vSphere estate assessment and diagnostics across every vCenter.</strong><br>
  <sub>Inspect, assess migration readiness, and track what changed &mdash; from Linux, macOS, or Windows, with no GUI or .NET runtime.</sub>
</p>

<p align="center">
  <picture>
    <source media="(prefers-reduced-motion: reduce)" srcset="docs/assets/vsfleet.png">
    <img src="docs/assets/vsfleet.gif" alt="vsfleet ranking assessment changes by migration impact across a run axis, reviewing stored runs and the read-only health verdict, then inspecting VM and vApp inventory across three vCenters, widening a filter into an estate-wide search, and diagnosing an unavailable site" width="1200">
  </picture>
</p>

<p align="center"><sub>Healthy inventory stays usable even when another vCenter is offline.</sub></p>

<p align="center">
  <a href="#why-vsfleet">Why vsfleet?</a> &bull;
  <a href="#installation">Installation</a> &bull;
  <a href="#quick-start">Quick Start</a> &bull;
  <a href="#core-capabilities">Core Capabilities</a> &bull;
  <a href="#security--safety-guarantees">Safety Guarantees</a> &bull;
  <a href="#documentation">Documentation</a>
</p>

---

## Why vsfleet?

Managing multiple VMware vCenters traditionally requires juggling browser tabs, coping with slow web interfaces, or maintaining brittle scripts that fail completely when a single endpoint is unreachable.

**vsfleet** organizes each vCenter into a named **context**—similar to a `kubectl` context—keeping endpoints, credentials, network routes, and certificate policies strictly separated:

- 🌐 **Estate-Wide Multi-vCenter Queries**: Query every configured vCenter in parallel with a single command using `--all-contexts`.
- 🛡️ **Partial Failure Resilience**: Unreachable or timing-out sites do not block results; healthy vCenters remain responsive and usable.
- 🔒 **Strict Read-Only Safety**: Guaranteed zero mutation. Never powers VMs on or off, reverts snapshots, modifies networks, or alters inventory.
- 🔀 **Independent Proxy Routing**: Route each context independently through direct TCP, SOCKS5, HTTP, or HTTPS CONNECT proxies.
- 🔑 **Secure Credential Handling**: Zero plaintext passwords in `config.toml`. Resolves credentials dynamically via native OS keyrings, interactive prompts, or unattended sources.
- 📊 **Historical Drift & RVTools-Compatible Exports**: Capture immutable local SQLite snapshots, track drift over time, and export 25-sheet Excel workbooks for migration sizing. An opt-in, key-free `vLicense` sheet is available for licensing reviews ([details](docs/licensing.md)).
- 🌐 **Distributed-Network Readiness**: Compare cross-cluster VLAN mappings, policy, MTU, host coverage, and affected VMs before migration.
- 🖥️ **Interactive TUI + Scriptable JSON**: Fast Bubble Tea terminal UI with local workstation handoffs (SSH, web browser, clipboard) alongside stable JSON for automation.

### Feature comparison

| Capability | `vsfleet` | `govc` | PowerCLI | vSphere Web Client |
|---|:---:|:---:|:---:|:---:|
| Multi-vCenter query in one command | **Yes** | No | Yes | No |
| Estate-wide resource search, every kind at once | **Yes** | No | Per cmdlet | Per vCenter |
| Partial results when one site fails | **Yes** | No | Custom error handling | Browser timeout |
| Per-context proxy routing | **Yes** | Global env | Global env | Browser proxy |
| Read-only safety guarantee | **Yes** | No | No | No |
| Historical drift and snapshot age | **Yes** | Export only | Custom script | Point-in-time |

> PowerCLI connects to several vCenters at once and fans most cmdlets out across them; what it does not give you is one query spanning every resource kind, or partial results by default when a site is unreachable. `govc` and PowerCLI are both full read-write toolkits — that is a capability vsfleet deliberately does not have, not a gap in theirs.

---

## Installation

### Homebrew (macOS and Linux)

```sh
brew install easonliuuuuu/tap/vsfleet
```

### Windows (WinGet & Scoop)

```powershell
# WinGet (Windows 10/11)
winget install vsfleet

# Scoop
scoop bucket add easonliuuuuu https://github.com/Easonliuuuuu/homebrew-tap
scoop install vsfleet
```

### Linux Packages (Debian, Ubuntu, RHEL, Fedora)

Download native `.deb` or `.rpm` packages from [GitHub Releases](https://github.com/Easonliuuuuu/vsfleet/releases):

```sh
# Debian / Ubuntu
sudo dpkg -i vsfleet_*_linux_amd64.deb

# RHEL / CentOS / Fedora
sudo rpm -i vsfleet_*_linux_amd64.rpm
```

### Pre-built binary (Linux, macOS, Windows)

Download an archive for your operating system and CPU architecture from [GitHub Releases](https://github.com/Easonliuuuuu/vsfleet/releases):

```sh
# Example for Linux x86_64
curl -sSL https://github.com/Easonliuuuuu/vsfleet/releases/latest/download/vsfleet_linux_amd64.tar.gz | tar -xz vsfleet
sudo install -m 0755 vsfleet /usr/local/bin/
```

Archives are available for Linux, macOS (Apple Silicon and Intel), and Windows (amd64 and arm64).

### Go install

Requires Go 1.25 or newer:

```sh
go install github.com/easonliuuuuu/vsfleet/cmd/vsfleet@latest
```

### Container (Automation & CI)

The official image is published on GitHub Container Registry for Linux `amd64` and `arm64`:

```sh
docker run --rm ghcr.io/easonliuuuuu/vsfleet:latest compatibility report --sheet vInfo -o json
```

*Note: The container runs as an unprivileged user and is designed for unattended commands, assessments, and exports. For the interactive terminal UI, install the native binary. See the [Containers guide](https://easonliuuuuu.github.io/vsfleet/containers/) for mounted configuration, secrets, and private CAs.*

---

## Quick Start

### 1. Test drive without a vCenter

Explore the full terminal UI immediately with zero configuration and zero credentials:

```sh
vsfleet demo
```

`vsfleet demo` opens the interface on a synthetic three-vCenter estate: two healthy sites on different routes and one whose proxy refuses the connection. The main site is sized like a production vCenter — about 1,000 VMs across six clusters, 36 datastores, 24 vApps (some nested), resource pools and 30 networks — and the History pane holds five dated assessments, so drift, capacity and snapshot ageing have something to show. It reads no configuration, opens no keyring, dials nothing, and writes nothing back. Every screen is marked `DEMO · SAMPLE DATA`.

### 2. Connect your first vCenter

Running `vsfleet` with no contexts configured opens the interactive setup wizard:

```sh
# Launch the setup wizard
vsfleet context add

# Test connectivity, routes, and authentication
vsfleet context test prod
```

### 3. Search and inspect inventory

Launch the interactive terminal UI or run direct CLI queries:

```sh
# Open the interactive Bubble Tea terminal UI
vsfleet

# List specific resources
vsfleet vm list
vsfleet host list --context prod

# Search across every configured vCenter at once
vsfleet search ubuntu --all-contexts
```

### 4. Capture and export an assessment

Capture an immutable local SQLite assessment and export an Excel workbook for migration planning or audit:

```sh
# Capture state across all contexts
vsfleet assessment run --all-contexts --label q3-audit

# Export RVTools-compatible workbook
vsfleet assessment export --format rvtools --file estate.xlsx
```

---

## Core Capabilities

### Interactive Terminal UI (TUI)

Run `vsfleet` to open an interactive dashboard built with Bubble Tea:
- **Fast Fuzzy Filtering**: Press `/` to filter the active view or `Tab` to widen to an estate-wide search.
- **Estate Diagnostics**: Press `d` to run immediate diagnostics on any selected vCenter.
- **Local Workstation Handoffs**: Detail-pane actions (SSH to a VM or host, open a resource in your workstation browser, copy an identifier) launch real local processes without issuing modifying vSphere API calls — see [the TUI guide](docs/tui.md#detail-pane-actions).

### Estate-Wide Search & Inventory

Supported resource kinds include `vm`, `template`, `host`, `cluster`, `vapp`, `datastore`, and `network`. All commands accept `--filter` / `-f` and work across single contexts or the entire estate:

```sh
vsfleet host list --context prod --filter esxi-07
vsfleet datastore list --all-contexts -f nvme
vsfleet search nvme --kind datastore --limit 20
```

Use `-o json` for automation with `jq` or custom tooling:

```sh
vsfleet vm list --all-contexts -o json | jq
```

### Historical Audits & Drift Tracking

Assessments are stored locally in an immutable SQLite database, giving your estate an auditable history rather than a series of disconnected spreadsheets:

```sh
vsfleet assessment diff q3-audit latest   # What changed between two captures
vsfleet assessment trends capacity        # Compute and storage trends over time
vsfleet assessment capacity latest        # Attribute datastore growth and project free space
vsfleet assessment trends churn           # VMs created and destroyed
vsfleet assessment snapshots              # Snapshot ages, oldest first
vsfleet health latest                     # Health findings on stored evidence
vsfleet blast-radius datastore ds-prod-01 # What depends on a datastore
```

### Migration & Sizing Exports (RVTools Interoperability)

vsfleet exports stored assessments for migration planning, sizing, audit, and downstream workflows. The XLSX export uses the `rvtools` format name for interoperability with tools that consume selected worksheet layouts:

```sh
vsfleet assessment export --format rvtools --file estate.xlsx
vsfleet assessment export --format csv --file ./audit-csv/
```

- **Byte-Identical Consistency**: Exporting the same stored assessment twice produces byte-identical files accompanied by a SHA256 receipt.
- **Audit Coverage Sheet**: Every export includes a `vsfleetCoverage` sheet detailing what collected, what failed, and why per vCenter. A partial estate is reported as partial rather than handed over as whole.
- **Schema Transparency**: Inspect every column, type, unit, and nullability without needing a vCenter or configuration:
  ```sh
  vsfleet compatibility report --sheet vPartition
  ```

<details>
<summary><strong>Supported Worksheet Layouts (25 sheets)</strong></summary>

vsfleet renders the following 25 worksheet layouts:

`vInfo` &bull; `vCPU` &bull; `vMemory` &bull; `vDisk` &bull; `vPartition` &bull; `vNetwork` &bull; `vCD` &bull; `vUSB` &bull; `vSnapshot` &bull; `vTools` &bull; `vSource` &bull; `vRP` &bull; `vCluster` &bull; `vHost` &bull; `vHBA` &bull; `vNIC` &bull; `vSwitch` &bull; `vPort` &bull; `dvSwitch` &bull; `dvPort` &bull; `vSC_VMK` &bull; `vDatastore` &bull; `vMultiPath` &bull; `vFileInfo` (opt-in file inventory) &bull; `vHealth`

- **Source Provenance (`vSource`)**: Records each context's vCenter or ESXi `ServiceInstance` About identity (product, version, patch level, build, API version) when it is captured, so a workbook from several vCenters says what produced each row. Runs captured before this evidence existed, and contexts that never connected, have no `vSource` row and `vsfleetCoverage` says why.
- **Guest Filesystem Usage (`vPartition`)**: Measures guest usage via VMware Tools. VMs with no running Tools contribute no rows, and `vsfleetCoverage` reports answering status.
- **Host Configuration Sheets**: HBAs, multipath LUN aggregates, physical NICs, standard virtual switches, standard port groups, and VMkernel adapters are pulled directly from host properties without querying separate manager endpoints.

*Compatibility is limited to the listed worksheet names and columns. This is an interoperability export, not RVTools and not a replacement for it.*

</details>

> [!NOTE]
> vsfleet is a personal open-source project, not an official Dell Technologies product, and is not sponsored, endorsed, or supported by Dell Technologies. Its export interoperability was independently implemented without RVTools source code or non-public documentation. RVTools is a Dell Technologies product; references to RVTools describe export-file interoperability only.

---

## Security & Safety Guarantees

> [!IMPORTANT]
> **Strict read-only safety guarantee:** vsfleet is an inspection and diagnostic tool. It does not power on/off VMs, create or revert snapshots, modify networks, provision resources, or delete inventory objects. That guarantee is about the vSphere API specifically: the TUI's detail-pane handoff actions (SSH to a VM or host, open a resource in the vSphere/Host Client, copy an identifier) launch real processes on your own workstation, never a vSphere API call — see [the TUI guide](docs/tui.md#detail-pane-actions).

- **Zero Plaintext Passwords on Disk**: Passwords are never written to `config.toml`. Credentials resolve dynamically via the native OS keyring (Secret Service, macOS Keychain, Windows Credential Manager), an interactive prompt, or unattended sources (`env:<VAR>`, `file:<path>`, `exec:<program>`).
- **Per-Context Proxy Isolation**: Route each vCenter independently via direct TCP, SOCKS5, HTTP, or HTTPS CONNECT proxies.
- **TLS Thumbprint Pinning**: Explicitly pin SHA256 or SHA1 thumbprints for private or self-signed certificates.

---

## Documentation

The full operator guide is published at **[easonliuuuuu.github.io/vsfleet](https://easonliuuuuu.github.io/vsfleet/)**:

| Guide | Description |
|---|---|
| 🚀 **[Getting Started](https://easonliuuuuu.github.io/vsfleet/getting-started/)** | First-time configuration, prerequisites, and shell completion |
| 💻 **[CLI Guide](https://easonliuuuuu.github.io/vsfleet/commands/)** | Exhaustive reference for commands, flags, filters, and JSON output |
| 🖥️ **[Terminal UI](https://easonliuuuuu.github.io/vsfleet/tui/)** | Keybindings, navigation, filtering, and workstation actions |
| 📦 **[Containers](https://easonliuuuuu.github.io/vsfleet/containers/)** | Docker and Kubernetes deployment, volume mounts, and CI automation |
| 📊 **[Assessments & History](https://easonliuuuuu.github.io/vsfleet/assessments/)** | Capturing state, diffing runs, trends, and export formats |
| ⚙️ **[Configuration](https://easonliuuuuu.github.io/vsfleet/configuration/)** | TOML configuration, proxy settings, TLS thumbprints, and credential sources |
| 📖 **[Operator Recipes](https://easonliuuuuu.github.io/vsfleet/recipes/)** | Real-world workflows, audit recipes, and pipeline integrations |
| 🔧 **[Troubleshooting](https://easonliuuuuu.github.io/vsfleet/troubleshooting/)** | Connectivity diagnostics (`vsfleet doctor`), common issues, and fixes |
| 🏛️ **[Architecture](https://easonliuuuuu.github.io/vsfleet/architecture/)** | Concurrency engine, session caching, and security invariants |
| 🧪 **[Synthetic Testbed](https://easonliuuuuu.github.io/vsfleet/testbed/)** | Deterministic sandbox, scenarios, render contracts, PTY journeys, and fuzzing |

---

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md) for development, testing, and synthetic testbed instructions. See [SECURITY.md](SECURITY.md) for credential handling, read-only guarantees, and vulnerability reporting.

---

## License

[MIT](LICENSE) © Eason Liu
