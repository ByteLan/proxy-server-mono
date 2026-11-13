#!/usr/bin/env bash
set -euo pipefail
umask 077

usage() {
  cat <<'EOF'
Install locally built agent and (for fresh nodes) downloaded protocol cores on a Linux systemd host.
Usage (fresh host, managed child cores):
  install-proxy-server.sh --binary PATH --manager URL --server PUBLIC_HOST --name NAME \
    --hysteria-binary PATH --hysteria-sha256 HASH --core-version hysteria=VERSION \
    --xray-binary PATH --xray-sha256 HASH --core-version xray=VERSION \
    [--hy2-port 443] [--vless-port 443] [--ca-file PATH]

Usage (existing host cores, companion/adopt mode):
  install-proxy-server.sh --binary PATH --adopt-existing --manager URL --server PUBLIC_HOST --name NAME \
    [--hysteria-config PATH] [--hysteria-unit hysteria.service] \
    [--xray-config PATH] [--xray-unit xray.service] [--ca-file PATH]

Set ENROLL_TOKEN in the environment; it is saved in a root-only environment file.
No downloads or 'latest' versions are accepted. Verify expected checksums through a trusted channel.
DESTDIR stages files and skips config initialization, enrollment, users and systemctl.
Existing node.env and config.json are preserved on upgrades.
EOF
}

binary= manager= server= name= hysteria_binary= hysteria_sha256= xray_binary= xray_sha256= ca_file=
hysteria_version= xray_version= hy2_port=443 vless_port=443
adopt_existing=0 hy2_port_set=0 vless_port_set=0
hysteria_config= hysteria_unit= xray_config= xray_unit=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --adopt-existing)
      adopt_existing=1
      shift ;;
    --binary|--manager|--server|--name|--hysteria-binary|--hysteria-sha256|--xray-binary|--xray-sha256|--core-version|--hy2-port|--vless-port|--ca-file|--hysteria-config|--hysteria-unit|--xray-config|--xray-unit)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      case "$1" in
        --binary) binary=$2 ;; --manager) manager=${2%/} ;; --server) server=$2 ;; --name) name=$2 ;;
        --hysteria-binary) hysteria_binary=$2 ;; --hysteria-sha256) hysteria_sha256=$2 ;;
        --xray-binary) xray_binary=$2 ;; --xray-sha256) xray_sha256=$2 ;;
        --hy2-port) hy2_port=$2; hy2_port_set=1 ;; --vless-port) vless_port=$2; vless_port_set=1 ;;
        --ca-file) ca_file=$2 ;;
        --hysteria-config) hysteria_config=$2 ;; --hysteria-unit) hysteria_unit=$2 ;;
        --xray-config) xray_config=$2 ;; --xray-unit) xray_unit=$2 ;;
        --core-version)
          case "$2" in
            hysteria=*) hysteria_version=${2#hysteria=} ;;
            xray=*) xray_version=${2#xray=} ;;
            *) echo 'Use --core-version hysteria=VERSION and --core-version xray=VERSION.' >&2; exit 2 ;;
          esac ;;
      esac
      shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done
[[ -f "$binary" && -x "$binary" ]] || { echo 'Agent binary path must be an executable file.' >&2; exit 2; }
manager_pattern='^https://(\[[0-9A-Fa-f:]+\]|[A-Za-z0-9.-]+)(:[0-9]{1,5})?$'
[[ "$manager" =~ $manager_pattern ]] || { echo 'Provide an HTTPS manager origin without path, query or credentials (use direct init for loopback development).' >&2; exit 2; }
[[ -z "$ca_file" || -f "$ca_file" ]] || { echo '--ca-file must point to a readable PEM CA file.' >&2; exit 2; }
[[ "$server" =~ ^[A-Za-z0-9.:-]+$ && -n "$name" && "$name" != *$'\n'* ]] || { echo 'Provide a public host/IP and single-line node name.' >&2; exit 2; }
verify_checksum() {
  local file=$1 expected=$2 actual
  [[ "$expected" =~ ^[A-Fa-f0-9]{64}$ ]] || { echo 'Each core requires a 64-digit SHA256 checksum.' >&2; exit 2; }
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$file" | awk '{print $1}')
  else
    actual=$(shasum -a 256 "$file" | awk '{print $1}')
  fi
  [[ $(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]') == $(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]') ]] || { echo "Checksum mismatch: $file" >&2; exit 2; }
}
if (( adopt_existing )); then
  if [[ -n "$hysteria_binary$hysteria_sha256$xray_binary$xray_sha256$hysteria_version$xray_version" ]] || (( hy2_port_set || vless_port_set )); then
    echo '--adopt-existing cannot be combined with core binary, checksum, version or port-generation flags.' >&2
    exit 2
  fi
  for unit in "$hysteria_unit" "$xray_unit"; do
    [[ -z "$unit" || "$unit" =~ ^[A-Za-z0-9@_][A-Za-z0-9@._-]*$ ]] || { echo 'Invalid systemd unit name.' >&2; exit 2; }
  done
