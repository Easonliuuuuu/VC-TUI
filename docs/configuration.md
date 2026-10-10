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

A credential reference names the password source, never the password itself.
It can be stored in `config.toml` or version control.

| Value | Behavior |
|---|---|
| `keyring:<name>` | Read the password from the native OS secret store |
| `prompt` | Prompt interactively on each run and store nothing on disk |
| `env:<VAR>` | Read the password from an environment variable |
| `file:<path>` | Read the password from a file; one trailing newline is stripped |
| `exec:<program>` | Run a program and read the password from its standard output |

vsfleet never writes passwords to TOML or logs. Avoid putting them in shell
commands that would save them in command history.

### Unattended sources

Use `env`, `file`, or `exec` for cron, systemd, containers, and CI. These
sources are read-only: `--password-stdin` is rejected with them. A missing or
failing source produces a specific error and never falls back to a prompt.

- `env`: useful for injected secrets. Another process running as the same
  user may read `/proc/<pid>/environ`; prefer `file` when that matters.
- `file`: works with systemd `LoadCredential=`, Kubernetes secret mounts,
  and Docker secrets. vsfleet does not enforce file permissions; protect the
  file using your platform's controls.
- `exec`: runs one program with no arguments or shell. Use a wrapper when
  your secret-manager helper needs arguments. Secrets stay off the command line.

The helper receives the context name, for example:

```sh
#!/bin/sh
# /usr/local/bin/vsfleet-credential
exec vault read -field=password "secret/vcenter/$VSFLEET_CONTEXT"
```

| Variable | Value |
|---|---|
| `VSFLEET_CONTEXT` | The context whose password is being resolved |
| `VSFLEET_CREDENTIAL_REF` | The reference being resolved, e.g. `exec:/usr/local/bin/vsfleet-credential` |

The helper receives no standard input. A hung helper is bounded by its
context's `--timeout`.

### Without an OS keyring

Headless servers, SSH sessions, WSL, and containers may lack an OS keyring.
The CLI wizard and TUI add-context form probe it with a lookup, without writing
a test entry. If unavailable, they explain the failure and offer `prompt`,
`env`, `file`, and `exec`. Unattended sources ask for a reference, then test
the connection through it.

With `--password-stdin` and no `--credential`, unattended `context add`
still attempts keyring storage. If the write fails, it saves
`credential = "prompt"` and warns.

### When a source stops answering

A missing variable, file, or helper leaves the context configured but unable
to connect. Restore the source or edit its reference; vsfleet never prompts as
a fallback. The CLI prints the cause, such as
`environment variable LAB_VC_PW is not set (env:LAB_VC_PW)`. The TUI names
the source on the failure line and offers a fix in its
[diagnosis](tui.md#a-password-source-that-is-gone), where `e` edits it.

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

The [SSH handoff](tui-actions.md#detail-pane-actions) uses `vm_user` for VM
guests and `host_user` for ESXi hosts, falling back to the shared `user`.
With no configured user, OpenSSH uses `~/.ssh/config` and then the local
username.

Per-machine users and private-key paths chosen in the TUI override these
defaults and are saved in `state.json`. OpenSSH default lets
`~/.ssh/config` and `ssh-agent` choose; an explicit identity uses public-key
authentication only.

An unauthenticated SOCKS5 or HTTP CONNECT context can generate
`ssh -o ProxyCommand=...` with a compatible `nc`. HTTPS and authenticated
proxies are declined: their TLS handshake or credentials cannot safely be
represented in the generated command.

### SSH routes

Use `[[ssh.routes]]` when a VM guest network needs a different route from
its vCenter. Rules select the SSH route by context and destination CIDR:

```toml
[ssh]
vm_user = "devops"

[[ssh.routes]]
context = "tdc-1f"
cidr = "172.31.7.0/24"
type = "http"
proxy_address = "100.109.21.17:8080"
```

This sends SSH to `tdc-1f` guests in `172.31.7.0/24` through HTTP CONNECT,
while the vCenter route may stay direct. The rendered command shows the route:
`ssh -o 'ProxyCommand=nc -X connect -x 100.109.21.17:8080 %h %p'`.

| Key | Meaning |
|---|---|
| `context` | Required. The context the route applies to; guests in another context never match. |
| `cidr` | Required. The destination network, normalized on load (`172.31.7.5/24` becomes `172.31.7.0/24`). |
| `type` | `direct`, `socks5` or `http`. `direct` overrides a proxied context for that network. |
| `proxy_address` | `host:port` of the proxy. Required for `socks5` and `http`; not allowed for `direct`. |

Selection uses the guest IP reported by vSphere, even when SSH targets a DNS
name. It never resolves a hostname or probes routes:

1. Use the matching context/CIDR rule with the longest prefix.
2. With no match, inherit the context's transport.
3. Without a transport override, let OpenSSH choose normally.

ESXi hosts use the context transport because their targets are host names.
Proxy rules add `ProxyCommand` without replacing other OpenSSH configuration.
A failed route is not retried through another route.

Routes carry no credentials and reject HTTPS or authenticated proxies.
Conflicting duplicate context/network rules and other invalid routes prevent
startup; identical duplicates are allowed.

### Three complementary routing sources

Three sources can choose a VM's route:

| Source | Scope |
| --- | --- |
| OpenSSH alias | An existing `~/.ssh/config` route, validated with bounded `ssh -G` against the VM's guest IP |
| Per-VM remembered route | A TUI choice saved for one machine in `state.json` |
| `[[ssh.routes]]` | Context/CIDR policy in `config.toml` |

See [destination and route](tui-actions.md#destination-and-route) for
precedence and [alias discovery](tui-actions.md#openssh-alias-discovery) for
validation. Aliases are revalidated on every use; stale entries are dropped
and rediscovered. Route choices are explicit, never inferred by trying a
connection.

Per-VM preferences use the key `<context>/<moref>` and save only user,
identity path, alias name, or route kind and proxy `host:port`. They contain
no passwords, private-key contents, proxy credentials, or shell fragments.

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

The built-in ReadOnly role covers connection tests and standard inventory
collection, including datastore backing identity. Optional reads need extra
privileges:

| Feature | Additional privilege |
| --- | --- |
| `assessment run --browse-datastores`, `--datastore-file-inventory`, interactive datastore browser | `Datastore.Browse` on the inspected datastores |
| `assessment run --include-licenses` | `Global.Licenses`, absent from the built-in ReadOnly role |

These paths remain read-only. Without browsing or its privilege, zombie-VMDK
health is not evaluated. Denied license collection reports `unavailable`,
never zero licenses; see [license metadata](licensing.md). File inventory
exports paths and filenames; review [its limits and privacy notes](exports.md#datastore-file-inventory-vfileinfo)
before enabling it.
