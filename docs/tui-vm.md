# VM dashboard

Press `Enter` on a VM to inspect its properties and performance. Use the
[detail actions](tui-actions.md) to copy fields, connect over SSH, or open the
vSphere Client.

## VM detail dashboard

Long property values wrap beneath their labels. The field cursor selects the
full value, including wrapped DNS names and inventory paths; copying preserves
it. Use `PgUp`/`PgDown` to scroll tall values.

Health marks use the same default thresholds as [History Health](tui.md#history-workspace):

| Field | `✓` | `▲` |
|---|---|---|
| VMware Tools | running | not installed, not running on a powered-on VM, or needs an upgrade |
| Snapshots | none, or oldest younger than 30 days | oldest 30 days or older, with age shown |
| Guest disks | fullest filesystem has at least 10% free | fullest filesystem has less than 10% free |

Missing evidence gets no mark: an empty Tools state, unread VM configuration,
or no filesystems reported by Tools.

## Chart pages

Charts sit beside the properties, or below them under 100 columns. An action
menu can also move charts below the properties; closing it restores the layout.
All five pages share one read, so changing pages makes no vCenter request.

| Key | Page | Shows |
|---|---|---|
| `0` | Overview (default) | CPU usage, active memory, disk read + write and peak latency; CPU ready, co-stop, CPU limited, balloon, swap-in, network throughput, dropped packets, sizing signal |
| `1` | CPU | Usage, ready, co-stop, CPU limited; “no CPU limit set” when none exists |
| `2` | Memory | Active and consumed memory as a share of configured memory, balloon, swap-in rate |
| `3` | Disk | Read/write throughput, worst disk latency, IOPS |
| `4` | Network | Received/transmitted throughput, dropped packets |

Titles show average, 95th percentile, and peak, subject to the minimum sample
count and window coverage used by [performance summaries](performance.md).
Each chart column keeps its largest sample. A faint baseline means zero, a dot
means no samples, and `▲` marks a reading at or above these thresholds:

| Reading | Threshold | Basis |
|---|---|---|
| CPU usage, active memory | 60% | sizing signal |
| CPU ready | 5% per vCPU | sizing signal |
| Balloon | 1 MiB | sizing signal |
| Co-stop | 3% per vCPU | common VMware guidance |
| CPU limited | 1% per vCPU | measurable throttling |
| Disk latency | 20 ms | common VMware guidance |
| Swap-in rate | 1 KB/s | active swapping |
| Dropped packets | 1 per sample | any drop |

These chart marks are separate from health rules and are neither stored nor
exported.

## Ranges and refresh

| Key | Action |
|---|---|
| `0`–`4` | Switch chart page |
| `<` / `>` or `,` / `.` | Shorter / longer range: 1h, 24h, 7d, 30d |
| `r` | Re-read the range |
| `←` / `→` | Change VM, keeping the page and range |

The 1h range uses 20-second realtime samples; 24h, 7d, and 30d use 5-minute,
30-minute, and 2-hour vSphere roll-ups. The window ends show clock times for
1h, dates and times for 24h, and dates for 7d/30d. “As of” is the read time.
Roll-ups publish after their interval closes, often leaving a 5 to 30 minute gap
at the end.

| Range | Refresh interval |
|---|---|
| 1h | 20 seconds |
| 24h | 5 minutes |
| 7d, 30d | 10 minutes |

Refresh runs while the pane is open. Returning to an expired cached read
refreshes it. The previous charts remain during a refresh; failure preserves
them with a warning and read time. A negative [background refresh interval](tui.md#refresh-and-cache-behavior)
disables chart refresh too and removes “live” from the header.

Only the open VM makes a read-only `QueryPerf` request. Device disk/network
counters are summed when no VM total exists. Dashboard counters are separate
from stored assessment performance. Longer ranges depend on the vCenter's
statistics level; unavailable counters show “no samples” and a reason. An
initial failure replaces charts with an error while properties remain usable.
`vsfleet demo` shows fixed synthetic charts.
