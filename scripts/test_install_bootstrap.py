"""Offline bootstrap checks: real installer staging plus private-release HTTP fixtures.

Run: python3 -m unittest discover -s scripts -p 'test_install_bootstrap.py'
No GitHub account, root installation, service changes or third-party packages needed.
"""

import gzip
import hashlib
import http.server
import io
import json
import os
from pathlib import Path
import platform
import struct
import subprocess
import tarfile
import tempfile
import threading
import unittest


ROOT = Path(__file__).resolve().parent.parent
FILES = (
    'scripts/install-proxy-server.sh',
    'scripts/install-proxy-manage-server.sh',
    'deploy/systemd/proxy-server.service',
    'deploy/systemd/proxy-manage-server.service',
)
BINARY = b'#!/bin/sh\nexit 0\n'
READ_TOKEN = 'fixture-read-only-token'


def elf(arch):
    header = bytearray(64)
    header[:7] = b'\x7fELF' + bytes([1 if arch == 'armv7' else 2, 1, 1])
    struct.pack_into('<HHI', header, 16, 2, {'amd64': 62, 'arm64': 183, 'armv7': 40}[arch], 1)
    return bytes(header)


class BootstrapTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.fixture = {}

        class Storage(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                cls.fixture['storage_auth'].append(self.headers.get('Authorization'))
                data = cls.assets().get(self.path.removeprefix('/files/'))
                if data is None:
                    self.send_error(404)
                    return
                self.send_response(200)
                self.end_headers()
                self.wfile.write(data)

        class API(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                cls.fixture['api_auth'].append(self.headers.get('Authorization'))
                if self.headers.get('Authorization') != 'Bearer ' + READ_TOKEN:
                    self.send_error(401)
                    return
                data = cls.assets()
                names = list(data)

                def release(tag, published, prerelease=True, draft=False):
                    return {'tag_name': tag, 'published_at': published, 'draft': draft,
                            'prerelease': prerelease, 'assets': [
                                {'name': name, 'id': index + 1, 'state': 'uploaded', 'size': len(data[name])}
                                for index, name in enumerate(names)]}

                if '/assets/' in self.path:
                    try:
                        name = names[int(self.path.rsplit('/', 1)[1]) - 1]
                    except (ValueError, IndexError):
                        self.send_error(404)
                        return
                    self.send_response(302)
                    # A different port is a different origin: never forward the PAT.
                    self.send_header('Location', 'http://127.0.0.1:%s/files/%s' % (cls.storage.server_port, name))
                    self.end_headers()
                    return
                if '/tags/' in self.path:
                    body = release(self.path.rsplit('/', 1)[1], '2026-11-12T00:00:00Z')
                elif self.path.endswith('/latest'):
                    body = release('v1.0.0', '2026-11-12T00:00:00Z', False)
                else:
                    body = [release('main-111111111111', '2026-11-11T00:00:00Z'),
                            release('main-333333333333', '2026-11-13T00:00:00Z', draft=True),
                            release('main-222222222222', '2026-11-12T00:00:00Z')]
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.end_headers()
                self.wfile.write(json.dumps(body).encode())

        cls.storage = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Storage)
        cls.api = http.server.ThreadingHTTPServer(('127.0.0.1', 0), API)
        for server in (cls.storage, cls.api):
            threading.Thread(target=server.serve_forever, daemon=True).start()

    @classmethod
    def tearDownClass(cls):
        for server in (cls.storage, cls.api):
            server.shutdown()
            server.server_close()

    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix='proxy-bootstrap-test-')
        self.addCleanup(self.directory.cleanup)
        self.base = Path(self.directory.name)
        self.token_file = self.base / 'token'
        self.token_file.write_text(READ_TOKEN)
        self.token_file.chmod(0o600)
        self.core = self.base / 'pinned core'
        self.core.write_bytes(BINARY)
        self.core.chmod(0o755)
        self.fixture.clear()
        self.fixture.update(bad_hash=False, bad_tar=False, bad_path=False, wrong_arch=False,
                            capture=False, api_auth=[], storage_auth=[])

    @classmethod
    def bundle(cls):
        buffer = io.BytesIO()
        with gzip.GzipFile(fileobj=buffer, mode='wb', mtime=0) as gzip_stream, tarfile.open(fileobj=gzip_stream, mode='w') as archive:
            for name in FILES:
                data = (ROOT / name).read_bytes()
                if cls.fixture['capture'] and name.endswith('.sh'):
                    data = b'#!/bin/bash\n[[ -z ${GITHUB_TOKEN:-}${GH_TOKEN:-} ]] || exit 81\nprintf "%s\\0" "$@" > "$CAPTURE_INSTALLER_ARGS"\n'
                info = tarfile.TarInfo(name)
                info.size = len(data)
                if cls.fixture['bad_tar'] and name.endswith('proxy-server.service'):
                    info.type = tarfile.SYMTYPE
                    info.linkname = '/etc/passwd'
                    info.size = 0
                    archive.addfile(info)
                else:
                    if cls.fixture['bad_path'] and name.endswith('proxy-server.service'):
                        info.name = '../../outside'
                    archive.addfile(info, io.BytesIO(data))
        return buffer.getvalue()

    @classmethod
    def assets(cls):
        data = {('%s_linux_%s' % (server, arch)): elf('armv7' if arch != 'armv7' else 'amd64') if cls.fixture['wrong_arch'] else elf(arch)
                for server in ('proxy-server', 'proxy-manage-server')
                for arch in ('amd64', 'arm64', 'armv7')}
        data['install-bundle.tar.gz'] = cls.bundle()
        data['install.sh'] = (ROOT / 'scripts/install.sh').read_bytes()
        sums = ''.join(hashlib.sha256(payload).hexdigest() + '  ' + name + '\n' for name, payload in data.items())
        if cls.fixture['bad_hash']:
            for arch in ('amd64', 'arm64', 'armv7'):
                sums = sums.replace(hashlib.sha256(elf(arch)).hexdigest(), '0' * 64)
        data['SHA256SUMS'] = sums.encode()
        return data

    def run_bootstrap(self, role='manager', options=(), failure=False, extra=(), env_token=False, token_value=None):
        stage = self.base / ('stage-' + str(len(list(self.base.iterdir()))))
        environment = dict(os.environ)
        environment.pop('GITHUB_TOKEN', None)
        environment.pop('GH_TOKEN', None)
        environment.update(DESTDIR=str(stage), PROXY_GITHUB_API_URL='http://127.0.0.1:%s' % self.api.server_port,
                           ENROLL_TOKEN='fixture-enrollment-12345678901234567890',
                           CAPTURE_INSTALLER_ARGS=str(self.base / 'installer-args'))
        command = ['bash', str(ROOT / 'scripts/install.sh'), role, *options]
        if env_token:
            environment['GITHUB_TOKEN'] = READ_TOKEN if token_value is None else token_value
        else:
            command += ['--token-file', str(self.token_file)]
        command += ['--']
        if role == 'manager':
            command += ['--public-url', 'https://manager.example.invalid']
        else:
            digest = hashlib.sha256(BINARY).hexdigest()
            command += ['--manager', 'https://manager.example.invalid', '--server', 'node.example.invalid',
                        '--name', 'test node', '--hy2-port', '8443', '--vless-port', '9443',
                        '--hysteria-binary', str(self.core), '--hysteria-sha256', digest,
                        '--core-version', 'hysteria=v1.0.0', '--xray-binary', str(self.core),
                        '--xray-sha256', digest, '--core-version', 'xray=v1.0.0']
        command += list(extra)
        result = subprocess.run(command, env=environment, capture_output=True, text=True, timeout=30)
        self.assertNotIn(READ_TOKEN, result.stdout + result.stderr)
        if failure:
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertFalse((stage / 'opt').exists())
        else:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            if not self.fixture['capture']:
                product = 'proxy-manage-server' if role == 'manager' else 'proxy-server'
                arch = {'x86_64': 'amd64', 'amd64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64', 'armv7l': 'armv7', 'armv7': 'armv7'}[platform.machine().lower()]
                self.assertEqual((stage / 'opt' / product / 'bin' / product).read_bytes(), elf(arch))
                env_file = stage / 'etc' / product / ('manager.env' if role == 'manager' else 'node.env')
                self.assertNotIn(READ_TOKEN, env_file.read_text())
        return result

    def test_manager_staging_main_and_private_redirect(self):
        self.assertIn('main-222222222222', self.run_bootstrap().stdout)
        self.assertEqual(set(self.fixture['api_auth']), {'Bearer ' + READ_TOKEN})
        self.assertTrue(self.fixture['storage_auth'])
        self.assertEqual(set(self.fixture['storage_auth']), {None})

    def test_node_staging(self):
        self.run_bootstrap(role='node')

    def test_stable_and_pinned_release(self):
        self.assertIn('v1.0.0', self.run_bootstrap(options=['--channel', 'stable']).stdout)
        self.assertIn('main-111111111111', self.run_bootstrap(options=['--version', 'main-111111111111']).stdout)

    def test_mismatched_checksum_does_not_install(self):
        self.fixture['bad_hash'] = True
        self.assertIn('SHA256 verification failed', self.run_bootstrap(failure=True).stderr)

    def test_unsafe_bundle_does_not_install(self):
        for unsafe in ('bad_tar', 'bad_path'):
            with self.subTest(unsafe=unsafe):
                self.fixture[unsafe] = True
                self.run_bootstrap(failure=True)
                self.fixture[unsafe] = False

    def test_token_file_must_be_private(self):
        self.token_file.chmod(0o644)
        self.assertIn('0600', self.run_bootstrap(failure=True).stderr)
        self.assertEqual(self.fixture['api_auth'], [])

    def test_wrong_architecture_does_not_install(self):
        self.fixture['wrong_arch'] = True
        self.assertIn('ELF', self.run_bootstrap(failure=True).stderr)

    def test_control_character_token_is_rejected_without_disclosure(self):
        result = self.run_bootstrap(failure=True, env_token=True, token_value=READ_TOKEN + '\x01')
        self.assertIn('Invalid GitHub token format', result.stderr)
        self.assertEqual(self.fixture['api_auth'], [])

    def test_installer_arguments_and_github_token_not_forwarded(self):
        self.fixture['capture'] = True
        self.run_bootstrap(role='node', extra=['--ca-file', '/private/CA with spaces.pem'], env_token=True)
        arguments = (self.base / 'installer-args').read_bytes().decode().split('\0')[:-1]
        self.assertEqual(arguments[0], '--binary')
        self.assertTrue(arguments[1].endswith('/server-binary'))
        self.assertEqual(arguments[arguments.index('--manager') + 1], 'https://manager.example.invalid')
        self.assertEqual(arguments[arguments.index('--name') + 1], 'test node')
        self.assertEqual(arguments[arguments.index('--hysteria-binary') + 1], str(self.core))
        self.assertEqual(arguments[-2:], ['--ca-file', '/private/CA with spaces.pem'])


if __name__ == '__main__':
    unittest.main()