else
  if [[ -n "$hysteria_config$hysteria_unit$xray_config$xray_unit" ]]; then
    echo '--hysteria-config/--hysteria-unit/--xray-config/--xray-unit require --adopt-existing.' >&2
    exit 2
  fi
  for file in "$hysteria_binary" "$xray_binary"; do
    [[ -f "$file" && -x "$file" ]] || { echo 'Agent and both core binary paths must be executable files.' >&2; exit 2; }
  done
  for port in "$hy2_port" "$vless_port"; do
    [[ "$port" =~ ^[0-9]{1,5}$ ]] && (( 10#$port >= 1 && 10#$port <= 65535 )) || { echo 'Ports must be 1..65535.' >&2; exit 2; }
  done
  hy2_port=$((10#$hy2_port))
  vless_port=$((10#$vless_port))
  for version in "$hysteria_version" "$xray_version"; do
    [[ "$version" =~ ^[vV]?[0-9][A-Za-z0-9._+-]*$ && "$version" != *latest* ]] || { echo 'Choose an explicit fixed version for each core.' >&2; exit 2; }
  done
  verify_checksum "$hysteria_binary" "$hysteria_sha256"
  verify_checksum "$xray_binary" "$xray_sha256"
fi
stage=${DESTDIR:-}
[[ -z "$stage" || "$stage" == /* ]] || { echo 'DESTDIR must be absolute.' >&2; exit 2; }
if [[ -z "$stage" ]]; then
  [[ $(uname -s) == Linux && $(id -u) -eq 0 ]] || { echo 'Run as root on Linux, or set DESTDIR for staging.' >&2; exit 2; }
fi
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
env_file=$stage/etc/proxy-server/node.env
enroll_token=${ENROLL_TOKEN:-${PROXY_ENROLL_TOKEN:-}}
if [[ ! -e "$env_file" ]]; then
  [[ "$enroll_token" =~ ^[A-Za-z0-9._~+/=-]{32,}$ ]] || { echo 'Set ENROLL_TOKEN to the manager enrollment token (at least 32 characters).' >&2; exit 2; }
fi
install -d -m 0755 "$stage/opt/proxy-server/bin" "$stage/etc/systemd/system"
install -d -m 0700 "$stage/etc/proxy-server"
install -m 0755 "$binary" "$stage/opt/proxy-server/bin/proxy-server"
install -m 0644 "$script_dir/../deploy/systemd/proxy-server.service" "$stage/etc/systemd/system/proxy-server.service"
if (( ! adopt_existing )); then
  install -m 0755 "$hysteria_binary" "$stage/opt/proxy-server/bin/hysteria"
  install -m 0755 "$xray_binary" "$stage/opt/proxy-server/bin/xray"
  printf 'hysteria=%s\nhysteria_sha256=%s\nxray=%s\nxray_sha256=%s\n' "$hysteria_version" "$hysteria_sha256" "$xray_version" "$xray_sha256" > "$stage/opt/proxy-server/core-versions"
fi
if [[ ! -e "$env_file" ]]; then
  printf 'ENROLL_TOKEN=%s\n' "$enroll_token" > "$env_file"
  chmod 0600 "$env_file"
fi
ca_args=()
if [[ -n "$ca_file" ]]; then
  install -m 0600 "$ca_file" "$stage/etc/proxy-server/manager-ca.pem"
  ca_args=(--ca-file /etc/proxy-server/manager-ca.pem)
fi
if [[ -n "$stage" ]]; then
  echo 'Staged node files. Config initialization, enrollment, user creation and service startup were skipped.'
  echo 'The staged tree is for installation inspection, not an enrolled/runnable node.'
  exit 0
fi
if ! id proxy-node >/dev/null 2>&1; then
  useradd --system --home-dir /etc/proxy-server --shell /usr/sbin/nologin proxy-node
fi
if [[ ! -e /etc/proxy-server/config.json ]]; then
  if (( adopt_existing )); then
    adopt_args=()
    [[ -n "$hysteria_config" ]] && adopt_args+=(--hysteria-config "$hysteria_config")
    [[ -n "$hysteria_unit" ]] && adopt_args+=(--hysteria-unit "$hysteria_unit")
    [[ -n "$xray_config" ]] && adopt_args+=(--xray-config "$xray_config")
    [[ -n "$xray_unit" ]] && adopt_args+=(--xray-unit "$xray_unit")
    /opt/proxy-server/bin/proxy-server adopt --config /etc/proxy-server/config.json \
      --manager "$manager" --server "$server" --name "$name" "${adopt_args[@]}" "${ca_args[@]}"
  else
    /opt/proxy-server/bin/proxy-server init --config /etc/proxy-server/config.json \
      --manager "$manager" --server "$server" --name "$name" --hy2-port "$hy2_port" --vless-port "$vless_port" \
      --hysteria-bin /opt/proxy-server/bin/hysteria --xray-bin /opt/proxy-server/bin/xray "${ca_args[@]}"
  fi
else
  echo 'Preserved existing node configuration; changes to name/address/ports require an explicit config edit.'
fi
chown -R proxy-node:proxy-node /etc/proxy-server
chown root:root /etc/proxy-server/node.env
systemctl daemon-reload
systemctl enable proxy-server.service
systemctl restart proxy-server.service
echo 'Installed node. Check journalctl -u proxy-server for registration and core status.'
