# Troubleshooting

Inspect the context, test its connection, and locate the failing stage:

```sh
vsfleet context show prod
vsfleet context test prod
vsfleet doctor prod
vsfleet status
```

## The eight-stage diagnostic pipeline

`vsfleet doctor` checks the connection in order and reports the first failure:

1. Configuration: validate TOML properties and context settings.
2. Credentials: resolve the keyring, environment, file, or helper reference,
   or confirm prompt mode.
3. Routing and proxy: check proxy reachability and authentication.
4. DNS resolution: resolve locally or through the configured proxy.
5. TCP handshake: connect to port 443.
6. TLS negotiation: verify the trust chain or pinned thumbprint.
7. SSO authentication: authenticate the configured vSphere user.
8. API handshake: probe vSphere ServiceContent with read access.

Correct [credentials](configuration.md#credentials),
[routes](configuration.md#network-routes), or
[TLS policies](configuration.md#tls-policies) at the failed stage.

## Partial failures

Estate-wide commands keep healthy results when a vCenter fails. The terminal
UI retains cached inventory with a stale-data warning.

If a context needs an interactive password, select it or reload it explicitly.
Background refresh never interrupts the terminal with a password prompt.

## Assessment database checks

If history operations fail, inspect the local ledger without contacting vCenter:

```sh
vsfleet assessment doctor
vsfleet assessment backup ./history-backup.db
```

Check database permissions and disk space. Back up before restoring or pruning;
see [Retention and recovery](assessments.md#retention-and-recovery).
