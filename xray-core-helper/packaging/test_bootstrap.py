"""Hermetic bootstrap tests; fake curl, uname, sudo and saved-installer stubs."""
import hashlib
import json
import os
from pathlib import Path
import pty
import subprocess
import tempfile
import textwrap
import unittest
from urllib.parse import parse_qs, urlparse


PACKAGING = Path(__file__).resolve().parent
SCRIPT = PACKAGING / 'install-from-github.sh'
VERSION = 'helper-v1.2.3'
UPDATE_DIR = '/var/lib/xray-core-helper-update'


CLI = b'''#!/usr/bin/env python3
import json
import os
import sys

with open(os.environ['INSTALL_LOG'], 'a', encoding='utf-8') as stream:
    stream.write(json.dumps({
        'args': sys.argv[1:],
        'tty': os.isatty(0) and os.isatty(1),
    }) + '\\n')
'''


class BootstrapTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.api = self.root / 'api'
        self.api.mkdir()
        self.assets = self.root / 'assets'
        self.assets.mkdir()
        self.temporary = self.root / 'tmp'
        self.temporary.mkdir()
        self.install_log = self.root / 'install.jsonl'
        self.sudo_log = self.root / 'sudo.jsonl'
        self.curl_log = self.root / 'curl.jsonl'
        self.legacy_log = self.root / 'legacy.txt'
        self.env = dict(
            os.environ,
            HOME=str(self.root),
            TMPDIR=str(self.temporary),
            PATH=f'{self.bin}:{os.environ["PATH"]}',
            INSTALL_LOG=str(self.install_log),
            SUDO_LOG=str(self.sudo_log),
            CURL_LOG=str(self.curl_log),
            API_DIR=str(self.api),
            ASSETS_DIR=str(self.assets),
            LEGACY_LOG=str(self.legacy_log),
        )
        self.write_commands()

    def executable(self, name, body):
        path = self.bin / name
        path.write_text('#!/usr/bin/env python3\n' + textwrap.dedent(body).lstrip())
        path.chmod(0o755)

    def write_commands(self):
        self.executable('uname', '''
            import os
            import sys

            arguments = sys.argv[1:]
            if arguments == ['-s']:
                print(os.environ.get('UNAME_SYS', 'Linux'))
            elif arguments == ['-m']:
                print(os.environ.get('UNAME_MACH', 'x86_64'))
            else:
                sys.exit(2)
        ''')
        self.executable('curl', '''
            import json
            import os
            import shutil
            import sys
            from pathlib import Path
            from urllib.parse import parse_qs, urlparse

            arguments = sys.argv[1:]
            try:
                output = Path(arguments[arguments.index('--output') + 1])
            except (IndexError, ValueError):
                sys.exit(2)
            url = arguments[-1]
            with open(os.environ['CURL_LOG'], 'a', encoding='utf-8') as stream:
                stream.write(json.dumps({'args': arguments, 'url': url}) + '\\n')
            if os.environ.get('CURL_FAIL') == '1':
                sys.exit(22)
            parsed = urlparse(url)
            if parsed.netloc == 'api.github.com':
                page = int(parse_qs(parsed.query).get('page', ['1'])[0])
                source = Path(os.environ['API_DIR']) / f'page-{page}.json'
                if source.is_file():
                    shutil.copyfile(source, output)
                else:
                    output.write_text('[]\\n')
            elif parsed.netloc == 'github.com':
                source = Path(os.environ['ASSETS_DIR']) / Path(parsed.path).name
                if not source.is_file():
                    sys.exit(22)
                shutil.copyfile(source, output)
            else:
                sys.exit(2)
        ''')
        self.executable('sudo', '''
            import json
            import os
            import sys

            arguments = sys.argv[1:]
            if arguments and arguments[0] == '--':
                arguments = arguments[1:]
            with open(os.environ['SUDO_LOG'], 'a', encoding='utf-8') as stream:
                stream.write(json.dumps(arguments) + '\\n')
            if not arguments:
                sys.exit(2)
            if arguments[0] in ('stat', 'cat', 'sha256sum'):
                stub = os.path.join(
                    os.path.dirname(os.path.realpath(__file__)), arguments[0])
                os.execv(stub, [stub, *arguments[1:]])
            if os.environ.get('SUDO_EXEC') == '1':
                os.execvp(arguments[0], arguments)
            sys.exit(0)
        ''')
        self.executable('stat', '''
            import os
            import sys

            update_dir = '/var/lib/xray-core-helper-update'
            path = sys.argv[-1]
            if os.environ.get('SAVED_STATE', 'missing') == 'missing':
                sys.exit(1)
            if path == update_dir:
                kind = os.environ.get('SAVED_DIR_KIND', 'directory')
                mode = os.environ.get('SAVED_DIR_MODE', '0755')
            elif path == update_dir + '/installer':
                kind = os.environ.get('SAVED_INSTALLER_KIND', 'regular file')
                mode = os.environ.get('SAVED_INSTALLER_MODE', '0755')
            elif path == update_dir + '/installer.sha256':
                kind = os.environ.get('SAVED_SUMS_KIND', 'regular file')
                mode = os.environ.get('SAVED_SUMS_MODE', '0644')
            else:
                sys.exit(1)
            if kind == 'missing':
                sys.exit(1)
            uid = os.environ.get('SAVED_UID', '0')
            print(f'{kind}:{uid}:{mode}')
        ''')
        self.executable('cat', '''
            import os
            import sys

            update_dir = '/var/lib/xray-core-helper-update'
            arguments = [item for item in sys.argv[1:] if item != '--']
            path = arguments[-1] if arguments else ''
            if (os.environ.get('SAVED_STATE', 'missing') != 'missing'
                    and path == update_dir + '/installer.sha256'):
                sys.stdout.write(os.environ.get('SAVED_SUMS', ''))
                sys.exit(0)
            sys.exit(1)
        ''')
        self.executable('sha256sum', '''
            import hashlib
            import os
            import sys
            from pathlib import Path

            update_dir = '/var/lib/xray-core-helper-update'
            arguments = [item for item in sys.argv[1:] if item != '--']
            path = arguments[-1] if arguments else ''
            if (os.environ.get('SAVED_STATE', 'missing') != 'missing'
                    and path == update_dir + '/installer'):
                digest = os.environ.get('SAVED_DIGEST', '0' * 64)
                print(f'{digest}  {path}')
                sys.exit(0)
            try:
                temporary = Path(os.environ['TMPDIR']).resolve()
                candidate = Path(path).resolve()
                candidate.relative_to(temporary)
                if not candidate.is_file():
                    raise ValueError
                digest = hashlib.sha256(candidate.read_bytes()).hexdigest()
            except (KeyError, OSError, ValueError):
                sys.exit(1)
            print(f'{digest}  {path}')
        ''')
        for name in ('gh', 'npm', 'tar', 'go', 'systemctl', 'service'):
            self.executable(name, '''
                import os
                import sys

                with open(os.environ['LEGACY_LOG'], 'a', encoding='utf-8') as stream:
                    stream.write(os.path.basename(sys.argv[0]) + '\\n')
                sys.exit(99)
            ''')

    def write_asset(self, tag, *, arch='amd64', content=CLI, sums=None):
        name = f'xray-core-helper-{tag.removeprefix("helper-")}-linux-{arch}'
        (self.assets / name).write_bytes(content)
        digest = hashlib.sha256(content).hexdigest()
        (self.assets / 'SHA256SUMS').write_text(
            sums if sums is not None else f'{digest}  {name}\n')
        return name, digest

    def release(self, tag, *, draft=False, has_assets=True, with_checksums=True):
        raw = f'xray-core-helper-{tag.removeprefix("helper-")}-linux-amd64'
        names = [raw if has_assets else raw + '.tar.gz']
        if with_checksums:
            names.append('SHA256SUMS')
        return {
            'tag_name': tag,
            'draft': draft,
            'prerelease': '-' in tag,
            'assets': [{'name': name} for name in names],
        }

    def write_page(self, page, releases):
        (self.api / f'page-{page}.json').write_text(json.dumps(releases))

    def invoke(self, *arguments, **environment):
        return subprocess.run(
            ['bash', str(SCRIPT), *arguments],
            env=dict(self.env, **environment),
            text=True,
            capture_output=True,
            timeout=30,
        )

    def clear_logs(self):
        for path in (self.install_log, self.sudo_log, self.curl_log, self.legacy_log):
            if path.exists():
                path.unlink()

    @staticmethod
    def json_lines(path):
        if not path.exists():
            return []
        return [json.loads(line) for line in path.read_text().splitlines() if line]

    def install_calls(self):
        return self.json_lines(self.install_log)

    def sudo_calls(self):
        return self.json_lines(self.sudo_log)

    def sudo_metadata_calls(self):
        return [call for call in self.sudo_calls()
                if call and call[0] in ('stat', 'cat', 'sha256sum')]

    def installer_sudo_calls(self):
        return [call for call in self.sudo_calls()
                if call and call[0] == UPDATE_DIR + '/installer']

    def curl_calls(self):
        return self.json_lines(self.curl_log)

    def legacy_calls(self):
        if not self.legacy_log.exists():
            return []
        return self.legacy_log.read_text().splitlines()

    def test_explicit_version_uses_verified_raw_cli_and_forwards_flags(self):
        for flag in ('--with-web', '--without-web'):
            with self.subTest(flag=flag):
                self.clear_logs()
                name, _ = self.write_asset(VERSION)
                result = self.invoke('--version', VERSION, flag, SUDO_EXEC='1')
                self.assertEqual(result.returncode, 0, result.stderr)
                installs = self.install_calls()
                self.assertEqual(len(installs), 1)
                self.assertEqual(installs[0]['args'],
                                 ['install', '--version', VERSION, flag])
                sudo = self.sudo_calls()
                self.assertEqual(len(sudo), 1)
                self.assertEqual(sudo[0][1:], ['install', '--version', VERSION, flag])
                urls = [call['url'] for call in self.curl_calls()]
                self.assertTrue(any(url.endswith('/' + name) for url in urls))
                self.assertTrue(any(url.endswith('/SHA256SUMS') for url in urls))
                self.assertFalse(any('/api.github.com/' in url for url in urls))
                self.assertFalse(any('/latest' in url for url in urls))
                self.assertEqual(self.legacy_calls(), [])
                self.assertEqual(
                    list(self.temporary.glob('xray-helper-bootstrap.*')), [])

    def test_latest_uses_paginated_release_list_and_semver(self):
        self.write_page(1, [self.release(f'helper-v1.0.{index}')
                            for index in range(100)])
        self.write_page(2, [
            self.release('helper-v2.0.0-beta.2'),
            self.release('helper-v2.0.0-beta.10'),
            self.release('helper-v2.0.0', draft=True),
            self.release('helper-v3.0.0', has_assets=False),
            self.release('helper-v4.0.0', with_checksums=False),
        ])
        self.write_asset('helper-v2.0.0-beta.10')
        result = self.invoke(SUDO_EXEC='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.install_calls()[0]['args'],
                         ['install', '--version', 'helper-v2.0.0-beta.10'])
        pages = {int(parse_qs(urlparse(call['url']).query)['page'][0])
                 for call in self.curl_calls()
                 if urlparse(call['url']).netloc == 'api.github.com'}
        self.assertEqual(pages, {1, 2})
        urls = [call['url'] for call in self.curl_calls()]
        self.assertFalse(any('/latest' in url for url in urls))

    def test_stable_release_wins_over_prerelease(self):
        self.write_page(1, [
            self.release('helper-v1.0.0'),
            self.release('helper-v1.1.0-beta.3'),
            self.release('helper-v1.1.0'),
            self.release('helper-v1.0.10'),
        ])
        self.write_asset('helper-v1.1.0')
        result = self.invoke(SUDO_EXEC='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.install_calls()[0]['args'],
                         ['install', '--version', 'helper-v1.1.0'])

    def test_arm64_asset_is_selected(self):
        name, _ = self.write_asset('helper-v1.0.0', arch='arm64')
        result = self.invoke('--version', 'helper-v1.0.0', UNAME_MACH='aarch64',
                             SUDO_EXEC='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(any(call['url'].endswith('/' + name)
                            for call in self.curl_calls()))

    def test_unsupported_platform_fails_before_download(self):
        for system, machine in (
                ('Darwin', 'x86_64'), ('Linux', 'riscv64'), ('Linux', 'armv7l')):
            with self.subTest(system=system, machine=machine):
                result = self.invoke('--version', VERSION, UNAME_SYS=system,
                                     UNAME_MACH=machine)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.curl_calls(), [])

    def test_unexpected_or_invalid_arguments_are_rejected(self):
        cases = {
            'missing value': ('--version',),
            'invalid tag': ('--version', 'latest'),
            'short tag': ('--version', 'helper-v1.2'),
            'duplicate version': ('--version', VERSION, '--version', VERSION),
            'recover with version': ('--recover', '--version', VERSION),
            'unknown option': ('--repo', 'evil'),
            'legacy prepare': ('--prepare-only',),
            'duplicate web': ('--with-web', '--without-web'),
        }
        for label, arguments in cases.items():
            with self.subTest(label=label):
                result = self.invoke(*arguments)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertEqual(self.curl_calls(), [])

    def test_download_failure_prevents_execution(self):
        self.write_asset(VERSION)
        result = self.invoke('--version', VERSION, CURL_FAIL='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('download failed', result.stderr)
        self.assertEqual(self.sudo_calls(), [])
        self.assertEqual(self.install_calls(), [])
        self.assertEqual(self.legacy_calls(), [])

    def test_checksum_mismatch_prevents_execution(self):
        name, _ = self.write_asset(VERSION)
        (self.assets / 'SHA256SUMS').write_text(f'{"0" * 64}  {name}\n')
        result = self.invoke('--version', VERSION, SUDO_EXEC='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('checksum mismatch', result.stderr)
        self.assertEqual(self.sudo_calls(), [])
        self.assertEqual(self.install_calls(), [])

    def test_checksum_requires_one_exact_filename(self):
        name, digest = self.write_asset(VERSION)
        cases = {
            'duplicate': f'{digest}  {name}\n{digest}  {name}\n',
            'wrong filename': f'{digest}  {name}.bak\n',
            'missing digest': f'{name}\n',
            'malformed line': f'{digest} *{name}\n',
        }
        for label, sums in cases.items():
            with self.subTest(label=label):
                (self.assets / 'SHA256SUMS').write_text(sums)
                result = self.invoke('--version', VERSION, SUDO_EXEC='1')
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.sudo_calls(), [])
                self.assertEqual(self.install_calls(), [])

    def test_metadata_request_failure_is_an_error(self):
        result = self.invoke(CURL_FAIL='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('release metadata request failed', result.stderr)
        self.assertEqual(self.sudo_calls(), [])
        self.assertEqual(self.install_calls(), [])

    def test_missing_suitable_release_is_an_error(self):
        self.write_page(1, [self.release('helper-v1.0.0', has_assets=False)])
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('no suitable published release', result.stderr)
        self.assertEqual(self.sudo_calls(), [])
        self.assertEqual(self.install_calls(), [])

    def test_metadata_page_is_bounded(self):
        self.write_page(1, [self.release('helper-v1.0.0')] * 101)
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('invalid release metadata', result.stderr)
        self.assertEqual(self.sudo_calls(), [])

    def test_recover_executes_saved_verified_installer_without_network(self):
        digest = 'a' * 64
        for option in (None, '--with-web', '--without-web'):
            with self.subTest(option=option):
                self.clear_logs()
                arguments = ['--recover'] + ([option] if option else [])
                result = self.invoke(
                    *arguments, SAVED_STATE='valid', SAVED_SUMS=f'{digest}\n',
                    SAVED_DIGEST=digest)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(self.curl_calls(), [])
                expected = ['install', '--recover'] + ([option] if option else [])
                self.assertEqual(self.installer_sudo_calls(), [[
                    UPDATE_DIR + '/installer', *expected,
                ]])
                self.assertEqual(self.install_calls(), [])
                self.assertEqual(self.legacy_calls(), [])
                self.assertEqual(
                    list(self.temporary.glob('xray-helper-bootstrap.*')), [])

    @unittest.skipIf(os.geteuid() == 0, 'requires an unprivileged test runner')
    def test_unprivileged_recovery_reads_0700_root_state_through_sudo(self):
        digest = 'd' * 64
        result = self.invoke(
            '--recover', SAVED_STATE='valid', SAVED_DIR_MODE='0700',
            SAVED_INSTALLER_MODE='0700', SAVED_SUMS_MODE='0600',
            SAVED_SUMS=f'{digest}  installer\n', SAVED_DIGEST=digest)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.curl_calls(), [])
        metadata = self.sudo_metadata_calls()
        self.assertIn(['stat', '-c', '%F:%u:%a', '--', UPDATE_DIR], metadata)
        self.assertIn(['cat', '--', UPDATE_DIR + '/installer.sha256'], metadata)
        self.assertIn(['sha256sum', '--', UPDATE_DIR + '/installer'], metadata)
        self.assertEqual(self.installer_sudo_calls(),
                         [[UPDATE_DIR + '/installer', 'install', '--recover']])

    def test_recover_without_saved_installer_does_not_use_network(self):
        result = self.invoke('--recover')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('saved verified installer', result.stderr)
        self.assertEqual(self.curl_calls(), [])
        self.assertEqual(self.installer_sudo_calls(), [])

    def test_recover_rejects_untrusted_or_unverified_installer(self):
        digest = 'b' * 64
        base = dict(SAVED_STATE='valid', SAVED_SUMS=f'{digest}\n',
                    SAVED_DIGEST=digest)
        cases = {
            'non-root owner': {'SAVED_UID': '1000'},
            'group writable installer': {'SAVED_INSTALLER_MODE': '0775'},
            'symlink installer': {'SAVED_INSTALLER_KIND': 'symbolic link'},
            'symlink checksum': {'SAVED_SUMS_KIND': 'symbolic link'},
            'symlink directory': {'SAVED_DIR_KIND': 'symbolic link'},
            'writable directory': {'SAVED_DIR_MODE': '0777'},
            'missing checksum': {'SAVED_SUMS_KIND': 'missing'},
            'writable checksum': {'SAVED_SUMS_MODE': '0666'},
            'malformed checksum': {'SAVED_SUMS': 'not-a-digest\n'},
            'multiple checksums': {'SAVED_SUMS': f'{digest}\n{digest}\n'},
            'wrong checksum filename': {'SAVED_SUMS': f'{digest}  other\n'},
            'non-executable installer': {'SAVED_INSTALLER_MODE': '0644'},
            'checksum mismatch': {'SAVED_DIGEST': 'c' * 64},
        }
        for label, overrides in cases.items():
            with self.subTest(label=label):
                self.clear_logs()
                result = self.invoke('--recover', **dict(base, **overrides))
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.curl_calls(), [])
                self.assertEqual(self.installer_sudo_calls(), [])

    def test_terminal_is_preserved_for_installer(self):
        self.write_asset(VERSION)
        master, slave = pty.openpty()
        try:
            result = subprocess.run(
                ['bash', str(SCRIPT), '--version', VERSION],
                env=dict(self.env, SUDO_EXEC='1'),
                stdin=slave, stdout=slave, stderr=slave, timeout=30)
        finally:
            os.close(master)
            os.close(slave)
        self.assertEqual(result.returncode, 0)
        installs = self.install_calls()
        self.assertEqual(len(installs), 1)
        self.assertTrue(installs[0]['tty'])


if __name__ == '__main__':
    unittest.main()
