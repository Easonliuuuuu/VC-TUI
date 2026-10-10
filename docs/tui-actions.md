# Detail actions and SSH

Open a row with `Enter`, then use `↑`/`↓` to select its header or a populated
field. `Enter` runs a single action or opens a menu when several apply.

## Detail pane actions

| Selected line | Actions |
|---|---|
| VM or host header | SSH, open vSphere/Host Client, copy managed object reference |
| VM recognized as a vCenter | Add vCenter context, or switch to its saved context |
| VM IP or DNS name | SSH, configure destination/user/key, copy SSH command or value |
| Host, datastore, network, or cluster header | Show its VMs |
| Datastore header | [Browse files or Find in datastore](tui-storage.md) |
| VM Host or Cluster field | Jump to that resource's row |
| Cluster Hosts, Host states, or VMs field | Show hosts or VMs in the cluster |
| Cluster Datastores field | Open [Storage](tui-network.md#cluster-workspace) |
| Other fields | Copy value |

Menus skip disabled actions while navigating and list them last with a reason.
Inapplicable actions are omitted. Proxied vCenters disable browser actions
because the browser has no route; SSH can use supported unauthenticated SOCKS5
or HTTP routes. `vsfleet demo` disables process and browser launches.

A VM qualifies as a vCenter when a component of its name or guest DNS name
starts with `vcsa`, `vcenter`, or `vcentre`, or equals `vc` or `vc` plus digits.
A saved nested context also qualifies. Add other vCenters with
`vsfleet context add` or the [Contexts screen](tui.md#contexts-and-vapps).

“Show VMs on this …” keeps the table scoped until `Esc` clears that scope
before clearing anything else.

## SSH destinations and settings

VM headers offer the guest DNS name first, then IP. The DNS name lets
`~/.ssh/config` match a `Host` block; IP remains available when DNS fails.
`localhost` is omitted. Action labels show the user resolved by `ssh -G`,
such as `SSH to tdclab@10.42.7.13`. vSphere does not supply guest login users.

Choose **SSH with a different destination, user or key…** to change those
settings. `Tab` cycles through the applicable Destination, Route, User, and
Identity sections; `Enter` advances and finally connects. Selections are
remembered per machine in `state.json`, not `config.toml`. Automatic Route,
OpenSSH default Identity, or a blank user clears the corresponding override.
Accepting the user reported by OpenSSH does not pin it.

## OpenSSH alias discovery

vsfleet offers an existing alias such as `app-prod` when its effective
`HostName` matches the VM's guest IP. It runs `ssh app-prod` with the alias's
own user, key, port, jump host, and timeout settings.

Discovery reads `~/.ssh/config` and its bounded recursive `Include` files.
Only literal `Host` aliases with a literal matching `HostName` qualify;
wildcards, negated/tokenized patterns, and `Match` hostnames are skipped.
Each candidate is confirmed with a bounded `ssh -G <alias>`.

One validated alias is offered first. Several aliases without a remembered
choice open **Choose OpenSSH alias… (N matches)**. A remembered alias is
revalidated on use; if its effective hostname no longer matches the VM's IP,
vsfleet drops it and repeats discovery.

## Destination and route

The destination picker lists validated `OpenSSH:` aliases, then `Guest DNS:`
and `Guest IP:`. Native targets offer a per-VM Route choice; aliases use their
OpenSSH configuration directly.

| Route | Effect |
|---|---|
| Automatic | Matching `[[ssh.routes]]` rule, then context transport, then plain SSH |
| OpenSSH default | Adds no vsfleet routing arguments; `~/.ssh/config` controls routing |
| Direct | Overrides even OpenSSH `ProxyJump`/`ProxyCommand` |
| HTTP CONNECT / SOCKS5 | Prompts for proxy `host:port`; unauthenticated proxies only |

Destination/routing precedence is: remembered valid alias, unique discovered
alias, remembered per-VM route, matching [SSH route](configuration.md#ssh-routes),
context transport, plain SSH. vsfleet does not try routes speculatively.

User precedence is: per-machine override, [SSH configuration](configuration.md#ssh)
`vm_user` or `host_user`, shared `user`, then OpenSSH's default.

Native SSH targets use a 15-second initial connection timeout. Failed sessions
show OpenSSH's final diagnostic line in the footer. Aliases receive no extra
options and control their own timeout. Selecting an identity explicitly uses
public-key authentication only, so a rejected key does not open a password
prompt.
