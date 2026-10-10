# Synthetic testbed

Use `scripts/testbed` for synthetic UI development and checks. The harness,
fixtures, and sandbox are checked into the repository.

## Profiles

| Profile | Backend | Boundaries |
| --- | --- | --- |
| `presentation` (default) | `cmd/vsfleet-demo` | Deterministic, offline, read-only, visibly synthetic; never reads operator configuration, credentials, keyrings, or network state |
| `connected` (opt-in) | `cmd/vsfleet-testbed` | Simulator SOAP endpoints and SOCKS5/HTTP CONNECT proxies on loopback only; fixture credentials and state under an isolated root |

Choose `--profile connected` to exercise production connection and credential
handling. Automated scenarios use per-process loopback port ranges and
temporary roots isolated from normal configuration. Neither profile establishes
real-vSphere compatibility.

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

`prepare` prints a removable shell hook. Activate it in your shell so `vsfleet`
uses the testbed; a subprocess cannot change its parent's `PATH`:

```sh
eval "$(scripts/testbed shell-hook)"
vsfleet
```

## Scenario catalogue

`scripts/testbed list` lists scenario names and purposes. Each has a headless
runner and a selectable developer sandbox. See the [scenario matrix](test-catalog.md#tui-scenarios)
for coverage. Failures retain a view and semantic observation under
`--results-dir PATH`, without fixture passwords.

## Render contracts

Critical screens have ANSI-normalized goldens at `60x20`, `100x30`, and
`140x40`. Scenarios also assert state; a matching render alone does not prove
behavior. Journeys may capture a golden at a checkpoint before their final
assertions.

Review the change before regenerating goldens:

```sh
scripts/testbed test --update-goldens
```

## Real-terminal PTY validation

On Linux, run the connected testbed process in a pseudo-terminal:

```sh
scripts/testbed pty --results-dir /tmp/vsfleet-pty
```

The [PTY journeys](test-catalog.md#pty-journeys) check semantic screen text and
exit status. Artifacts include redacted process output, normalized transcripts,
input/resize logs, result metadata, and isolated state. The lab seeds a small
synthetic datastore tree for browsing. Fixture passwords become `[REDACTED]`;
the journeys never read operator configuration or keyrings.

## Verification ladder

See [Testing](testing.md) for unit/race tests, model scenarios, Linux PTY,
in-process and out-of-process vcsim, Kubernetes, and manual real-vSphere
acceptance.

## Safety invariants

- Presentation fixtures stay deterministic, offline, read-only, and visibly synthetic.
- Connected services bind only to loopback and use fixture credentials.
- Testbed modes never copy real endpoints, passwords, thumbprints, inventory, or keyring data.
- Presentation add/edit/remove actions do not pretend to succeed.
- Simulator results never claim real-vSphere compatibility.
