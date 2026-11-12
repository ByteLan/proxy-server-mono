#!/usr/bin/env bash
# Download a verified release and run its bundled installer. No repository checkout required.
set -euo pipefail
umask 077

usage() {
  cat <<'USAGE'
Usage: install.sh manager|node [--repo owner/repo] [--channel main|stable]
                  [--version TAG] [--token-file PATH] -- INSTALLER_ARGUMENTS

Default repository: ByteLan/proxy-server-mono; default channel: main.
Private repositories: GITHUB_TOKEN / GH_TOKEN, or a regular 0600 token file.
The manager installer needs --public-url; the node installer also needs manager,
public node address and pinned Hysteria2/Xray binaries, hashes and versions.
--binary is supplied by this bootstrap and must not be passed after --.
Run as root for installation, or set DESTDIR for staging without system changes.
Requires Python 3 and the dependencies of the selected bundled installer.
USAGE
}

if [[ $# -eq 0 || ${1:-} == --help || ${1:-} == -h ]]; then usage; exit 0; fi
role=$1; shift
case "$role" in manager|node) ;; *) usage >&2; exit 2 ;; esac
repo=ByteLan/proxy-server-mono channel=main version= token_file=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --) shift; break ;;
    --repo|--channel|--version|--token-file)
      [[ $# -ge 2 && -n $2 ]] || { echo "Missing value for $1" >&2; exit 2; }
      case "$1" in --repo) repo=$2 ;; --channel) channel=$2 ;; --version) version=$2 ;; --token-file) token_file=$2 ;; esac
      shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) echo "Unknown bootstrap argument: $1 (put installer arguments after --)" >&2; exit 2 ;;
  esac
done
for arg in "$@"; do
  case "$arg" in --binary|--binary=*) echo '--binary is supplied by the bootstrap.' >&2; exit 2 ;; esac
done
command -v python3 >/dev/null || { echo 'Python 3 is required during installation.' >&2; exit 1; }
if [[ -z ${DESTDIR:-} && $EUID -ne 0 ]]; then echo 'Run with sudo, or set DESTDIR for staging.' >&2; exit 1; fi
work_dir=$(mktemp -d /tmp/proxy-install.XXXXXXXX)
chmod 0700 "$work_dir"
trap 'rm -rf -- "$work_dir"' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

python3 - "$work_dir" "$role" "$repo" "$channel" "$version" "$token_file" <<'PYTHON'
import datetime
import hashlib
import json
import os
import pathlib
import platform
import re
import stat
import struct
import sys
import tarfile
import urllib.error
import urllib.parse
import urllib.request

WORK, ROLE, REPO, CHANNEL, TAG, TOKEN_FILE = sys.argv[1:]
MAX_ASSET = 128 * 1024 * 1024
API_DEFAULT = 'https://api.github.com'
# Test seam only: production always uses GitHub; overrides must be loopback HTTP(S).
API = os.environ.get('PROXY_GITHUB_API_URL', API_DEFAULT).rstrip('/')

def fail(message):
    raise RuntimeError(message)

def origin(url):
    parsed = urllib.parse.urlsplit(url)
    return parsed.scheme, parsed.hostname, parsed.port or (443 if parsed.scheme == 'https' else 80)

def checked_url(url):
    parsed = urllib.parse.urlsplit(url)
    if parsed.username or parsed.password or parsed.fragment:
        fail('Invalid download URL.')
    if parsed.scheme == 'https':
        return
    if API != API_DEFAULT and parsed.scheme == 'http' and parsed.hostname in ('localhost', '127.0.0.1', '::1'):
        return
    fail('Download redirects must use HTTPS.')

class SafeRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        checked_url(newurl)
        redirected = super().redirect_request(req, fp, code, msg, headers, newurl)
        if redirected is not None and origin(newurl) != origin(req.full_url):
            private_headers = {'authorization', 'cookie', 'proxy-authorization', 'referer'}
            for name in list(redirected.headers) + list(redirected.unredirected_hdrs):
                if name.lower() in private_headers:
                    redirected.remove_header(name)
        return redirected

OPENER = urllib.request.build_opener(SafeRedirect())

