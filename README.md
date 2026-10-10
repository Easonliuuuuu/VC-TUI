# vsfleet

<p align="center">
  <a href="https://github.com/Easonliuuuuu/vsfleet/actions/workflows/ci.yml"><img src="https://github.com/Easonliuuuuu/vsfleet/actions/workflows/ci.yml/badge.svg" alt="CI Status"></a>
  <a href="https://github.com/Easonliuuuuu/vsfleet/actions/workflows/docs.yml"><img src="https://github.com/Easonliuuuuu/vsfleet/actions/workflows/docs.yml/badge.svg" alt="Documentation"></a>
  <a href="https://github.com/Easonliuuuuu/vsfleet/releases"><img src="https://img.shields.io/github/v/release/Easonliuuuuu/vsfleet?include_prereleases&color=blue" alt="Latest Release"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/Easonliuuuuu/vsfleet" alt="Go Version"></a>
  <a href="docs/testing.md#real-vcenter-validation"><img src="https://img.shields.io/badge/vCenter-8.0.3%20lab--verified-1d6fa5" alt="Verified against vCenter 8.0.3"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-yellow.svg" alt="License: MIT"></a>
</p>

<p align="center">
  <strong>Read-only vSphere estate assessment and diagnostics across every vCenter.</strong><br>
  <sub>Inspect, assess migration readiness, and track what changed &mdash; from Linux, macOS, or Windows, with no GUI or .NET runtime.</sub>
</p>

<p align="center">
  <picture>
    <source media="(prefers-reduced-motion: reduce)" srcset="docs/assets/vsfleet.png">
    <img src="docs/assets/vsfleet.gif" alt="vsfleet browsing a synthetic three-vCenter estate, opening VM performance charts and distributed-switch wiring, widening a VM filter into an estate-wide search, and diagnosing an unavailable DR site" width="1200">
  </picture>
</p>

<p align="center"><sub>Healthy inventory stays usable even when another vCenter is offline.</sub></p>

<p align="center">
  <a href="#why-vsfleet">Why vsfleet?</a> &bull;
  <a href="#installation">Installation</a> &bull;
  <a href="#quick-start">Quick Start</a> &bull;
  <a href="#documentation">Documentation</a>
</p>

---

## Why vsfleet?

Managing multiple VMware vCenters traditionally requires juggling browser tabs, coping with slow web interfaces, or maintaining brittle scripts that fail completely when a single endpoint is unreachable.

**vsfleet** organizes each vCenter into a named **context**—similar to a `kubectl` context—keeping endpoints, credentials, network routes, and certificate policies strictly separated:

