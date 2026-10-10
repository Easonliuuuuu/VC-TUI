# Containers

Use `ghcr.io/easonliuuuuu/vsfleet` for unattended commands, assessments, and
JSON, CSV, or XLSX exports on Linux `amd64` and `arm64`. Install the native
binary for the interactive terminal UI.

## Tags and pinning

Stable releases publish an exact version tag such as <!-- x-release-please-start-version -->`v0.6.1`<!-- x-release-please-end -->,
a rolling minor tag, and `latest`. Prereleases publish only their exact tag;
there is no floating `v0` tag. Pin a version or digest in production:

<!-- x-release-please-start-version -->

```sh
docker pull ghcr.io/easonliuuuuu/vsfleet:v0.6.2
docker pull ghcr.io/easonliuuuuu/vsfleet@sha256:<manifest-digest>
```

The image runs as UID/GID `65532` with `vsfleet` as its entrypoint. It includes
system CA certificates and timezone data, but no shell or package manager.

## Configuration, secrets, and history

Mount a read-only configuration file and set `VSFLEET_CONFIG`. Put the history
database on a writable volume with `VSFLEET_HISTORY_DB`. Use a `file:`
credential so the password stays out of the process environment:

```toml
version = 1
current_context = "prod"

[[contexts]]
name = "prod"
endpoint = "https://vcsa.example.internal"
username = "administrator@vsphere.local"
credential = "file:/run/secrets/vsfleet-prod"

[contexts.transport]
type = "direct"
```

```sh
docker run --rm \
  --read-only \
  --user 65532:65532 \
  --mount type=bind,src="$PWD/config.toml",dst=/config/config.toml,readonly \
  --mount type=bind,src="$PWD/secrets/vsfleet-prod",dst=/run/secrets/vsfleet-prod,readonly \
  --mount type=bind,src="$PWD/vsfleet-data",dst=/data \
  --env VSFLEET_CONFIG=/config/config.toml \
  --env VSFLEET_HISTORY_DB=/data/history.db \
  ghcr.io/easonliuuuuu/vsfleet:v0.6.2 \
  assessment run --all-contexts --label nightly
```

Use `env:` credentials when the runtime injects secrets. Prefer `file:` on
shared hosts where process inspection could expose environment variables.
History contains inventory observations, never credentials or session cookies.

The data and export mounts must be writable by UID `65532` (for example,
`chown 65532:65532 vsfleet-data exports` on a bind-mounted Linux directory).

To export a capture, mount the same history volume and a writable output
directory. Export reads stored evidence without configuration or credentials:

```sh
docker run --rm \
  --mount type=bind,src="$PWD/vsfleet-data",dst=/data \
  --mount type=bind,src="$PWD/exports",dst=/exports \
  --env VSFLEET_HISTORY_DB=/data/history.db \
  ghcr.io/easonliuuuuu/vsfleet:v0.6.2 \
  assessment export --format csv --file /exports
```

## Private certificate authorities

For a vCenter signed by a private CA, mount a PEM bundle and set
`SSL_CERT_FILE`:

```sh
docker run --rm \
  --mount type=bind,src="$PWD/ca.pem",dst=/etc/vsfleet/ca.pem,readonly \
  --env SSL_CERT_FILE=/etc/vsfleet/ca.pem \
  ghcr.io/easonliuuuuu/vsfleet:v0.6.2 context list
```

You can also configure `thumbprint` or `insecure` TLS. Prefer a private CA or
thumbprint over disabling verification.

## Kubernetes

The [CronJob template](https://github.com/Easonliuuuuu/vsfleet/blob/main/deploy/kubernetes/cronjob.yaml)
uses a ConfigMap, Secret, and history volume. It runs as UID/GID `65532`, has a
read-only root filesystem, drops Linux capabilities, and sets `fsGroup: 65532`
for storage drivers that honor pod ownership. Pre-create `hostPath` directories
with UID/GID `65532`; use a managed StorageClass for production history.

Create the prerequisites in the namespace where the CronJob will run:

```sh
kubectl -n vsfleet create configmap vsfleet-config --from-file=config.toml
kubectl -n vsfleet create secret generic vsfleet-credentials \
  --from-file=vsfleet-prod=./secrets/vsfleet-prod
kubectl -n vsfleet create -f - <<'EOF'
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: vsfleet-data
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 1Gi
EOF
kubectl -n vsfleet apply -f deploy/kubernetes/cronjob.yaml
```

The Secret key must match the `file:` path: this example mounts `vsfleet-prod`
at `/run/secrets/vsfleet-prod`. For a private CA, mount its PEM file and set
`SSL_CERT_FILE`. [Kubernetes tests](testing.md#kubernetes-end-to-end) exercise
the template against synthetic services, never production vCenters.

## Signature verification

Release images are signed with keyless Cosign through GitHub Actions OIDC and
carry a BuildKit-generated SBOM. Verify a version tag with the release workflow
identity:

```sh
cosign verify ghcr.io/easonliuuuuu/vsfleet:v0.6.2 \
  --certificate-identity-regexp '^https://github.com/Easonliuuuuu/vsfleet/.github/workflows/release.yml@refs/heads/main$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

<!-- x-release-please-end -->

## Runtime limitations

The stock image has no OS keyring, browser, SSH client, or `exec:` credential
helper. Use `env:` or `file:`, or add a helper in a derived image. Preserve the
non-root user and keep secrets out of image layers and metadata.
