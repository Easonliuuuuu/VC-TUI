# VM performance history

Collect VM CPU and memory history for sizing work. Collection is opt-in and
stored separately from inventory captures, which hold configured sizes.
Performance collection never rewrites or references a capture.

```sh
vsfleet assessment perf collect --window 7d            # contacts vCenter, read-only
vsfleet assessment perf list
vsfleet assessment perf show latest                    # offline
vsfleet assessment report                              # lists collected/not collected per context
vsfleet assessment export --format rvtools --file e.xlsx   # adds the vsfleetPerformance sheet
```

`perf collect` uses vCenter's `PerformanceManager.QueryPerf`, with counters
and roll-ups from `perfCounter` and `historicalInterval`. It selects the finest
historical interval retaining the whole `--window`; `--interval` overrides it.

| Metric | vSphere counter | Level | Stored unit | What one sample is |
| --- | --- | --- | --- | --- |
| `cpu.usage` | `cpu.usage.average` | 1 | percent | interval average of VM CPU usage |
| `cpu.ready` | `cpu.ready.summation` | 1 | percent | interval sum of ready milliseconds, divided by interval length and vCPU count: the average share of the interval one vCPU waited to be scheduled |
| `mem.active` | `mem.active.average` | 2 | MiB | interval average of recently touched guest memory |
| `mem.consumed` | `mem.consumed.average` | 1 | MiB | interval average of host memory backing the VM |
| `mem.balloon` | `mem.vmmemctl.average` | 1 | MiB | interval average reclaimed by the balloon driver |
| `mem.swapped` | `mem.swapped.average` | 2 | MiB | interval average swapped to the host swap file |
| `mem.swapinRate` | `mem.swapinRate.average` | 1 | KBps | interval average rate memory was read back in from the host swap file |

Level is the vCenter statistics level required to retain a counter. The default
is level 1 on every interval, which usually excludes `mem.active` and
`mem.swapped`. Collection skips counters the chosen interval does not retain
and records them as unavailable, with the required level. The window's `Source`
records the interval level.

For active memory, set the interval to level 2 in
`vCenter > Configure > General > Statistics` and wait for history to accumulate.
vsfleet never changes this
setting; it prints a hint naming the interval when active memory is unavailable.

Samples are roll-up averages, typically over 5 minutes to 2 hours. A peak is
the highest interval average and may understate an instantaneous spike.
Storage I/O is not collected until its counters and cost have been validated.

## Bounds and impact

Collection limits are `--window`, `--max-samples` (refuses too many samples
per counter), `--max-vms`, `--max-requests`, and `--max-runtime`. Reaching a
bound marks the window `partial` and lists unsampled VMs. Permission denial
stops further queries.

Each collection records and prints requests used, runtime, sampled/requested
VMs, source API, and server version. Storage keeps summaries for seven counters
per VM per window, without raw samples.

## Unknown is never zero

A `-1` sample means missing data, such as powered-off time, expired history,
or an uncollected counter. It never enters an average.

Statistics are omitted rather than approximated:

- `unavailable`: counter denied, unsupported, not retained at the interval's
  statistics level, or absent while the VM returned other counters.
- `insufficient-data`: fewer than 12 samples or coverage below half the window.
  Powered-off periods are missing; a VM returning no samples for any counter
  also has insufficient data.
- P95 uses nearest rank over interval averages and requires 50 successful samples.

## Sizing signal

Signals describe the collection window; they are not resize instructions:

| Signal | Meaning |
| --- | --- |
| `contention-observed` | CPU ready peaked at 5% per vCPU or more, ballooned or swapped memory reached 1 MiB, or swap-in reached 1 KBps. Outranks utilisation: a starved VM looks idle. |
| `peaks-observed` | Typical CPU or active memory is under 30%, but a peak reached 60% or more. A current sample would understate demand. |
| `in-use` | Usage is not low. |
| `sustained-low` | Peaks stayed under 30% of CPU and of configured memory across the window, with supporting samples. |
| `cpu-only` | `cpu.usage` has enough samples but `mem.active` could not be read, usually because of the statistics level. The reason gives CPU average and peak; memory is unknown, not low. |
| `insufficient-data` | Samples were returned but do not support a reading, including VMs that were not sampled or were powered off. |
| `unavailable` | `cpu.usage` could not be read. |

`sustained-low` requires enough samples for both `cpu.usage` and `mem.active`.
Missing, denied, or sparse inputs produce `cpu-only`, `unavailable`, or
`insufficient-data`.

## In reports

`assessment report` shows each context's newest usable collection and window
dates, or `not collected`. The RVTools workbook's `vsfleetPerformance` sheet
has one row per VM per counter: window, interval, sample counts, average, peak,
P95, status, reason, sizing signal, and source. Missing statistics are blank;
a context without a collection gets one `not collected` row.

Matching uses vCenter identity. A collection may precede or follow the capture;
`Inventory match` indicates whether the VM exists in that run. `vCPU`,
`vMemory`, and `vInfo` remain RVTools-compatible. `assessment prune` removes
performance windows older than its cutoff, preserving each context's newest
usable window.

## Validation status

Collection, units, and signals have synthetic fixture and govmomi simulator
coverage. Real-vCenter validation used Server 8.0.3 at statistics level 1
([issue #214](https://github.com/Easonliuuuuu/vsfleet/issues/214)):

* `cpu.usage`, `cpu.ready`, `mem.consumed`, `mem.vmmemctl` and `mem.swapinRate`
  return 5-minute history; `mem.active` and `mem.swapped` are level 2 and are
  not kept, so running VMs read `cpu-only`.
* VMs powered off for the window return no samples and read
  `insufficient-data`.
* 7-day and 30-day windows on a vCenter that was only running part of that time
  read `insufficient-data` (under half the expected samples), not zero.

The `sustained-low`, `peaks-observed`, and `in-use` thresholds remain
unvalidated against a real vCenter at statistics level 2.
