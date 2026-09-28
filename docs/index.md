# vSphere estate assessment and diagnostics across every vCenter

vsfleet is an open-source Go CLI and terminal UI for VMware vSphere operators
and site reliability engineers. It inspects inventory, diagnoses connectivity,
assesses migration readiness, and preserves historical observations across a
whole estate in one run — from Linux, macOS or Windows, with no GUI and no .NET
runtime. It organizes each vCenter into a named context, so you can search the
estate and compare changes without juggling browser tabs or scripts.

![vsfleet inspecting inventory across three vCenters](assets/vsfleet.gif){ width="1200" }

## Try it without a vCenter

```sh
vsfleet demo
```

The demo opens the interface on a synthetic three-vCenter estate — two healthy
sites and one whose proxy refuses the connection. The main site is production-sized
(about 1,000 VMs, 36 datastores, 24 vApps) and History holds five dated
assessments. It reads no configuration,
opens no keyring, dials nothing, and writes nothing back. Every screen is
marked `DEMO · SAMPLE DATA`.

> [!NOTE]
> vsfleet is a personal open-source project, not an official Dell Technologies
> product, and is not sponsored, endorsed, or supported by Dell Technologies.
> Its export interoperability was independently implemented without RVTools
> source code or non-public documentation. RVTools is a Dell Technologies
> product; references to RVTools describe export-file interoperability only.

## Export the estate for migration planning and sizing

```sh
vsfleet assessment run --all-contexts --label q3-audit
vsfleet assessment export --format rvtools --file estate.xlsx
```

The `rvtools` format renders a documented subset of worksheet layouts used by
RVTools exports: `vInfo`, `vCPU`, `vMemory`, `vDisk`, `vPartition`, `vNetwork`,
`vCD`, `vUSB`, `vSnapshot`, `vTools`, `vRP`, `vCluster`, `vHost`, `vHBA`, `vNIC`,
`vSwitch`, `vPort`, `dvSwitch`, `dvPort`, `vSC_VMK`, `vDatastore`, `vMultiPath`,
and `vHealth`.
This is an interoperability profile, not RVTools or a replacement for it — see
[Assessments](assessments.md) for the full tab reference and the
`vsfleetCoverage` sheet.

> [!IMPORTANT]
> **Strict read-only safety guarantee:** vsfleet never powers on or off VMs,
> changes networks, provisions resources, manages snapshots, or deletes
> inventory objects.

## Why vsfleet?

- Query every configured vCenter with one command using `--all-contexts`.
- Search VMs, templates, hosts, clusters, vApps, datastores, and networks
  across the estate.
- Keep healthy results usable when another vCenter is offline or timing out.
- Route each context independently through direct TCP, SOCKS5, HTTP, or HTTPS
  CONNECT proxies.
- Keep passwords in the operating system keyring, an interactive prompt, or an
  unattended source for cron, systemd, containers and CI;
  they never go into `config.toml`.
- Pin TLS thumbprints for private or self-signed vCenters.
- Capture immutable, local SQLite assessments and explain drift over time.
- Use a responsive Bubble Tea TUI or stable JSON output for automation.

## Start here

| Goal | Guide |
|---|---|
| Install and connect the first vCenter | [Getting Started](getting-started.md) |
| Run unattended commands in Docker or Kubernetes | [Containers](containers.md) |
| Find the right command or JSON output | [CLI Guide](commands.md) |
| Capture, compare, and export inventory history | [Assessments](assessments.md) |
| Learn the interactive terminal UI | [Terminal UI](tui.md) |
| Configure credentials, routes, and TLS | [Configuration](configuration.md) |
| Diagnose a failed connection | [Troubleshooting](troubleshooting.md) |
| Understand the system design | [Architecture](architecture.md) |
| Develop and verify the TUI without a vCenter | [Synthetic Testbed](testbed.md) |

## Feature comparison

| Capability | `vsfleet` | `govc` | PowerCLI | vSphere Web Client |
|---|:---:|:---:|:---:|:---:|
| Multi-vCenter query in one command | **Yes** | No | Yes | No |
| Estate-wide resource search, every kind at once | **Yes** | No | Per cmdlet | Per vCenter |
| Partial results when one site fails | **Yes** | No | Custom error handling | Browser timeout |
| Per-context proxy routing | **Yes** | Global env | Global env | Browser proxy |
| Read-only safety guarantee | **Yes** | No | No | No |
| Historical drift and snapshot age | **Yes** | Export only | Custom script | Point-in-time |

## Community and project links

- [Releases](https://github.com/Easonliuuuuu/vsfleet/releases)
- [Contributing guide](https://github.com/Easonliuuuuu/vsfleet/blob/main/CONTRIBUTING.md)
- [Security policy](https://github.com/Easonliuuuuu/vsfleet/blob/main/SECURITY.md)
- [Source code and issues](https://github.com/Easonliuuuuu/vsfleet)
