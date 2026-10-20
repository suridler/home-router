"""Build real archives and raw CLI assets from a local Git fixture with a fake Go compiler."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import tarfile
import tempfile
import unittest

PACKAGING = Path(__file__).resolve().parent
NATIVE_TEMPLATES = PACKAGING.parent / 'templConf'
CANONICAL_XRAY_TEMPLATE = 'xray-core-helper/templConf/xray/config.json'
VERSION = 'helper-v0.2.0-beta.1'
UPSTREAM_LICENSE = b'fake domain-list-community license\n'


def load_pin():
    spec = importlib.util.spec_from_file_location('fixture_dlc_builder', PACKAGING / 'dlc_builder.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return dict(module.PIN)


class ReleaseBuildTest(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name)
        self.repo=self.root/'repo'
        self.repo.mkdir()
        self.helper=self.repo/'xray-core-helper'
        self.packaging=self.helper/'packaging'
        self.packaging.mkdir(parents=True)
        for name in ('build-release.py','verify-release.py','install-from-github.sh','dlc_builder.py'):
            shutil.copyfile(PACKAGING/name,self.packaging/name)
        for name in ('config.example.toml','README.md'):
            (self.helper/name).write_text('fixture\n')
        for name in ('README.md', 'CONTRIBUTING.md', 'LICENSE', 'docs/INSTALLATION.md',
                     'xray-core-helper/docs/NATIVE_TEMPLATES.md', 'xrayui-web/README.md',
                     'xray-routing-exporter/README.md'):
            path = self.repo/name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('committed documentation\n')
        # Use the real shipped template set so omission of the canonical Xray
        # template cannot hide behind a synthetic "template" placeholder.
        shutil.copytree(NATIVE_TEMPLATES, self.helper / 'templConf',
                        ignore=shutil.ignore_patterns('__pycache__', '*.pyc'))
        web=self.repo/'xrayui-web';web.mkdir(exist_ok=True)
        for name in ('app.js','server.js','package.json','package-lock.json','xrayui-web.service'):
            (web/name).write_text('{}\n')
        for name in ('packaging','config','controllers','lib','middleware','public','routes','services'):
            (web/name).mkdir();(web/name/'fixture').write_text('fixture\n')
        self.git('init','--quiet','-b','main');self.git('add','.')
        self.git('-c','user.name=Fixture','-c','user.email=fixture@example.test','commit','--quiet','-m','fixture')
        self.sha=self.git('rev-parse','HEAD').stdout.strip()
        self.pin=load_pin()
        self.dlc_source=self.root/'dlc-source';self.dlc_source.mkdir()
        (self.dlc_source/'LICENSE').write_bytes(UPSTREAM_LICENSE)
        self.dlc_metadata=self.root/'dlc-metadata.json'
        self.dlc_log=self.root/'dlc-downloads.txt'
        self.go_log=self.root/'go-builds.jsonl'
        self.write_dlc_metadata()
        self.bin=self.root/'bin';self.bin.mkdir()
        go=self.bin/'go'
        go.write_text('''#!/usr/bin/env python3
import json,os,sys,struct
from pathlib import Path
args=sys.argv[1:]
with open(os.environ['GO_LOG'],'a') as log:
    log.write(json.dumps(args)+'\\n')
if 'mod' in args and 'download' in args:
    metadata=json.loads(Path(os.environ['DLC_METADATA']).read_text())
    requested=args[args.index('-json')+1]
    with open(os.environ['DLC_LOG'],'a') as log:
        log.write(requested+'\\n')
    if requested != f"{metadata['Path']}@{metadata['Version']}":
        metadata['Version']=requested.rsplit('@',1)[-1]
    print(json.dumps(metadata))
    sys.exit(0)
b=bytearray(64);b[:6]=b'\\x7fELF\\x02\\x01'
struct.pack_into('<H',b,18,{'amd64':62,'arm64':183}[os.environ['GOARCH']])
p=Path(args[args.index('-o')+1]);p.write_bytes(b);p.chmod(0o755)
''')
        go.chmod(0o755)
        self.env=dict(os.environ,PATH=f'{self.bin}:{os.environ["PATH"]}',
                      DLC_METADATA=str(self.dlc_metadata),DLC_LOG=str(self.dlc_log),
                      GO_LOG=str(self.go_log))
        self.output=self.root/'dist'

    def write_dlc_metadata(self,**overrides):
        metadata=dict(Path=self.pin['module'],Version=self.pin['version'],Sum=self.pin['sum'],
                      GoModSum=self.pin['goModSum'],Dir=str(self.dlc_source))
        metadata.update(overrides)
        self.dlc_metadata.write_text(json.dumps(metadata))

    def git(self,*args):
        return subprocess.run(['git','-C',str(self.repo),*args],check=True,text=True,capture_output=True)

    def build(self):
        return subprocess.run(['python3',str(self.packaging/'build-release.py'),VERSION,str(self.output)],
                              env=self.env,text=True,capture_output=True,timeout=30)

    def test_archives_share_commit_and_exclude_dirty_runtime_files(self):
        committed_canonical = (self.helper / 'templConf/xray/config.json').read_bytes()
        (self.helper/'templConf/xray/config.json').write_text('dirty canonical template')
        (self.packaging/'install-from-github.sh').write_text('dirty bootstrap')
        (self.repo/'xrayui-web'/'.env').write_text('never ship')
        (self.repo/'docs/INSTALLATION.md').write_text('dirty instructions')
        (self.repo/'docs/untracked.md').write_text('never ship')
        result=self.build();self.assertEqual(result.returncode,0,result.stderr)
        raw_assets=[]
        for arch in ('amd64','arm64'):
            path=self.output/f'xray-core-helper-{VERSION.removeprefix("helper-")}-linux-{arch}.tar.gz'
            with tarfile.open(path) as archive:
                manifest=json.load(archive.extractfile('release.json'))
                self.assertEqual(manifest['commit'],self.sha)
                self.assertEqual(manifest['arch'],arch)
                self.assertIn(CANONICAL_XRAY_TEMPLATE, archive.getnames(),
                              'bundle omitted the selected Xray template')
                self.assertEqual(archive.extractfile(CANONICAL_XRAY_TEMPLATE).read(),
                                 committed_canonical)
                xray_templates = sorted(member.name for member in archive.getmembers()
                                        if member.isfile()
                                        and member.name.startswith('xray-core-helper/templConf/xray/'))
                self.assertEqual(xray_templates, [CANONICAL_XRAY_TEMPLATE])
                self.assertNotIn('xrayui-web/.env',archive.getnames())
                self.assertNotIn('docs/untracked.md',archive.getnames())
                for name in ('docs/INSTALLATION.md', 'xray-core-helper/docs/NATIVE_TEMPLATES.md',
                             'xrayui-web/README.md', 'xray-routing-exporter/README.md',
                             'README.md', 'CONTRIBUTING.md', 'LICENSE'):
                    self.assertEqual(archive.extractfile(name).read(), b'committed documentation\n')
                self.assertEqual(set(manifest['binaries']),
                                 {'xray-core-helper', 'xray-core-helperd', 'domain-list-community'})
                self.assertEqual(manifest['dependencies'], {'domain-list-community': self.pin})
                self.assertEqual(archive.extractfile(
                    'xray-core-helper/licenses/domain-list-community.txt').read(), UPSTREAM_LICENSE)
                for name,digest in manifest['binaries'].items():
                    content=archive.extractfile(f'xray-core-helper/bin/{name}').read()
                    self.assertEqual(hashlib.sha256(content).hexdigest(),digest)
                    self.assertEqual(content[:6], b'\x7fELF\x02\x01')
                    self.assertEqual(struct.unpack_from('<H', content, 18)[0],
                                     {'amd64': 62, 'arm64': 183}[arch])
                cli=archive.extractfile('xray-core-helper/bin/xray-core-helper').read()
            raw=self.output/f'xray-core-helper-{VERSION.removeprefix("helper-")}-linux-{arch}'
            self.assertEqual(raw.read_bytes(), cli)
            self.assertNotEqual(raw.stat().st_mode & 0o111, 0)
            raw_assets.append(raw)
        sums=(self.output/'SHA256SUMS').read_text().splitlines()
        self.assertEqual(len(sums), 4)
        names=[line.split('  ',1)[1] for line in sums]
        self.assertEqual(len(set(names)), 4)
        for path in [self.output/f'xray-core-helper-{VERSION.removeprefix("helper-")}-linux-{arch}.tar.gz'
                     for arch in ('amd64','arm64')] + raw_assets:
            self.assertIn(f'{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}', sums)
        self.assertEqual((self.output/'install-from-github.sh').read_bytes(),(PACKAGING/'install-from-github.sh').read_bytes())
        builds=[json.loads(line) for line in self.go_log.read_text().splitlines()]
        instrumented=[args for args in builds
                      if './cmd/xray-core-helper' in args or './cmd/xray-core-helperd' in args]
        self.assertEqual(len(instrumented), 4)
        for args in instrumented:
            flags=args[args.index('-ldflags')+1]
            self.assertIn(f'-X main.version={VERSION}+{self.sha[:12]}', flags)

    def test_committed_downgrade_policy_is_published(self):
        minimum = 'helper-v0.2.0-beta.0'
        policy = self.packaging / 'release-policy.json'
        policy.write_text(json.dumps({'minimum_installable_version': minimum}))
        self.git('add', '.')
        self.git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.test',
                 'commit', '--quiet', '-m', 'policy')
        # The uncommitted value must not override the selected release snapshot.
        policy.write_text(json.dumps({'minimum_installable_version': 'helper-v9.0.0'}))
        result = self.build()
        self.assertEqual(result.returncode, 0, result.stderr)
        for arch in ('amd64', 'arm64'):
            with tarfile.open(self.output / f'xray-core-helper-{VERSION.removeprefix("helper-")}-linux-{arch}.tar.gz') as bundle:
                metadata = json.load(bundle.extractfile('release.json'))
            self.assertEqual(metadata['minimum_installable_version'], minimum)

    def test_module_checksum_mismatch_prevents_any_archive(self):
        self.write_dlc_metadata(Sum='h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=')
        result=self.build()
        self.assertNotEqual(result.returncode,0)
        self.assertIn('checksum mismatch',result.stderr)
        self.assertEqual(list(self.output.glob('*.tar.gz')),[])
        self.assertFalse((self.output/'SHA256SUMS').exists())

    def test_invalid_or_future_downgrade_policy_prevents_archives(self):
        for minimum in ('helper-v9.0.0', 'helper-v0.2.0', 'helper-v0.2.0-beta.2',
                        'helper-v0.2.0-beta.01', 'helper-v00.2.0', 1):
            with self.subTest(minimum=minimum):
                (self.packaging / 'release-policy.json').write_text(
                    json.dumps({'minimum_installable_version': minimum}))
                self.git('add', '.')
                self.git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.test',
                         'commit', '--quiet', '-m', 'invalid policy')
                result = self.build()
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(list(self.output.glob('*.tar.gz')), [])
                self.assertFalse(self.go_log.exists(), 'compiler ran before policy validation')

    def test_dirty_live_builder_pin_does_not_affect_committed_build(self):
        builder=self.packaging/'dlc_builder.py'
        dirty_version='v0.0.0-20250101000000-deadbeef'
        builder.write_text(builder.read_text()
            .replace(self.pin['version'],dirty_version)
            .replace(self.pin['sum'],'h1:DIRTYDIRTYDIRTYDIRTYDIRTYDIRTYDIRTYDIRTYDIRTY=')
            .replace(self.pin['goModSum'],'h1:DIRTYMODDIRTYMODDIRTYMODDIRTYMODDIRTYMOD='))
        result=self.build()
        self.assertEqual(result.returncode,0,result.stderr)
        committed=f"{self.pin['module']}@{self.pin['version']}"
        self.assertEqual(set(self.dlc_log.read_text().splitlines()),{committed})
        for arch in ('amd64','arm64'):
            path=self.output/f'xray-core-helper-{VERSION.removeprefix("helper-")}-linux-{arch}.tar.gz'
            with tarfile.open(path) as archive:
                manifest=json.load(archive.extractfile('release.json'))
                self.assertEqual(manifest['dependencies'], {'domain-list-community': self.pin})
                self.assertNotEqual(manifest['dependencies']['domain-list-community']['version'],dirty_version)
                self.assertEqual(archive.extractfile(
                    'xray-core-helper/licenses/domain-list-community.txt').read(), UPSTREAM_LICENSE)

    def test_existing_release_is_never_overwritten(self):
        self.assertEqual(self.build().returncode,0)
        self.assertNotEqual(self.build().returncode,0)

    def test_source_symlink_rejected(self):
        (self.helper/'templConf'/'unsafe').symlink_to('/etc/passwd')
        self.git('add','.')
        self.git('-c','user.name=Fixture','-c','user.email=fixture@example.test','commit','--quiet','-m','unsafe')
        self.assertNotEqual(self.build().returncode,0)
        self.assertEqual(list(self.output.glob('*.tar.gz')),[])


if __name__=='__main__':
    unittest.main()
