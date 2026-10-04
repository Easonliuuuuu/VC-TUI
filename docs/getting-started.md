# Getting Started

## Install

### Homebrew (macOS and Linux)

```sh
brew install --cask easonliuuuuu/tap/vsfleet
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
OS keyring when available. On a headless system, point the context at where the
password already lives with `--credential env:<VAR>`, `file:<path>` or
`exec:<program>` — see [unattended sources](configuration.md#unattended-sources).

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
