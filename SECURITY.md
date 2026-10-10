# Security Policy

vsfleet is designed to operate in enterprise environments spanning multiple
security zones and networks. Security, privacy, and operational safety are core
design priorities.

---

## Security Guarantees & Architecture

### 1. Strictly Read-Only Operations
vsfleet does not contain functionality to modify infrastructure. It cannot:
* Power VMs on, off, or reset them.
* Create, revert, or delete snapshots.
* Reconfigure virtual machines, networks, or datastores.
* Provision or delete inventory objects.

This invariant is enforced at the API layer in `internal/vsphere`: only query
and read-only information collection APIs are implemented.

### 2. Credential Security & Zero Disk Storage
* **No Plaintext Passwords:** vsfleet never writes passwords to `config.toml`,
  log files, or command history.
* **OS Keyring Integration:** When `credential = "keyring:<name>"` is configured,
  passwords are stored in the operating system's native secret manager:
  * Linux: Secret Service API (via `libsecret` / DBus)
  * macOS: Keychain Services
  * Windows: Credential Manager
* **Volatile Memory:** Passwords stored in memory are dereferenced as soon as
  sessions terminate.
* **Headless Safety:** On systems with no secret store and no terminal
  (locked-down containers, systemd units, CI runners), the password is resolved
  from a source the platform already secures: `env:<VAR>`, `file:<path>`, or
  `exec:<program>`. Each names *where* the password lives and never contains
  it, so `config.toml` stays safe to commit. These sources never fall back to
  an interactive prompt — a missing one is an error naming what is missing,
  rather than a prompt that would read whatever a scheduled job had on
  standard input. Where a terminal does exist and nothing is configured,
  vsfleet falls back to prompt mode rather than storing credentials insecurely.
* **Credential Helper Execution:** `exec:<program>` runs a program you name and
  reads the password from its standard output. It names a program and nothing
  else — no arguments and no shell — so a configuration file cannot become a
  shell command, and no secret is placed on a command line where `ps` would
  reveal it. The helper inherits no standard input and is killed when its
  context's timeout expires. Anyone who can write your `config.toml` can choose
  which program runs, so protect it with the same care as any file naming an
  executable.

### 3. Local Assessment History
Historical assessments are opt-in, explicit captures written to a local SQLite
database. The database is created with `0700` directory and `0600` file
permissions and may contain VM names, UUIDs, inventory paths, annotations,
host/datastore placement, and snapshot metadata. It never contains passwords,
keyring material, session cookies, or private keys. Treat the history file as
sensitive operational inventory: protect it with the host's normal disk and
backup controls, and remove runs explicitly with `vsfleet assessment delete
<id> --force` when retention policy requires it.

### 4. Network Isolation & Transport Security
* **Per-Context Isolation:** Each configured vCenter maintains its own isolated
  transport dialer and TLS policy. A compromised or misbehaving proxy route for
  one context cannot intercept traffic meant for another.
* **Certificate Thumbprint Pinning:** When internal enterprise vCenters use
  self-signed certificates or private enterprise CAs not present in the system
  trust store, vsfleet supports pinning SHA-256 or SHA-1 certificate fingerprints
  (`--tls thumbprint`). Any future change in the presented certificate halts the
  connection immediately to prevent man-in-the-middle (MITM) attacks.
* **Release Check:** Apart from vCenter, the only host vsfleet contacts is
  GitHub's API, at most once a day, to learn the newest release: one
  anonymous `GET /repos/Easonliuuuuu/vsfleet/releases/latest` with the
  `vsfleet/<version>` User-Agent and no inventory, context names or other
  identifiers. It uses the standard `HTTPS_PROXY`/`NO_PROXY` settings, never a
  context's vCenter route, and fails silently. The answer is only compared and
  displayed, never executed. Upgrading from the launch prompt runs one of a
  fixed set of package-manager commands chosen from where the binary is
  installed, never `sudo` and never on Windows. Set
  `VSFLEET_NO_UPDATE_NOTIFIER=1` to turn all of this off; CI and the container
  image never check.

---

## Reporting a Vulnerability

If you discover a security vulnerability in vsfleet, please **do not** report it
via a public GitHub issue.

Instead, please report it through one of the following channels:
1. **GitHub Security Advisory:** Open a private draft advisory via GitHub's
   Security tab on the repository.
2. **Email:** Send details directly to the project maintainers.

Please include:
* Description of the vulnerability and its potential impact.
* Steps to reproduce or proof-of-concept configuration.
* Any potential remediations or patches you have identified.

### Response Timeline
* **Initial Acknowledgement:** Within 48 hours of receipt.
* **Assessment & Fix:** We aim to investigate and develop a patch within 7 business days.
* **Public Disclosure:** Coordinated after a patched release is published.
