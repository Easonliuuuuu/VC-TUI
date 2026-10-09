# Configuration

The default configuration file is:

```text
~/.config/vsfleet/config.toml
```

Override it with `--config <path>` or `VSFLEET_CONFIG`. The file is created with
`0600` permissions and contains no passwords.

## Example `config.toml`

```toml
version = 1
current_context = "prod"

[[contexts]]
name = "prod"
endpoint = "https://vcsa.example.internal"
username = "administrator@vsphere.local"
credential = "keyring:prod"

[contexts.transport]
type = "direct"

[contexts.tls]
mode = "system"

[[contexts]]
name = "customer-enclave"
endpoint = "https://vcsa.enclave.internal"
username = "readonly@vsphere.local"
credential = "keyring:customer-enclave"
via = "prod"
via_moref = "vm-1234"

[contexts.transport]
type = "socks5"
address = "127.0.0.1:1080"
remote_dns = true

[contexts.tls]
mode = "thumbprint"
thumbprint = "1A:2B:3C:4D:5E:6F:..."
```

Each context keeps its endpoint, credentials, route, and TLS policy isolated.
Editing or removing a context invalidates its existing session and cache.

## Credentials

A credential reference names *where* the password lives. It never contains the
password, so it is safe in `config.toml`, in version control, and in anything
that prints your configuration.

| Value | Behavior |
|---|---|
| `keyring:<name>` | Read the password from the native OS secret store |
| `prompt` | Prompt interactively on each run and store nothing on disk |
| `env:<VAR>` | Read the password from an environment variable |
| `file:<path>` | Read the password from a file; one trailing newline is stripped |
| `exec:<program>` | Run a program and read the password from its standard output |

Passwords never go into TOML, logs, or command history.

### Unattended sources

`env`, `file` and `exec` resolve without a terminal, which is what makes cron,
systemd, containers and CI possible. They are read-only: they say where a
password is, and nothing stores one through them, so `--password-stdin` is
rejected when combined with them rather than accepted and silently discarded.

They also never fall back to the interactive prompt. A missing variable or an
unreadable file is an error naming exactly what is missing — not a password
prompt that would read whatever a scheduled job happened to have on standard
input.

`env` suits CI runners and container runtimes that inject secrets into the
process environment. On a shared host another process of the same user can
read `/proc/<pid>/environ`, so prefer `file` where that matters.

`file` is the shape a systemd `LoadCredential=` unit, a Kubernetes secret
mount, and a Docker secret all present. File permissions are not enforced,
because Kubernetes projects secret volumes world-readable inside the container
by default; protecting the file is yours to decide.

`exec` reaches a secret manager vsfleet does not integrate with. The reference
names a program and nothing else — no arguments and no shell — so a
configuration file cannot become a shell command, and no secret is ever placed
on a command line where `ps` would show it. A helper needing arguments is a
wrapper script; it is told which context it is answering for:

```sh
#!/bin/sh
# /usr/local/bin/vsfleet-credential
exec vault read -field=password "secret/vcenter/$VSFLEET_CONTEXT"
```

| Variable | Value |
|---|---|
| `VSFLEET_CONTEXT` | The context whose password is being resolved |
| `VSFLEET_CREDENTIAL_REF` | The reference being resolved, e.g. `exec:/usr/local/bin/vsfleet-credential` |

The helper inherits no standard input, and a helper that hangs fails its own
context's `--timeout` rather than holding up the rest of the estate.

### Without an OS keyring

A headless server, an SSH session to a bastion, WSL and most containers have
no Secret Service, so there is nowhere to store a password. Before asking for
one, the `context add` wizard and the terminal interface's add-context form
check whether the keyring can be used. The check is a lookup only and never
writes a test entry. When the keyring is unavailable, they say why and offer
`prompt`, `env`, `file` and `exec` instead of `keyring`. For the last three
they ask only where the password lives, then run the connection test through
that source, so a missing variable or a failing helper shows up during setup
rather than on the first unattended run.

An unattended `context add` that pipes a password with `--password-stdin` and
names no `--credential` still asks for the keyring. If the keyring write fails,
the context is saved as `credential = "prompt"` with a warning, never as a
keyring reference with nothing stored behind it.

## Network routes

| Transport | Behavior |
|---|---|
| `direct` | Direct TCP connection with local DNS resolution |
| `socks5` | SOCKS5 proxy; `--remote-dns` resolves through the proxy |
| `http` | HTTP CONNECT forward proxy |
| `https` | HTTPS CONNECT forward proxy with TLS |

Proxy authentication can use a separate credential reference of any scheme
above, set with `--proxy-credential`. The unattended setup flags are documented
by `vsfleet context add --help`.

## SSH

```toml
[ssh]
user = "ubuntu"
# vm_user = "ubuntu"
# host_user = "root"
```

