#!/usr/bin/env bash
set -euo pipefail
umask 077

usage() {
  cat <<'EOF'
Install a locally built manager binary on a Linux systemd host.
Usage: install-proxy-manage-server.sh --binary PATH --public-url URL [--listen ADDRESS]
  [--tls-cert CERT_PEM --tls-key KEY_PEM]
  [--http-sub-listen ADDRESS --http-sub-public-url URL]
Optional environment: ADMIN_TOKEN, ENROLL_TOKEN, MASTER_KEY (base64 32 bytes).
DESTDIR=/staging/path stages files without creating users or calling systemctl.
An existing manager.env is preserved. Tokens are never printed.
EOF
}

binary= public_url= listen=127.0.0.1:8080 tls_cert= tls_key= http_sub_listen= http_sub_public_url=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary|--public-url|--listen|--tls-cert|--tls-key|--http-sub-listen|--http-sub-public-url)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      case "$1" in
        --binary) binary=$2 ;;
        --public-url) public_url=${2%/} ;;
        --listen) listen=$2 ;;
        --tls-cert) tls_cert=$2 ;;
        --tls-key) tls_key=$2 ;;
        --http-sub-listen) http_sub_listen=$2 ;;
        --http-sub-public-url) http_sub_public_url=${2%/} ;;
      esac
      shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done
[[ -f "$binary" && -x "$binary" ]] || { echo 'Provide an executable --binary.' >&2; exit 2; }
url_pattern='^https?://(\[[0-9A-Fa-f:]+\]|[A-Za-z0-9.-]+)(:[0-9]{1,5})?$'
listen_pattern='^(\[[0-9A-Fa-f:]+\]|[A-Za-z0-9.-]*):[0-9]{1,5}$'
[[ "$public_url" =~ $url_pattern ]] || { echo 'Provide an http(s) origin with host and optional port, without path, query, credentials or spaces.' >&2; exit 2; }
[[ "$listen" =~ $listen_pattern ]] || { echo 'Invalid listen host:port address.' >&2; exit 2; }
if [[ -n "$http_sub_listen" || -n "$http_sub_public_url" ]]; then
  [[ -n "$http_sub_listen" && -n "$http_sub_public_url" ]] || { echo '--http-sub-listen and --http-sub-public-url must be provided together.' >&2; exit 2; }
  [[ "$http_sub_listen" =~ $listen_pattern && "$http_sub_listen" != "$listen" ]] || { echo 'Invalid or conflicting --http-sub-listen host:port address.' >&2; exit 2; }
  [[ "$http_sub_public_url" =~ $url_pattern ]] || { echo 'Invalid --http-sub-public-url origin.' >&2; exit 2; }
fi
if [[ -n "$tls_cert" || -n "$tls_key" ]]; then
  [[ -f "$tls_cert" && -f "$tls_key" ]] || { echo '--tls-cert and --tls-key must both point to readable PEM files.' >&2; exit 2; }
fi
if [[ -n ${MASTER_KEY:-} ]]; then
  [[ "$MASTER_KEY" =~ ^[A-Za-z0-9+/]{43}=$ ]] || { echo 'MASTER_KEY must be base64 encoding of 32 bytes.' >&2; exit 2; }
