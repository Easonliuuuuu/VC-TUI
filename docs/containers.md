# Containers

The official image is published at
`ghcr.io/easonliuuuuu/vsfleet` for Linux `amd64` and `arm64`. It is an
automation image for unattended commands, assessments, and JSON, CSV, or XLSX
exports. Native binaries remain the recommended install for the interactive
terminal UI.

After the first release, set the GHCR package visibility to **Public** and
confirm that it is linked to this repository in the package settings. That is
a one-time registry setting and is intentionally outside the release workflow.

## Tags and pinning

Each release publishes:

- an immutable version tag such as <!-- x-release-please-start-version -->`v0.6.1`<!-- x-release-please-end -->;
- a rolling minor tag, the version without its patch number; and
- `latest` for stable releases only.

Prereleases receive only their exact version tag. There is no floating `v0`
tag while the project is pre-1.0. Pin an immutable version or digest in
production:

<!-- x-release-please-start-version -->

```sh
docker pull ghcr.io/easonliuuuuu/vsfleet:v0.6.1
docker pull ghcr.io/easonliuuuuu/vsfleet@sha256:<manifest-digest>
```

The image runs as UID and GID `65532` and has the `vsfleet` binary as its
vector-form entrypoint. The distroless base includes system CA certificates
and timezone data, but intentionally has no shell or package manager.

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
  ghcr.io/easonliuuuuu/vsfleet:v0.6.1 \
  assessment run --all-contexts --label nightly
```

`env:` credentials are also supported when the container runtime injects the
secret. `file:` is usually preferable on shared hosts because environment
variables can be visible through process inspection. The mounted history
database contains inventory observations but never credentials or session
cookies.

The data and export mounts must be writable by UID `65532` (for example,
`chown 65532:65532 vsfleet-data exports` on a bind-mounted Linux directory).

Exports need a writable destination. Mount an output directory and pass an
explicit file or directory with `--file`:

```sh
docker run --rm \
  --mount type=bind,src="$PWD/config.toml",dst=/config/config.toml,readonly \
  --mount type=bind,src="$PWD/exports",dst=/exports \
  --env VSFLEET_CONFIG=/config/config.toml \
  ghcr.io/easonliuuuuu/vsfleet:v0.6.1 \
  assessment export --format csv --file /exports
```

## Private certificate authorities

For a vCenter signed by a private CA, mount a PEM bundle and set
`SSL_CERT_FILE`:

```sh
docker run --rm \
  --mount type=bind,src="$PWD/ca.pem",dst=/etc/vsfleet/ca.pem,readonly \
  --env SSL_CERT_FILE=/etc/vsfleet/ca.pem \
  ghcr.io/easonliuuuuu/vsfleet:v0.6.1 context list
```

The alternative `thumbprint` and `insecure` TLS policies remain available in
the configuration. Prefer a private CA or thumbprint over disabling
verification.

## Kubernetes

The maintained [CronJob template](https://github.com/Easonliuuuuu/vsfleet/blob/main/deploy/kubernetes/cronjob.yaml)
uses the same ConfigMap, Secret, and history-volume contract as the container
examples. It runs as the image's unprivileged UID/GID (`65532`), has a
read-only root filesystem, drops Linux capabilities, and sets `fsGroup: 65532`
so the mounted SQLite database is writable on storage drivers that honor pod
ownership. For `hostPath` volumes, pre-create the directory with UID/GID
`65532`; use a managed StorageClass for production history data.

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

The Secret key must match the `file:` path in `config.toml`; the command above
mounts `vsfleet-prod` at `/run/secrets/vsfleet-prod`. Set `SSL_CERT_FILE` and
mount another PEM file when the vCenter uses a private CA. The CronJob template
is exercised on every pull request against two private, synthetic `vcsim`
Services in an ephemeral Kubernetes cluster; it never contacts production
vCenters.

## Signature verification

Release images are signed with keyless Cosign through GitHub Actions OIDC and
carry a BuildKit-generated SBOM. Verify a version tag with the release workflow
identity:

```sh
cosign verify ghcr.io/easonliuuuuu/vsfleet:v0.6.1 \
  --certificate-identity-regexp '^https://github.com/Easonliuuuuu/vsfleet/.github/workflows/release.yml@refs/heads/main$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

<!-- x-release-please-end -->

## Runtime limitations

The stock image deliberately omits workstation integrations. Keyring access,
browser and SSH handoffs, and `exec:` credential helpers are unavailable in
the distroless image. Use `env:` or `file:` credentials, or build a derived
image that adds the specific helper your environment requires. A derived image
should preserve the non-root user and avoid putting secrets in layers or image
metadata.
