# Getting Started

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

### Linux Packages (Debian, Ubuntu, RHEL, Fedora)

Download native `.deb` or `.rpm` packages from the
[GitHub Releases](https://github.com/Easonliuuuuu/vsfleet/releases) page:

```sh
# Debian / Ubuntu
sudo dpkg -i vsfleet_*_linux_amd64.deb

# RHEL / CentOS / Fedora
sudo rpm -i vsfleet_*_linux_amd64.rpm
```

### Pre-built release binary

Download an archive for your operating system and CPU architecture from the
[GitHub Releases](https://github.com/Easonliuuuuu/vsfleet/releases) page.

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

The official image is available from GitHub Container Registry for Linux
amd64 and arm64:

```sh
docker run --rm ghcr.io/easonliuuuuu/vsfleet:latest compatibility report --sheet vInfo -o json
```

Use a version tag such as <!-- x-release-please-start-version -->`v0.6.1`<!-- x-release-please-end --> for repeatable deployments, or pin the
image digest in production. The image runs as an unprivileged user and is
designed for unattended commands, assessments, and exports. It does not
include a shell, browser, SSH client, OS keyring, or `exec:` credential helper,
so install the native binary for the interactive terminal UI and workstation
handoffs. See the [Containers guide](containers.md) for mounts and secrets.

### Build from source

```sh
git clone https://github.com/Easonliuuuuu/vsfleet.git
cd vsfleet
go build -o vsfleet ./cmd/vsfleet
```

### Upgrading

vsfleet checks for a newer release at most once a day, in the background.
When one is out, the next launch of the interface asks whether to upgrade.
For Homebrew and `go install` it can run the upgrade for you and restart into
the new version. Scoop, winget, `.deb`/`.rpm` packages and release archives
get the command or download link to copy instead. Other commands print a short
notice on stderr instead of asking. Set `VSFLEET_NO_UPDATE_NOTIFIER=1` to turn
the check off; it never runs under CI or in the container image. See
[Terminal UI](tui.md#upgrade-prompt) and [SECURITY.md](https://github.com/Easonliuuuuu/vsfleet/blob/main/SECURITY.md)
for exactly what is sent.

## Look around first

Before configuring anything, open the interface on sample data:

```sh
vsfleet demo
```

The demo is a synthetic three-vCenter estate: two healthy sites reached by
different routes, and one disaster-recovery site whose proxy refuses the
connection. The main site is production-sized — about 1,000 VMs across six
clusters, 36 datastores and 24 vApps — and History holds five dated
assessments so drift, capacity and snapshot ageing are visible. It reads no
configuration file, opens no keyring, resolves no
credentials, dials nothing, and writes nothing back — so it does not remember
the last screen the way a real run does. Every screen is marked
`DEMO · SAMPLE DATA`.

Historical assessments are unavailable in the demo: there is no captured run
behind the sample data to compare against.

## First context

If no contexts exist, running `vsfleet` opens the setup wizard:

```sh
vsfleet
```

You can also start it explicitly:

```sh
vsfleet context add
```

The wizard asks for an endpoint, username, route, certificate policy, and
password, then tests the connection before saving. Passwords are stored in the
OS keyring when available. On a system without one, such as a headless server
or an SSH session, the wizard says so and asks where the password should come
from instead: a prompt on every run, or an environment variable, a file or a
helper program such as `vault` — see
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

Use `--all-contexts` for an estate-wide operation or `--context NAME` to scope
one command. See the [CLI Guide](commands.md) for output and filtering.

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

For the complete command, use `vsfleet completion --help`.