fi
stage=${DESTDIR:-}
[[ -z "$stage" || "$stage" == /* ]] || { echo 'DESTDIR must be absolute.' >&2; exit 2; }
if [[ -z "$stage" ]]; then
  [[ $(uname -s) == Linux && $(id -u) -eq 0 ]] || { echo 'Run as root on Linux, or set DESTDIR for staging.' >&2; exit 2; }
fi
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
install -d -m 0755 "$stage/opt/proxy-manage-server/bin" "$stage/etc/systemd/system"
install -d -m 0750 "$stage/etc/proxy-manage-server"
install -d -m 0700 "$stage/var/lib/proxy-manage-server"
install -m 0755 "$binary" "$stage/opt/proxy-manage-server/bin/proxy-manage-server"
install -m 0644 "$script_dir/../deploy/systemd/proxy-manage-server.service" "$stage/etc/systemd/system/proxy-manage-server.service"
if [[ -n "$tls_cert" ]]; then
  install -m 0640 "$tls_cert" "$stage/etc/proxy-manage-server/tls.crt"
  install -m 0640 "$tls_key" "$stage/etc/proxy-manage-server/tls.key"
fi
env_file=$stage/etc/proxy-manage-server/manager.env
if [[ ! -e "$env_file" ]]; then
  admin_token=${ADMIN_TOKEN:-$(openssl rand -hex 32)}
  enroll_token=${ENROLL_TOKEN:-$(openssl rand -hex 32)}
  [[ "$admin_token" =~ ^[A-Za-z0-9._~+/=-]{32,}$ && "$enroll_token" =~ ^[A-Za-z0-9._~+/=-]{32,}$ ]] || { echo 'Tokens must have at least 32 characters from A-Z/a-z/0-9/._~+/=-.' >&2; exit 2; }
  [[ "$admin_token" != "$enroll_token" ]] || { echo 'ADMIN_TOKEN and ENROLL_TOKEN must be different.' >&2; exit 2; }
  {
    printf 'LISTEN=%s\nDATA_DIR=/var/lib/proxy-manage-server\nPUBLIC_URL=%s\n' "$listen" "$public_url"
    printf 'ADMIN_TOKEN=%s\nENROLL_TOKEN=%s\n' "$admin_token" "$enroll_token"
    if [[ -n ${MASTER_KEY:-} ]]; then
      printf 'MASTER_KEY=%s\n' "$MASTER_KEY"
    fi
    if [[ -n "$tls_cert" ]]; then
      printf 'TLS_CERT_FILE=/etc/proxy-manage-server/tls.crt\nTLS_KEY_FILE=/etc/proxy-manage-server/tls.key\n'
    fi
    if [[ -n "$http_sub_listen" ]]; then
      printf 'HTTP_SUB_LISTEN=%s\nHTTP_SUB_PUBLIC_URL=%s\n' "$http_sub_listen" "$http_sub_public_url"
    fi
  } > "$env_file"
  chmod 0600 "$env_file"
else
  echo 'Preserved existing manager.env; edit it separately to change settings.'
  if [[ -n "$tls_cert" ]]; then
    temp_env=$(mktemp "$env_file.XXXXXX")
    awk '!/^TLS_CERT_FILE=/ && !/^TLS_KEY_FILE=/' "$env_file" > "$temp_env"
    printf 'TLS_CERT_FILE=/etc/proxy-manage-server/tls.crt\nTLS_KEY_FILE=/etc/proxy-manage-server/tls.key\n' >> "$temp_env"
    chmod 0600 "$temp_env"
    mv "$temp_env" "$env_file"
  fi
  if [[ -n "$http_sub_listen" ]]; then
    temp_env=$(mktemp "$env_file.XXXXXX")
    awk '!/^HTTP_SUB_LISTEN=/ && !/^HTTP_SUB_PUBLIC_URL=/' "$env_file" > "$temp_env"
    printf 'HTTP_SUB_LISTEN=%s\nHTTP_SUB_PUBLIC_URL=%s\n' "$http_sub_listen" "$http_sub_public_url" >> "$temp_env"
    chmod 0600 "$temp_env"
    mv "$temp_env" "$env_file"
  fi
fi
if [[ -n "$stage" ]]; then
  echo 'Staged manager files. User creation, service startup and key/database initialization were skipped.'
  exit 0
fi
if ! id proxy-manager >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/proxy-manage-server --shell /usr/sbin/nologin proxy-manager
fi
chown -R proxy-manager:proxy-manager /var/lib/proxy-manage-server
chown root:proxy-manager /etc/proxy-manage-server
for tls_file in /etc/proxy-manage-server/tls.crt /etc/proxy-manage-server/tls.key; do
  if [[ -f "$tls_file" ]]; then
    chown root:proxy-manager "$tls_file"
    chmod 0640 "$tls_file"
  fi
done
systemctl daemon-reload
systemctl enable proxy-manage-server.service
systemctl restart proxy-manage-server.service
echo 'Installed manager. Admin/enrollment tokens are in /etc/proxy-manage-server/manager.env (root-only).'
echo 'Use manager TLS or an existing HTTPS reverse proxy before enrolling remote nodes.'
