# Synthetic testbed

VSFleet's testbed is repository tooling, not an agent-only capability. The
single entrypoint is `scripts/testbed`.

## Profiles

The default `presentation` profile runs the deterministic, offline
`cmd/vsfleet-demo` backend. It is read-only, visibly synthetic, and never
reads operator configuration, credentials, keyrings, or the network.

The optional `connected` profile runs `cmd/vsfleet-testbed`. It starts
govmomi simulator SOAP endpoints and SOCKS5/HTTP CONNECT proxies on loopback,
uses fixture-only credentials, and stores state beneath an isolated root. It
exercises production connection and credential plumbing but is not evidence of
real-vSphere behavior.

## Daily commands

```sh
scripts/testbed prepare
scripts/testbed shell-hook
scripts/testbed launch
scripts/testbed verify
scripts/testbed list
scripts/testbed test
scripts/testbed test overview
scripts/testbed pty
scripts/testbed sandbox datastore-browser
```

`prepare` prints a removable shell-hook command. A subprocess cannot change
the parent shell's `PATH`, so activate it explicitly:

```sh
eval "$(scripts/testbed shell-hook)"
vsfleet
```

Use `--profile connected` when the production connection path is the subject
of the check. Connected automated scenarios use a per-process loopback port
range and keep
their temporary roots isolated from normal VSFleet configuration.

## Scenario catalogue

`scripts/testbed list` is the source of truth for names and short purposes.
Each scenario has a deterministic headless runner and can be selected for a
developer sandbox. Scenario failures retain a view and semantic observation
under `--results-dir PATH`; fixture passwords are never written there.

The initial catalogue covers overview, partial failure, duplicate names,
credential cancellation, stale results, history coverage gaps, safe context
addition, datastore browsing, and terminal resizing.

## Render contracts

Only critical screens have goldens. They are ANSI-normalized and checked at
`60x20`, `100x30`, and `140x40`. Behavior is asserted semantically; goldens
are not a replacement for state assertions. A longer journey can capture its
golden at an explicit checkpoint before completing its final assertions.

Review a change before rewriting files. Regeneration requires an explicit
flag:

```sh
scripts/testbed test --update-goldens
```

## Real-terminal PTY validation

On Linux, `scripts/testbed pty` builds and launches the actual connected
`cmd/vsfleet-testbed` process inside a pseudo-terminal. Its nine journeys cover
clean inventory startup and exit, SSH failure and cancellation, credential
cancellation, recursive datastore browsing, History pane cleanup, narrow-to-wide
resizing, the VM performance dashboard, and Ctrl-C while a capture is active. Assertions follow semantic
screen text and process exit status rather than snapshotting terminal byte
streams.

Use `--results-dir PATH` to choose where each journey retains its redacted raw
process output, ANSI-normalized transcript, input/resize event log, result
metadata, and isolated testbed state:

```sh
scripts/testbed pty --results-dir /tmp/vsfleet-pty
```

The connected lab seeds a small synthetic datastore tree for this journey and
interactive sandbox use. PTY artifacts replace fixture passwords with
`[REDACTED]`; they never read operator configuration or keyrings.

## Verification ladder

The testbed is one layer in the full verification ladder. Run
`docs/testing.md` for the distinction between unit/race tests, synthetic
scenarios, the Linux PTY process boundary, in-process and out-of-process vcsim,
Kubernetes, and future real-vSphere acceptance.

## Safety invariants

- presentation fixtures remain deterministic, offline, read-only, and visibly synthetic;
- connected services bind only to loopback and use fixture credentials;
- no testbed mode copies real endpoints, passwords, thumbprints, inventory, or keyring data;
- add/edit/remove actions do not pretend to succeed in the presentation backend;
- simulator results never claim real-vSphere compatibility.
