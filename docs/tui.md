# Terminal UI

Run `vsfleet` to browse inventory, switch vCenters, and inspect resources.

## Browse screen

| Key | Action |
|---|---|
| `1`–`7` | VMs, templates, hosts, clusters, datastores, networks, vApps |
| `h` / `l`, `←` / `→` | Previous/next resource tab |
| `k` / `j`, `↑` / `↓` | Previous/next row |
| `Enter` | Open selected resource |
| `c` | Manage contexts |
| `a` | Toggle current/all contexts |
| `/` | Filter table |
| `Tab` | Widen filter to estate search |
| `Esc` | Clear filter, restore view |
| `r` / `R` | Reload current/all contexts |
| `d` | Diagnose selected row's vCenter |
| `space` / `t` on Networks | Fold switch / toggle tree and plain list |
| `v` on Networks | Open [VLAN map](tui-network.md#vlan-map) |
| `H` | Open History |
| `?` / `q` | Help / quit |

The selected row has a `▸` marker as well as highlighting, including under
`NO_COLOR`. A focused text input lists its own keys; other keys enter text.
`?` shows help for the current screen, and `Esc` returns there.

For resource-specific controls, see [VM dashboards](tui-vm.md),
[detail actions and SSH](tui-actions.md), [datastore browsing](tui-storage.md),
and [networks and clusters](tui-network.md).

## Filtering and searching

1. Press `/` to filter the current view by name.
2. The query line reports cached matches outside that view:
   `/ubuntu   0 VMs in prod · 2 in the estate — tab to widen`.
3. Press `Tab` to search all resource kinds and vCenters in cached inventory.
4. Press `Tab` again or `Esc` to narrow back, preserving the query.

While typing, `↑`/`↓` and `PgUp`/`PgDn` move the selection.
`Enter` ends text entry and keeps that selection.

## Contexts and vApps

On Contexts (`c`), use `Enter` to select a context, `a` for all contexts,
`n`/`e`/`x` to add/edit/remove, `o` to log out, `d` to diagnose,
and `Esc` to return.

Logging out retains cached inventory and stops background refresh for that
context. Selecting it or reloading with `r` logs in again. Sessions ended by
vCenter are replaced on the next load.

Browse-screen `a` includes only loaded vCenters; it does not connect others.
Missing vCenters appear below the table with their state:

```text
○ not connected: edge-vc, dr-vc · R connects all · c then enter connects one, d diagnoses
◐ connecting: lab-vc
◐ credentials required: site-3 · R asks for them · c then enter picks one
✕ old-vc: connection failed · c select, d diagnose
```

`R` connects all and prompts for needed credentials. `c`, then `Enter`,
connects one and selects it; `c`, then `d`, diagnoses without connecting.
Long lists and narrow-terminal hints collapse to save space. Estate search
reports the same missing-context reasons.

### A password source that is gone

`env:`, `file:`, and `exec:` passwords are read on each connection. A
missing source leaves the context configured and identifies the cause:

```text
✕ lab-vc: LAB_VC_PW is not set · d diagnose
✕ lab-vc: password file /home/ops/.config/vsfleet/lab.pw does not exist · d diagnose
✕ lab-vc: password helper vsfleet-credential failed: Vault is sealed · d diagnose
```

Diagnosis (`d`) gives source-specific recovery steps:

- `env:`: export the variable before restarting vsfleet. Reload cannot see
  environment changes made after launch.
- `file:`: restore the password file, then press `r`.
- `exec:`: run the helper with `VSFLEET_CONTEXT` set to the context name;
  once it works, press `r`.

Press `e` in diagnosis to change the source. The form offers keyring, prompt,
env, file, and exec, prefilled with saved settings. It tests the new source
before saving. Moving off keyring removes the old entry after saving unless
another context uses it. See [credential configuration](configuration.md#credentials).

### vApp workspace

`Enter` on a vApp shows CPU/memory allocation (limit, reservation, shares,
expandable), startup order, and nested members with a `START` column. The
vApp's limit is shared across members and can throttle VMs with unlimited
individual settings.

Arrow keys select nested vApps, VMs, and pools. `Enter` opens a VM inspector;
`Esc` returns a level. VM headers offer [SSH/copy actions](tui-actions.md).
A recognized vCenter VM can seed a context from its IP, copying the parent
route. The saved context retains the VM reference and annotates its member
row with the context name.

Live charts sum member VM performance, including nested vApps:

- CPU usage sums MHz. With a vApp limit, that limit is the chart ceiling and
  `▲` marks samples within 5% of it. Otherwise the ceiling is member vCPUs
  at host core speed.
- Active memory sums against the vApp memory limit, or configured memory if
  unlimited. The reservation appears alongside.
- Members show peak CPU (MHz, `▲` when busy for their vCPUs), CPU ready,
  and CPU limited time. `s` toggles startup order/busiest first.

Under 100 columns or on short terminals, charts become sparklines.
`<`/`>` changes range and `r` rereads, using the
[VM refresh schedule](tui-vm.md#ranges-and-refresh). Unreadable members are
named and excluded from totals.

## History workspace

Press `H` for Changes, Trends, Runs, and Health.
`Tab`/`Shift+Tab` switches panes.

Changes and Trends use the selected context, or all configured contexts in the
all-vCenters view. `n` captures that same scope. Trends names contexts without
stored data. The coverage matrix, Runs, and Health are always estate-wide.
Health shows the latest stored assessment's migration verdict, categories,
and default findings, ordered critical, warning, info. `↑`/`↓` scrolls.
Use [`vsfleet health`](health.md) to tune thresholds.

Changes starts with the newest two assessments. Its run axis goes oldest to
newest and marks baseline `b` and target `t`:

| Keys | Action |
|---|---|
| `b` / `t` | Choose end to move; active end is uppercase |
| `←` / `→` | Move one run, skipping the other end |
| `R` | Open full run picker for active end |
| `s` | Swap ends |
| `c` | Set baseline to newest older run covering the target's vCenters |
| `1`–`4`, `0` | Filter blocks/sizing/growth/churn; clear filter |
| `Enter` | Open change inspector on narrow terminals |
| `h` | Open VM timeline from a change |
| `n` | Capture current scope |
| `e` / `N` / `p` in Runs | Edit label / operator note / pin |

`?` lists the current pane's keys. Captures run in the background while
inventory remains usable.

Coverage uses `●` reached, `✕` not compared, and `·` no record.
It collapses when visible runs have identical coverage; otherwise it names the
missing sites. `c` clips the comparison to shared coverage.

Changes rank migration blockers first: vanished VMs/hosts, snapshots older
than 30 days, and changed migration configuration. These stay separate.
Other identical changes may share a counted row such as
`k8s-worker-0… 12 VMs`; the inspector lists all members. Wide terminals
show the inspector beside the stream.

See [assessments](assessments.md) for capture and comparison workflows.

### VM timeline

Press `h` on a VM's detail pane, a vApp member, or a Changes row to open that
VM's timeline. It has three sources, one tab each:

| Tab | Source | What it answers |
|---|---|---|
| `1 Changes` | Stored assessments | What changed between runs. Works offline and goes back as far as your history. |
| `2 vCenter events` | vCenter's event log, read live | Who did what and when, including failed tasks and changes undone before the next run. Only as far back as vCenter keeps events, usually 30 days. |
| `3 Combined` | Both | Each stored change beside the event that caused it, grouped by the gap between the two runs that bracket it. |

On Combined, `←` marks an event that caused the stored change on its line or
above it. `·` marks an event that left no stored change: it failed, it was
undone before the next run, it is newer than any run, or it is a kind of event
assessments do not record. An event explains a change only when it falls
between the same two runs, its kind can produce that change, it did not fail,
and the details agree. An event logged while a run was collecting counts
toward that run's changes if it explains one, and toward the next run's
otherwise. For example, a migration explains `moved` only if it
ended on the host the run recorded.

vCenter events are read only when you open an events tab or press `r` on
one. Opening a tab reads vCenters that are already connected. `r` also
connects a configured vCenter that is not. The tab header always says `LIVE`, which
vCenter the events came from, and the oldest event returned. A vCenter that
is not connected, not configured, or refused the read is named rather than
left out. A VM that moved between vCenters is read on each one, and a VM
stored under more than one managed object ID, for example after it was
re-registered, is read under each ID.

| Keys | Action |
|---|---|
| `Tab` / `Shift+Tab`, `1`–`3` | Switch source |
| `a` | Show unchanged runs (Changes) or routine power, guest and task events (events tabs) |
| `r` | Read vCenter events again (events tabs) |
| `Enter` | Open the change or event detail |

DETAIL shows what a migration, rename, reconfiguration or clone changed, and
for every other event vCenter's own message on one line, without the VM, host
and datacenter it is about. `BY` widens with the terminal so a long user such
as `VSPHERE.LOCAL\Administrator` is shown whole when there is room.

The same event log is available on the command line as
`vsfleet vm events <name-or-uuid>`.

## Refresh and cache behavior

| Context state | Background behavior |
|---|---|
| Selected | Refresh every 20 seconds by default |
| Successfully visited, inactive | Refresh every 200 seconds |
| Unvisited or logged out | No connection |
| Waiting for interactive credentials | Retry only when selected or explicitly reloaded |
| Refresh failed | Keep cached inventory with warning |

Use `--refresh 5s` to poll faster or `--refresh -1` to disable timers.
Slow contexts poll at roughly three times their last load duration when that
exceeds the interval.

## Large estates

Inventory loads have no fixed enumeration deadline; they fail when progress
stops. Rows arrive in pages while the cursor stays on the selected machine.
`--timeout` still bounds connection and authentication.

Browsing omits expensive virtual disks, guest NIC bindings, and snapshot
trees; [assessment capture](assessments.md) retrieves them. The path index
is reused for two minutes; new VMs use their parent folder to retain their
datacenter and path.

When credentials are required, the pane stays usable. Select or reload the
context to open its masked password prompt.

## Welcome animation

On first run and after upgrades, a welcome animation lasts under three
seconds while the remembered vCenter loads. Any key skips it; `Ctrl+C` quits.
Upgrades show old/new versions and release notes. Below 64×18, the message
line replaces the animation.

`VSFLEET_NO_WELCOME` or `CI` disables it. `state.json` stores
`welcomed_version`; source builds share version `dev`.

## Upgrade prompt

A newer release known at launch prompts after the welcome animation:

| Key | Action |
|---|---|
| `y` | Run upgrade in foreground and restart; Homebrew/`go install` on macOS/Linux only |
| `c` | Copy upgrade command or release link; Scoop, winget, `.deb`/`.rpm`, archives |
| `n` / `Esc` | Snooze for one week; badge remains |
| `s` | Skip this release's prompt and badge |

Failed upgrades show the exit status and reopen the current version. Releases
discovered during a session add a header badge and prompt next launch.
`update.json` stores checks, snoozes, and skips beside `state.json`.
`VSFLEET_NO_UPDATE_NOTIFIER=1` disables checks, prompts, badges, and CLI notices.

<span id="vm-detail-dashboard"></span>

## VM dashboard

See [VM properties and performance charts](tui-vm.md).

<span id="detail-pane-actions"></span>
<span id="openssh-alias-discovery"></span>
<span id="destination-and-route"></span>

## Detail actions and SSH

See [copying values, external tools, SSH destinations, and routing](tui-actions.md).

<span id="datastore-file-browser"></span>
<span id="real-vsphere-validation-gate"></span>

## Datastore browser

See [datastore browsing and recursive search](tui-storage.md).

<span id="networks-and-switches"></span>
<span id="vlan-map"></span>
<span id="host-network-page"></span>
<span id="cluster-workspace"></span>

## Networks and clusters

See [switches, VLANs, host networking, and cluster relationships](tui-network.md).
