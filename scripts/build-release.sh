#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Build six Linux raw binaries, install.sh, install-bundle.tar.gz and SHA256SUMS.
Usage: scripts/build-release.sh [--output DIRECTORY] [--skip-web]
Environment: VERSION, COMMIT, BUILD_TIME (RFC3339). Defaults use the current Git commit.
Install frontend dependencies first with npm --prefix web ci.
--skip-web requires an already built frontend in internal/webui/dist.
EOF
}

repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
output=$repo_dir/bin/releases
skip_web=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output)
      [[ $# -ge 2 && -n "$2" ]] || { usage >&2; exit 2; }
      output=$2; shift 2 ;;
    --skip-web) skip_web=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

cd "$repo_dir"
commit=${COMMIT:-$(git rev-parse HEAD)}
version=${VERSION:-main-${commit:0:12}}
build_time=${BUILD_TIME:-$(git show -s --format=%cI "$commit")}
[[ "$version" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] || { echo 'VERSION must contain only letters, numbers, dot, dash, underscore or plus.' >&2; exit 2; }
[[ "$commit" =~ ^[0-9a-fA-F]{40}$ ]] || { echo 'COMMIT must be a full 40-character Git commit.' >&2; exit 2; }
[[ "$build_time" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(Z|[+-][0-9]{2}:[0-9]{2})$ ]] || { echo 'BUILD_TIME must be RFC3339 without fractional seconds.' >&2; exit 2; }
if command -v sha256sum >/dev/null 2>&1; then
  checksum=(sha256sum)
elif command -v shasum >/dev/null 2>&1; then
  checksum=(shasum -a 256)
else
  echo 'sha256sum or shasum is required.' >&2
  exit 2
fi
[[ -f scripts/install.sh ]] || { echo 'scripts/install.sh is required for release bootstrap assets.' >&2; exit 2; }

if [[ "$skip_web" == false ]]; then
  make web-build
elif [[ ! -f internal/webui/dist/index.html ]]; then
  echo '--skip-web requires internal/webui/dist/index.html; run make web-build first.' >&2
  exit 2
fi

mkdir -p "$output"
output=$(cd "$output" && pwd)
temporary=$(mktemp -d "$output/.build.XXXXXX")
trap 'rm -rf -- "$temporary"' EXIT
ldflags="-s -w -X proxy-mono/internal/version.Version=$version -X proxy-mono/internal/version.Commit=$commit -X proxy-mono/internal/version.BuildTime=$build_time"
assets=()
for architecture in amd64 arm64 armv7; do
  goarch=$architecture
  target_env=(CGO_ENABLED=0 GOOS=linux GOAMD64=v1 GOARM64=v8.0)
  if [[ "$architecture" == armv7 ]]; then
    goarch=arm
    target_env+=(GOARM=7)
  fi
  target_env+=(GOARCH="$goarch")
  for product in proxy-server proxy-manage-server; do
    asset=${product}_linux_${architecture}
    printf 'Building %s (%s)\n' "$asset" "$version"
    env "${target_env[@]}" go build -tags stdjson,gjson -trimpath -ldflags "$ldflags" \
      -o "$temporary/$asset" "./cmd/$product"
    assets+=("$asset")
  done
done
COPYFILE_DISABLE=1 tar --format=ustar -czf "$temporary/install-bundle.tar.gz" \
  scripts/install-proxy-server.sh scripts/install-proxy-manage-server.sh \
  deploy/systemd/proxy-server.service deploy/systemd/proxy-manage-server.service
cp scripts/install.sh "$temporary/install.sh"
chmod 0755 "$temporary/install.sh"
assets+=(install-bundle.tar.gz install.sh)
(
  cd "$temporary"
  "${checksum[@]}" "${assets[@]}" > SHA256SUMS
  "${checksum[@]}" --check SHA256SUMS
)
for asset in "${assets[@]}" SHA256SUMS; do
  mv -f -- "$temporary/$asset" "$output/$asset"
done
printf 'Built six raw binaries, bootstrap assets and SHA256SUMS in %s\n' "$output"