Sets the default remote username the TUI's SSH handoff action (see
[Detail pane actions](tui.md#detail-pane-actions)) fills in ahead of a target
address. `vm_user` applies to VM guest IPs and `host_user` applies to ESXi host
names. The older shared `user` setting remains the fallback for both kinds.
When the applicable setting is empty, `ssh` resolves a user through
`~/.ssh/config` and then the local username.

A user and private-key path selected in the TUI's **SSH with a different
destination, user or key…** overlay are remembered per machine and take
precedence over these defaults; they are stored in `state.json`, not in this
file. Selecting OpenSSH default leaves `~/.ssh/config` and `ssh-agent` in
control. An explicitly selected identity uses public-key authentication only.

SSH through a proxied context follows the same route vsfleet itself uses —
an unauthenticated SOCKS5 or HTTP CONNECT proxy becomes an `ssh -o
ProxyCommand=...` argument automatically when a compatible `nc` is installed.
HTTPS and authenticated proxies are declined because their credentials or TLS
handshake cannot safely be represented by the generated command.

### SSH routes

A context's transport describes how vsfleet reaches the **vCenter**. A VM's
guest address is a different destination: vCenter reports it, but that does not
mean your workstation can reach it the same way. `[[ssh.routes]]` describes the
route to SSH targets separately, chosen by context and destination network:

```toml
[ssh]
vm_user = "devops"

[[ssh.routes]]
context = "tdc-1f"
cidr = "172.31.7.0/24"
type = "http"
proxy_address = "100.109.21.17:8080"
```

Here the vCenter behind `tdc-1f` may stay `direct`; only SSH to guests in
`172.31.7.0/24` goes through the HTTP CONNECT proxy, exactly as if you had run
`ssh -o 'ProxyCommand=nc -X connect -x 100.109.21.17:8080 %h %p'`. The rendered
command in the detail pane shows the route in use. Your remembered user and
identity for the machine are unaffected.

| Key | Meaning |
|---|---|
| `context` | Required. The context the route applies to; guests in another context never match. |
| `cidr` | Required. The destination network, normalized on load (`172.31.7.5/24` becomes `172.31.7.0/24`). |
| `type` | `direct`, `socks5` or `http`. `direct` overrides a proxied context for that network. |
| `proxy_address` | `host:port` of the proxy. Required for `socks5` and `http`; not allowed for `direct`. |

Selection is deterministic and never probes the network:

1. The route for the selected context whose `cidr` contains the VM's IP address
   applies; when several match, the **longest prefix** wins.
2. With no match, SSH inherits the context's own transport, as before.
3. With neither, `ssh` connects normally and `~/.ssh/config` decides.

Routes match the guest IP vSphere reports, whether you connect by DNS name or
by address, and are never chosen by resolving a hostname. ESXi hosts are known
by name rather than IP, so they keep following the context's transport. A route
only adds a `ProxyCommand` when it is a proxy type; it does not replace
`~/.ssh/config`. There is no fallback between routes: a connection that fails
through its route fails, rather than being retried through another proxy.

`https` and authenticated proxies are rejected in routes for the reason above,
and routes carry no credentials. Duplicate rules for the same context and
network are an error unless identical, and `vsfleet` refuses to start on an
invalid route.

### Three complementary routing sources

`[[ssh.routes]]` is one of three ways a VM's SSH handoff is routed, each
suited to a different scope:

```text
OpenSSH alias
    -> preferred when an operator's own ~/.ssh/config already describes a
       complete, working route to the VM's guest IP — a Host block, its
       ProxyJump, key, user and port, proven with a bounded "ssh -G" rather
       than reconstructed inside vsfleet (see
       "OpenSSH alias discovery" in tui.md)

per-VM remembered route
    -> an interactive, one-off choice (Automatic / OpenSSH default / Direct /
       HTTP / SOCKS5) made in the TUI's destination picker and remembered in
       state.json, for a single machine that needs something other than the
       estate-wide default

[[ssh.routes]]
    -> deterministic context/CIDR policy for repeatable, estate-wide routing,
       version-controlled alongside the rest of config.toml
```

vsfleet never infers a security route by trying one speculatively: an
OpenSSH alias is used only once a bounded `ssh -G` proves its effective
hostname matches the VM, and a per-VM route or `[[ssh.routes]]` rule is only
ever an explicit choice — either typed into `config.toml` or picked in the
TUI — never a guess. Full precedence is documented in
[Destination and route](tui.md#destination-and-route).

Per-VM destinations are stored in `state.json` alongside the remembered user
and identity, keyed the same way (`<context>/<moref>`). An OpenSSH alias
entry there is never trusted on its own: it is re-validated against the VM's
current guest IP on every use, and dropped and rediscovered the moment it no
longer matches, rather than risk connecting somewhere else. Neither entry
ever carries a password, private key, proxy credential, or shell command
fragment — only the alias name, or the route kind and the proxy's own
`host:port`.

## TLS policies

| Policy | Behavior |
|---|---|
| `system` | Verify against the system trust store |
| `thumbprint` | Pin a SHA-256 or SHA-1 certificate fingerprint |
| `insecure` | Disable verification; use only when strictly necessary |

When `--tls thumbprint` has no explicit thumbprint, the setup wizard fetches the
remote certificate, displays its fingerprints, and pins the selected value.

## Assessment history path

Assessment history is separate from `config.toml` and defaults to
`<user-config-dir>/vsfleet/history.db`. Set `VSFLEET_HISTORY_DB` or pass
`--history-db` to override it. The private database contains inventory,
identifiers, paths, annotations, snapshot metadata, and coverage, but never
credentials or session cookies.

## vSphere permissions

Use a read-only vSphere account; the built-in ReadOnly role is enough for
the connection test and all inventory reads. Inventory and assessment
collection do not need write privileges. The optional `vsfleet assessment run
--browse-datastores` path, and the separate opt-in `--datastore-file-inventory`
path, additionally need `Datastore.Browse` on the datastores to inspect
VM disk-file metadata (or, for the inventory, list file names and sizes); it still performs no inventory
mutation. The interactive datastore file browser in the terminal interface
(see [Terminal interface](tui.md#datastore-file-browser)) needs the same
privilege and is likewise read-only. The datastore backing identity properties are part of the same
read-only datastore inventory and require no additional privilege. Without
`Datastore.Browse` or without the flag, zombie-VMDK health is reported as not
evaluated. The opt-in `--include-licenses` path needs the `Global.Licenses`
privilege, which the built-in ReadOnly role lacks; without it license
coverage is reported `unavailable`, never as zero licenses (see
[License metadata](licensing.md)).
