# vsfleet documentation

<span id="vsphere-estate-assessment-and-diagnostics-across-every-vcenter"></span>

vsfleet inspects VMware vSphere inventory across named vCenter contexts.
Use the terminal UI to browse live data, or the CLI to diagnose connections,
capture assessments, and query stored history on Linux, macOS, or Windows.

vsfleet never powers VMs on or off, changes networks, provisions resources,
manages snapshots, or deletes inventory objects. Estate-wide operations keep
healthy results when another vCenter fails.

![vsfleet demo command and startup animation, a connection form preview, VM details and timeline, network wiring and policies, vApp members, and estate-wide search](assets/vsfleet.gif){ width="1200" }

<span id="try-it-without-a-vcenter"></span>

## Start here

```sh
vsfleet demo
```

The demo uses synthetic inventory and five in-memory history captures. It
needs no vCenter, configuration, or credentials, makes no network connections,
and writes no state. Screens are marked `DEMO · SAMPLE DATA`.

| Task | Guide |
| --- | --- |
| Install and connect your first vCenter | [Getting started](getting-started.md) |
| Browse live inventory and diagnose failures | [Terminal UI](tui.md), [Troubleshooting](troubleshooting.md) |
| Configure credentials, routes, and TLS | [Configuration](configuration.md) |
| Find commands, filters, output formats, and exit codes | [CLI reference](commands.md) |
| Schedule captures in CI, systemd, Docker, or Kubernetes | [Recipes](recipes.md), [Containers](containers.md) |

<span id="export-the-estate-for-migration-planning-and-sizing"></span>

## Work with assessments

```sh
vsfleet assessment run --all-contexts --label q3-audit
vsfleet assessment export --format rvtools --file estate.xlsx
```

Captures are stored locally in SQLite. Queries and exports read captured
evidence without contacting vCenter; missing collection coverage remains
visible in the output.

| Task | Guide |
| --- | --- |
| Capture, compare, retain, and recover inventory history | [Assessments](assessments.md) |
| Review health findings and orphan-disk evidence | [Health](health.md) |
| Collect and interpret CPU and memory history | [Performance](performance.md) |
| Export XLSX/CSV or check RVTools file interoperability | [Exports](exports.md) |
| Review migration readiness and destination sizing | [Planning](planning.md) |
| Report on tags and custom attributes | [Metadata](metadata.md) |
| Share a scoped or pseudonymized workbook | [Sharing](sharing.md) |
| Capture license metadata without license keys | [Licensing](licensing.md) |

<span id="why-vsfleet"></span>
<span id="feature-comparison"></span>

## Contribute

Read [Architecture](architecture.md) for data flow and design boundaries,
[Testing](testing.md) for verification, and [Synthetic testbed](testbed.md)
for offline UI development. [Writing documentation](writing.md) explains where
to put new material and how to keep it concise.

<span id="community-and-project-links"></span>

## Project links

[Releases](https://github.com/Easonliuuuuu/vsfleet/releases) ·
[Source and issues](https://github.com/Easonliuuuuu/vsfleet) ·
[Contributing](https://github.com/Easonliuuuuu/vsfleet/blob/main/CONTRIBUTING.md) ·
[Security](https://github.com/Easonliuuuuu/vsfleet/blob/main/SECURITY.md)

vsfleet is a personal open-source project, not an official Dell Technologies
product, and is not sponsored, endorsed, or supported by Dell Technologies.
Export interoperability was independently implemented without RVTools source
code or non-public documentation. RVTools is a Dell Technologies product;
references here describe export-file interoperability only.
