#!/usr/bin/env bash
# Fail when a value that must agree across release files has drifted.
#
# - Dependabot bumps the distroless digest in the Dockerfiles only, so the
#   OCI base-image annotation in .goreleaser.yaml must be checked against it.
# - release-please bumps the extra-files listed in release-please-config.json,
#   so every pinned image tag in the docs and the shipped CronJob must match
#   the released version in .release-please-manifest.json.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
failed=0

fail() {
  printf 'release pins: %s\n' "$*" >&2
  failed=1
}

base_ref() {
  sed -n 's/^FROM \([^ ]*\).*/\1/p' "$1" | head -n 1
}

product_base="$(base_ref Dockerfile)"
[[ "$product_base" == *@sha256:* ]] || fail "Dockerfile base image is not pinned by digest: $product_base"
base_name="${product_base%@*}"
base_digest="${product_base#*@}"

vcsim_base="$(base_ref Dockerfile.vcsim)"
[[ "$vcsim_base" == "$product_base" ]] || fail "Dockerfile.vcsim base $vcsim_base differs from Dockerfile base $product_base"

annotated_name="$(sed -n 's/.*org\.opencontainers\.image\.base\.name": "\([^"]*\)".*/\1/p' .goreleaser.yaml)"
annotated_digest="$(sed -n 's/.*org\.opencontainers\.image\.base\.digest": "\([^"]*\)".*/\1/p' .goreleaser.yaml)"
[[ "$annotated_name" == "$base_name" ]] || fail ".goreleaser.yaml base.name $annotated_name differs from Dockerfile $base_name"
[[ "$annotated_digest" == "$base_digest" ]] || fail ".goreleaser.yaml base.digest $annotated_digest differs from Dockerfile $base_digest"

version="$(sed -n 's/^ *"\." *: *"\([^"]*\)".*/\1/p' .release-please-manifest.json)"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+ ]] || fail "unreadable version in .release-please-manifest.json: $version"

mapfile -t extra_files < <(sed -n '/"extra-files"/,/]/s/^ *"\([^"]*\)",\{0,1\}$/\1/p' release-please-config.json)
((${#extra_files[@]})) || fail "release-please-config.json lists no extra-files"
for file in "${extra_files[@]}"; do
  [[ -f "$file" ]] || { fail "extra-file $file does not exist"; continue; }
  grep -q 'x-release-please' "$file" || fail "$file has no x-release-please marker, so release-please will not bump it"
done

# Every image tag pinned to an exact version anywhere in the shipped docs and
# manifests must be the current release, and must live in a bumped file.
while IFS=: read -r file line tag; do
  [[ "$tag" == "v$version" ]] || fail "$file:$line pins $tag, expected v$version"
  printf '%s\n' "${extra_files[@]}" | grep -qxF "$file" || fail "$file:$line pins an image version but is not a release-please extra-file"
done < <(grep -rnoE 'ghcr\.io/easonliuuuuu/vsfleet:v[0-9]+\.[0-9]+\.[0-9]+[^ ]*' README.md docs deploy |
  sed -E 's#^([^:]+):([0-9]+):ghcr\.io/easonliuuuuu/vsfleet:#\1:\2:#')

if ((failed)); then
  exit 1
fi
printf 'release pins agree: base %s, version v%s\n' "$base_digest" "$version"