def request(path, accept='application/vnd.github+json'):
    url = API + path
    headers = {'Accept': accept, 'User-Agent': 'proxy-server-mono-installer', 'X-GitHub-Api-Version': '2022-11-28'}
    if TOKEN:
        headers['Authorization'] = 'Bearer ' + TOKEN
    try:
        return OPENER.open(urllib.request.Request(url, headers=headers), timeout=60)
    except urllib.error.HTTPError as error:
        if error.code in (401, 403, 404):
            fail('GitHub returned HTTP %s; check repository, tag, token access and API limits.' % error.code)
        fail('GitHub request failed with HTTP %s.' % error.code)
    except urllib.error.URLError:
        fail('Cannot reach the release download service.')

def read_limited(response, maximum):
    data = response.read(maximum + 1)
    if len(data) > maximum:
        fail('Release metadata exceeds the size limit.')
    return data

def api_json(path):
    with request(path) as response:
        return json.loads(read_limited(response, 8 * 1024 * 1024))

def published(release):
    value = release.get('published_at')
    if not isinstance(value, str):
        fail('Release has no publication timestamp.')
    return datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))

def select_release():
    path = '/repos/' + REPO + '/releases'
    if TAG:
        release = api_json(path + '/tags/' + urllib.parse.quote(TAG, safe=''))
        if release.get('tag_name') != TAG or release.get('draft'):
            fail('The requested tag has no published release.')
        return release
    if CHANNEL == 'stable':
        release = api_json(path + '/latest')
        if release.get('draft') or release.get('prerelease') or not str(release.get('tag_name', '')).startswith('v'):
            fail('No stable v* release is available.')
        return release
    candidates = []
    for page in range(1, 101):
        releases = api_json(path + '?per_page=100&page=' + str(page))
        if not isinstance(releases, list):
            fail('Invalid release list response.')
        candidates.extend(item for item in releases if isinstance(item, dict) and not item.get('draft')
                          and item.get('prerelease') and re.fullmatch(r'main-[0-9a-f]{12}', str(item.get('tag_name', ''))))
        if len(releases) < 100:
            break
    else:
        fail('Release history is too large; select an explicit --version tag.')
    if not candidates:
        fail('No published main-* prerelease is available.')
    return max(candidates, key=published)

def asset(release, name):
    matches = [item for item in release.get('assets', []) if item.get('name') == name and item.get('state') == 'uploaded']
    if len(matches) != 1 or not isinstance(matches[0].get('id'), int) or isinstance(matches[0]['id'], bool) or matches[0]['id'] <= 0:
        fail('Release must contain exactly one uploaded asset: ' + name)
    return matches[0]

def download(release, name, maximum=MAX_ASSET):
    info = asset(release, name)
    if info.get('size', 0) > maximum:
        fail('Release asset exceeds the size limit: ' + name)
    target = pathlib.Path(WORK) / name
    digest = hashlib.sha256()
    total = 0
    path = '/repos/' + REPO + '/releases/assets/' + str(info['id'])
    with request(path, 'application/octet-stream') as response, target.open('xb') as output:
        while True:
            chunk = response.read(65536)
            if not chunk:
                break
            total += len(chunk)
            if total > maximum:
                fail('Release asset exceeds the size limit: ' + name)
            digest.update(chunk)
            output.write(chunk)
    if total == 0:
        fail('Empty release asset: ' + name)
    return target, digest.hexdigest()

def manifest(path):
    result = {}
    for line in path.read_text(encoding='ascii').splitlines():
        if not line.strip():
            continue
        match = re.fullmatch(r'([0-9a-fA-F]{64}) [ *]([A-Za-z0-9_.-]+)', line)
        if not match or match[2] in result:
            fail('Malformed or duplicate SHA256SUMS entry.')
        result[match[2]] = match[1].lower()
    return result

def unpack(bundle):
    expected = {
        'scripts/install-proxy-server.sh', 'scripts/install-proxy-manage-server.sh',
        'deploy/systemd/proxy-server.service', 'deploy/systemd/proxy-manage-server.service',
    }
    seen = set()
    with tarfile.open(bundle, 'r|gz') as archive:
        for member in archive:
            if member.name not in expected or member.name in seen:
                fail('Installer bundle must contain exactly the four expected files.')
            if member.type not in (tarfile.REGTYPE, tarfile.AREGTYPE) or member.linkname or member.size > 1024 * 1024:
                fail('Installer bundle contains an unsafe member.')
            seen.add(member.name)
            output = pathlib.Path(WORK) / 'bundle' / member.name
            output.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            source = archive.extractfile(member)
            if source is None:
                fail('Installer bundle member is unreadable.')
            with source, output.open('xb') as stream:
                data = source.read(member.size + 1)
                if len(data) != member.size:
                    fail('Installer bundle member is truncated.')
                stream.write(data)
            output.chmod(0o700 if member.name.endswith('.sh') else 0o600)
    if seen != expected:
        fail('Installer bundle must contain exactly the four expected files.')

