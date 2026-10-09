# Getting started

Try the sample estate, install vsfleet, then connect your first vCenter.
Use a read-only vSphere account for live inventory.

## Look around first

Try the interface with sample data before configuring it:

```sh
vsfleet demo
```

The synthetic demo has three vCenter sites: two healthy sites with different
routes, and a disaster-recovery site whose proxy refuses the connection.
The main site has about 1,000 VMs across six clusters, 36 datastores and
24 vApps. History has five dated assessments showing drift, capacity and
snapshot ageing.

The demo reads no configuration, opens no keyring, resolves no credentials,
and makes no network connections or writes. Unlike a live run, it does not
remember the last screen. Every screen is marked `DEMO · SAMPLE DATA`.

## Install

### Homebrew (macOS and Linux)

```sh
brew install easonliuuuuu/tap/vsfleet
```

### Windows (WinGet & Scoop)

```powershell
# WinGet (Windows 10/11)
winget install vsfleet

# Scoop
scoop bucket add easonliuuuuu https://github.com/Easonliuuuuu/homebrew-tap
scoop install vsfleet
```

### Linux packages (Debian, Ubuntu, RHEL, Fedora)

Download native `.deb` or `.rpm` packages from
[GitHub Releases](https://github.com/Easonliuuuuu/vsfleet/releases):

```sh
# Debian / Ubuntu
sudo dpkg -i vsfleet_*_linux_amd64.deb

# RHEL / CentOS / Fedora
sudo rpm -i vsfleet_*_linux_amd64.rpm
```

### Pre-built release binary

Download a release archive for your operating system and CPU architecture from
[GitHub Releases](https://github.com/Easonliuuuuu/vsfleet/releases).

```sh
# Example for Linux x86_64
curl -sSL https://github.com/Easonliuuuuu/vsfleet/releases/latest/download/vsfleet_linux_amd64.tar.gz \
  | tar -xz vsfleet
sudo install -m 0755 vsfleet /usr/local/bin/
```

Archives are available for Linux, macOS (Apple Silicon and Intel), and Windows
(amd64 and arm64).

### Go installation

Requires Go 1.25 or newer:

```sh
go install github.com/easonliuuuuu/vsfleet/cmd/vsfleet@latest
```

### Container (automation)

The official GitHub Container Registry image supports Linux amd64 and arm64:

```sh
docker run --rm ghcr.io/easonliuuuuu/vsfleet:latest compatibility report --sheet vInfo -o json
```

Use a version tag such as <!-- x-release-please-start-version -->`v0.6.1`<!-- x-release-please-end --> for repeatable deployments, or pin the
image digest in production. The image runs as an unprivileged user for
unattended commands, assessments, and exports.

Install the native binary for the interactive terminal UI and workstation
handoffs. The image has no shell, browser, SSH client, OS keyring, or `exec:`
credential helper. See the [Containers guide](containers.md) for mounts and secrets.

### Build from source

```sh
git clone https://github.com/Easonliuuuuu/vsfleet.git
cd vsfleet
go build -o vsfleet ./cmd/vsfleet
```

### Upgrading

vsfleet checks for updates in the background at most once a day. When an
update is available, the next interface launch asks whether to upgrade.
Homebrew and `go install` users can upgrade and restart from that prompt.
Scoop, winget, `.deb`/`.rpm` and release archive users get a command or
download link to copy. Other commands print a notice on stderr.

Set `VSFLEET_NO_UPDATE_NOTIFIER=1` to disable the check. It never runs in CI
or the container image. See
[Terminal UI](tui.md#upgrade-prompt) and [SECURITY.md](https://github.com/Easonliuuuuu/vsfleet/blob/main/SECURITY.md)
for what the check sends.

## First context

Running `vsfleet` opens the setup wizard if no contexts exist:

```sh
vsfleet
```

You can also open the wizard with:

```sh
vsfleet context add
```

Choose a context name, endpoint, username, route, and certificate policy.
The wizard tests the connection before saving. With an OS keyring, it prompts
for a password and stores it there.

Without a keyring, such as on a headless server or in an SSH session, the
wizard tells you and asks you to choose a password source: a prompt on every
run, an environment variable, a file, or a helper such as `vault`. See
[without an OS keyring](configuration.md#without-an-os-keyring) and
[unattended sources](configuration.md#unattended-sources).

## Explore inventory

```sh
vsfleet context list
vsfleet context test prod
vsfleet status

vsfleet vm list
vsfleet vapp list
vsfleet host list
vsfleet datastore list

vsfleet search ubuntu --all-contexts
```

Replace `prod` with your context name. Use `--all-contexts` to query the estate
or `--context NAME` to select one context. See the [CLI reference](commands.md)
for filtering and output, or open the [terminal UI](tui.md) with `vsfleet`.

## Capture your first assessment

```sh
vsfleet assessment run --all-contexts
vsfleet assessment report latest
```

The capture is stored locally; the report reads it offline. Check coverage
before interpreting missing objects. Continue with [Assessments](assessments.md)
for comparisons, [Exports](exports.md) for XLSX/CSV, or
[Planning](planning.md) for migration and sizing.

## Shell completion

Generate completion for Bash, Zsh, Fish, or PowerShell:

```sh
# Bash
vsfleet completion bash > ~/.local/share/bash-completion/completions/vsfleet

# Zsh
vsfleet completion zsh > "${fpath[1]}/_vsfleet"

# Fish
vsfleet completion fish > ~/.config/fish/completions/vsfleet.fish
```

Run `vsfleet completion --help` for command details.