- <img src="docs/assets/icons/globe.svg" width="16" height="16" alt=""> **Estate-Wide Multi-vCenter Queries**: Query every configured vCenter in parallel with a single command using `--all-contexts`.
- <img src="docs/assets/icons/shield-check.svg" width="16" height="16" alt=""> **Partial Failure Resilience**: Unreachable or timing-out sites do not block results; healthy vCenters remain responsive and usable.
- <img src="docs/assets/icons/lock.svg" width="16" height="16" alt=""> **Strict Read-Only Safety**: Never powers VMs on or off, reverts snapshots, modifies networks, or alters inventory. See [SECURITY.md](SECURITY.md).
- <img src="docs/assets/icons/arrow-switch.svg" width="16" height="16" alt=""> **Independent Proxy Routing**: Route each context independently through direct TCP, SOCKS5, HTTP, or HTTPS CONNECT proxies, with optional TLS thumbprint pinning.
- <img src="docs/assets/icons/key.svg" width="16" height="16" alt=""> **Secure Credential Handling**: Zero plaintext passwords in `config.toml`. Resolves credentials via native OS keyrings, interactive prompts, or unattended sources.
- <img src="docs/assets/icons/graph.svg" width="16" height="16" alt=""> **Historical Drift & RVTools-Compatible Exports**: Capture immutable local SQLite snapshots, track drift over time, and export 25-sheet Excel workbooks for migration sizing ([details](docs/assessments.md#deterministic-exports)).
- <img src="docs/assets/icons/workflow.svg" width="16" height="16" alt=""> **Distributed-Network Readiness**: Compare cross-cluster VLAN mappings, policy, MTU, host coverage, and affected VMs before migration.
- <img src="docs/assets/icons/terminal.svg" width="16" height="16" alt=""> **Interactive TUI + Scriptable JSON**: Fast Bubble Tea terminal UI with local workstation handoffs (SSH, web browser, clipboard) alongside stable JSON for automation.

### Feature comparison

| Capability | `vsfleet` | `govc` | PowerCLI | vSphere Web Client |
|---|:---:|:---:|:---:|:---:|
| Multi-vCenter query in one command | **Yes** | No | Yes | No |
| Estate-wide resource search, every kind at once | **Yes** | No | Per cmdlet | Per vCenter |
| Partial results when one site fails | **Yes** | No | Custom error handling | Browser timeout |
| Per-context proxy routing | **Yes** | Global env | Global env | Browser proxy |
| Read-only safety guarantee | **Yes** | No | No | No |
| Historical drift and snapshot age | **Yes** | Export only | Custom script | Point-in-time |

<sub>`govc` and PowerCLI are full read-write toolkits; vsfleet deliberately is not.</sub>

---

## Installation

```sh
# Homebrew (macOS and Linux)
brew install easonliuuuuu/tap/vsfleet

# WinGet (Windows 10/11)
winget install vsfleet

# Container (unattended commands and CI; linux/amd64 and arm64)
docker run --rm ghcr.io/easonliuuuuu/vsfleet:latest compatibility report --sheet vInfo -o json
```

Scoop, `.deb` and `.rpm` packages, release archives, and `go install` are covered in [Getting Started](https://easonliuuuuu.github.io/vsfleet/getting-started/). The interactive TUI needs the native binary; see the [Containers guide](https://easonliuuuuu.github.io/vsfleet/containers/) for running the image.

---

## Quick Start

### 1. Test drive without a vCenter

```sh
vsfleet demo
```

Opens the TUI on a synthetic three-vCenter estate, with no configuration, credentials, or network access.

### 2. Connect your first vCenter

Running `vsfleet` with no contexts configured opens the interactive setup wizard:

```sh
# Launch the setup wizard
vsfleet context add

# Test connectivity, routes, and authentication
vsfleet context test prod
```

### 3. Search and inspect inventory

```sh
# Open the interactive terminal UI
vsfleet

# List specific resources
vsfleet vm list
vsfleet host list --context prod

# Search across every configured vCenter at once
vsfleet search ubuntu --all-contexts
```

### 4. Capture, compare, and export

```sh
vsfleet assessment run --all-contexts --label q3-audit   # Capture state across all contexts
vsfleet assessment diff q3-audit latest                  # What changed between two captures
vsfleet assessment trends capacity                       # Compute and storage trends over time
vsfleet assessment snapshots                             # Snapshot ages, oldest first
vsfleet health latest                                    # Health findings on stored evidence
vsfleet blast-radius datastore ds-prod-01                # What depends on a datastore
vsfleet assessment export --format rvtools --file estate.xlsx
```

> [!NOTE]
> vsfleet is a personal project, not affiliated with or endorsed by Dell Technologies; its RVTools-compatible export is an independent interoperability implementation.

---

## Documentation

The full operator guide is published at **[easonliuuuuu.github.io/vsfleet](https://easonliuuuuu.github.io/vsfleet/)**:

| Guide | Description |
|---|---|
| <img src="docs/assets/icons/rocket.svg" width="16" height="16" alt=""> **[Getting Started](https://easonliuuuuu.github.io/vsfleet/getting-started/)** | Installation, first configuration, shell completion |
| <img src="docs/assets/icons/code.svg" width="16" height="16" alt=""> **[CLI Guide](https://easonliuuuuu.github.io/vsfleet/commands/)** | Commands, flags, filters, JSON output |
| <img src="docs/assets/icons/terminal.svg" width="16" height="16" alt=""> **[Terminal UI](https://easonliuuuuu.github.io/vsfleet/tui/)** | Keybindings, filtering, workstation actions |
| <img src="docs/assets/icons/package.svg" width="16" height="16" alt=""> **[Containers](https://easonliuuuuu.github.io/vsfleet/containers/)** | Docker, Kubernetes, CI automation |
| <img src="docs/assets/icons/graph.svg" width="16" height="16" alt=""> **[Assessments & History](https://easonliuuuuu.github.io/vsfleet/assessments/)** | Captures, diffs, trends, export formats |
| <img src="docs/assets/icons/gear.svg" width="16" height="16" alt=""> **[Configuration](https://easonliuuuuu.github.io/vsfleet/configuration/)** | Proxies, TLS thumbprints, credential sources |
| <img src="docs/assets/icons/book.svg" width="16" height="16" alt=""> **[Operator Recipes](https://easonliuuuuu.github.io/vsfleet/recipes/)** | Real-world workflows and pipelines |
| <img src="docs/assets/icons/tools.svg" width="16" height="16" alt=""> **[Troubleshooting](https://easonliuuuuu.github.io/vsfleet/troubleshooting/)** | `vsfleet doctor` and common fixes |
| <img src="docs/assets/icons/stack.svg" width="16" height="16" alt=""> **[Architecture](https://easonliuuuuu.github.io/vsfleet/architecture/)** | Concurrency, session caching, security invariants |

---

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md) for development and testing, and the [Synthetic Testbed](https://easonliuuuuu.github.io/vsfleet/testbed/) guide for scenarios and PTY journeys. See [SECURITY.md](SECURITY.md) for credential handling, read-only guarantees, and vulnerability reporting.

---

## License

[MIT](LICENSE) © Eason Liu