def validate_binary(path, arch):
    with path.open('rb') as binary:
        header = binary.read(64)
    elf_class, machine = {'amd64': (2, 62), 'arm64': (2, 183), 'armv7': (1, 40)}[arch]
    minimum_size = 64 if elf_class == 2 else 52
    if len(header) < minimum_size or header[:4] != b'\x7fELF' or header[4] != elf_class or header[5] != 1:
        fail('Downloaded program is not a little-endian ELF for this architecture.')
    elf_type, elf_machine = struct.unpack_from('<HH', header, 16)
    if elf_type not in (2, 3) or elf_machine != machine:
        fail('Downloaded ELF does not match this machine architecture.')

try:
    if not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', REPO):
        fail('Repository must have owner/repo form.')
    if CHANNEL not in ('main', 'stable'):
        fail('Channel must be main or stable.')
    parsed_api = urllib.parse.urlsplit(API)
    if API != API_DEFAULT and (parsed_api.hostname not in ('localhost', '127.0.0.1', '::1')
                               or parsed_api.scheme not in ('http', 'https') or parsed_api.path not in ('', '/')
                               or parsed_api.username or parsed_api.password or parsed_api.query or parsed_api.fragment):
        fail('The API test override must be a loopback origin.')
    checked_url(API)
    TOKEN = os.environ.get('GITHUB_TOKEN') or os.environ.get('GH_TOKEN') or ''
    if TOKEN_FILE:
        info = pathlib.Path(TOKEN_FILE).lstat()
        if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600 or info.st_size > 16384:
            fail('Token file must be a regular 0600 file, at most 16 KiB.')
        TOKEN = pathlib.Path(TOKEN_FILE).read_text(encoding='utf-8')
    TOKEN = TOKEN.strip()
    if TOKEN and (len(TOKEN) > 4096 or any(ord(character) < 33 or ord(character) > 126 for character in TOKEN)):
        fail('Invalid GitHub token format.')
    if TOKEN_FILE and not TOKEN:
        fail('Token file is empty.')
    if platform.system() != 'Linux' and not os.environ.get('DESTDIR'):
        fail('Installation is supported on Linux; use DESTDIR only for staging elsewhere.')
    arch = {'x86_64': 'amd64', 'amd64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64', 'armv7l': 'armv7', 'armv7': 'armv7'}.get(platform.machine().lower())
    if not arch:
        fail('No release asset is supported for this machine architecture.')
    name = ('proxy-manage-server' if ROLE == 'manager' else 'proxy-server') + '_linux_' + arch
    release = select_release()
    checksum_file, _ = download(release, 'SHA256SUMS', 1024 * 1024)
    checksums = manifest(checksum_file)
    for selected in (name, 'install-bundle.tar.gz'):
        if selected not in checksums:
            fail('SHA256SUMS is missing ' + selected)
        path, digest = download(release, selected)
        if digest != checksums[selected]:
            fail('SHA256 verification failed for ' + selected)
    validate_binary(pathlib.Path(WORK) / name, arch)
    unpack(pathlib.Path(WORK) / 'install-bundle.tar.gz')
    (pathlib.Path(WORK) / name).rename(pathlib.Path(WORK) / 'server-binary')
    (pathlib.Path(WORK) / 'server-binary').chmod(0o755)
    print('Verified %s from %s (%s).' % (name, REPO, release['tag_name']))
except (RuntimeError, OSError, ValueError, tarfile.TarError) as error:
    print('install.sh: ' + str(error), file=sys.stderr)
    sys.exit(1)
PYTHON

# GitHub credentials are only needed for download, not by the installed service.
unset GITHUB_TOKEN GH_TOKEN
installer=install-proxy-manage-server.sh
[[ $role == node ]] && installer=install-proxy-server.sh
bash "$work_dir/bundle/scripts/$installer" --binary "$work_dir/server-binary" "$@"
