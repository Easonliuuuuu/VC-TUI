# Operator recipes

These examples combine CLI commands for recurring tasks. See
[Configuration](configuration.md#credentials) for credential sources and
[CLI reference](commands.md#exit-codes) for automation exit codes.

## Estate-wide search with `jq`

Find VMs matching an OS name and print their context, name, and IP address:

```sh
vsfleet vm list --all-contexts -o json | \
  jq -r '.[] | select(.Name | test("ubuntu"; "i")) | "\(.Context)\t\(.Name)\t\(.IPAddress)"'
```

## Unattended collection

Choose an unattended credential source for a container, systemd unit, or CI
job. Store its reference in `config.toml`, never the password.

### CI, with the secret in the environment

```sh
VCENTER_PASSWORD="$CI_VCENTER_SECRET" \
  vsfleet context add \
  --name prod \
  --endpoint https://vcsa.example.internal \
  --username administrator@vsphere.local \
  --credential env:VCENTER_PASSWORD \
  --tls system

VCENTER_PASSWORD="$CI_VCENTER_SECRET" \
  vsfleet assessment run --all-contexts --fail-on-partial
```

### systemd, with the secret in a file

`LoadCredential=` exposes the secret to the unit through
`$CREDENTIALS_DIRECTORY`. Use a helper to read that runtime path:

```ini
[Service]
Type=oneshot
LoadCredential=vcenter:/etc/vsfleet/vcenter.password
ExecStart=/usr/local/bin/vsfleet assessment run --all-contexts --fail-on-partial
```

```sh
#!/bin/sh
# /usr/local/bin/vsfleet-systemd-credential (install as an executable)
exec cat "$CREDENTIALS_DIRECTORY/vcenter"
```

Configure the context before starting the unit:

```sh
vsfleet context add --name prod \
  --endpoint https://vcsa.example.internal \
  --username administrator@vsphere.local \
  --credential exec:/usr/local/bin/vsfleet-systemd-credential \
  --tls system --no-test
```

`--no-test` defers the connection test until the service has its credential
directory. vsfleet reads `file:` paths literally; systemd's `%d` expansion
does not apply inside `config.toml`. For a fixed Kubernetes or Docker secret
mount, use `file:/var/run/secrets/...` or `file:/run/secrets/...` instead.

### A secret manager, through a helper

`exec:` reads a program's standard output and passes the context name in
`VSFLEET_CONTEXT`, so one helper can serve the estate:

```sh
#!/bin/sh
# /usr/local/bin/vsfleet-credential
exec vault read -field=password "secret/vcenter/$VSFLEET_CONTEXT"
```

```sh
vsfleet context add --name prod \
  --endpoint https://vcsa.example.internal \
  --username administrator@vsphere.local \
  --credential exec:/usr/local/bin/vsfleet-credential \
  --tls system
```

Put arguments in the helper; the reference accepts only a program path, with
no shell expansion. See [Unattended sources](configuration.md#unattended-sources).

### Reacting to a partial estate

Use `--fail-on-partial` to distinguish a complete capture from a partial one:

```sh
vsfleet assessment run --all-contexts --fail-on-partial
case $? in
  0) ;;                                  # complete
  3) echo "some vCenters did not answer" ;;
  *) echo "the capture could not run" ; exit 1 ;;
esac
```

Without the flag, a partial capture exits 0.

### Capacity projection gate

Check both percentage and absolute free-space floors. This example writes
JSON and exits `2` if a datastore is projected to cross either floor within
30 days:

```sh
vsfleet assessment capacity latest --since 90d \
  --min-free 10 --min-free-bytes 500Gi \
  --fail-on-projection 30d -o json > capacity.json
case $? in
  0) echo "capacity floors are not projected within 30 days" ;;
  2) echo "capacity action is required"; exit 2 ;;
  *) echo "capacity assessment failed"; exit 1 ;;
esac
```

`--include-partial` includes incomplete captures and lowers projection
confidence. See [Capacity projection](assessments.md#capacity-attribution-and-projection).

## Customer enclave through SOCKS5

Resolve a hostname inside an isolated network through a bastion:

```sh
vsfleet context add \
  --name customer-a \
  --endpoint https://vcsa.customer-a.internal \
  --username operator@vsphere.local \
  --credential keyring:customer-a \
  --transport socks5 \
  --proxy-address 127.0.0.1:1080 \
  --remote-dns \
  --tls thumbprint
```

## Migration watcher

Use a short refresh interval to monitor VM placement changes interactively:

```sh
vsfleet --refresh 3s
```

For durable comparisons and scheduled checks, use
[assessment policies](assessments.md#compare-runs).
