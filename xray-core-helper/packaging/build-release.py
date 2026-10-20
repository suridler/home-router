#!/usr/bin/env python3
"""Build two Linux bundles and raw CLI assets from one committed Git snapshot; never package live files."""
import gzip
import hashlib
import io
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile


def release_version(value):
    if not isinstance(value, str):
        raise ValueError('invalid release version')
    match = re.fullmatch(r'helper-v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*))?', value)
    if not match:
        raise ValueError('invalid release version')
    prerelease = match[4].split('.') if match[4] else []
    if any(part.isdigit() and len(part) > 1 and part[0] == '0' for part in prerelease):
        raise ValueError('invalid release version')
    return tuple(int(match[i]) for i in (1, 2, 3)), prerelease


def version_compare(left, right):
    core_a, pre_a = release_version(left)
    core_b, pre_b = release_version(right)
    if core_a != core_b:
        return (core_a > core_b) - (core_a < core_b)
    if not pre_a or not pre_b:
        return bool(pre_b) - bool(pre_a)
    for a, b in zip(pre_a, pre_b):
        if a == b:
            continue
        if a.isdigit() and b.isdigit():
            return (int(a) > int(b)) - (int(a) < int(b))
        if a.isdigit() != b.isdigit():
            return -1 if a.isdigit() else 1
        return (a > b) - (a < b)
    return (len(pre_a) > len(pre_b)) - (len(pre_a) < len(pre_b))


def build(version, output):
    release_version(version)
    repo = Path(__file__).resolve().parents[2]
    output = Path(output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    commit = subprocess.check_output(['git', '-C', str(repo), 'rev-parse', 'HEAD'], text=True).strip()
    timestamp = int(subprocess.check_output(['git', '-C', str(repo), 'show', '-s', '--format=%ct', commit]))
    tracked = subprocess.check_output(
        ['git', '-C', str(repo), 'ls-tree', '-r', '--name-only', commit], text=True).splitlines()
    documentation = [name for name in tracked if name.endswith('.md')
                     and Path(name).name != 'AGENTS.md'
                     and not any(part.startswith('.') for part in Path(name).parts)]
    extra_paths = [name for name in documentation
                   if not name.startswith(('xray-core-helper/', 'xrayui-web/'))]
    if 'LICENSE' in tracked:
        extra_paths.append('LICENSE')
    suffix = version.removeprefix('helper-')
    archives = {arch: output / f'xray-core-helper-{suffix}-linux-{arch}.tar.gz'
                for arch in ('amd64', 'arm64')}
    clis = {arch: output / f'xray-core-helper-{suffix}-linux-{arch}'
            for arch in ('amd64', 'arm64')}
    for asset in (*archives.values(), *clis.values()):
        if asset.exists():
            raise ValueError('refusing to replace an existing release asset')
    with tempfile.TemporaryDirectory(prefix='xray-release-build.') as temporary:
        snapshot = Path(temporary) / 'source'
        snapshot.mkdir()
        archive = subprocess.check_output(['git', '-C', str(repo), 'archive', commit,
                                           'xray-core-helper', 'xrayui-web', *extra_paths])
        # The archive is produced locally by Git from the selected trusted snapshot.
        with tarfile.open(fileobj=io.BytesIO(archive)) as source:
            for member in source.getmembers():
                if not member.isdir() and not member.isfile():
                    raise ValueError('source snapshot contains a link or special file')
            source.extractall(snapshot)
        helper = snapshot / 'xray-core-helper'
        spec = importlib.util.spec_from_file_location('release_dlc_builder', helper / 'packaging/dlc_builder.py')
        dlc_builder = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(dlc_builder)
        policy_path = helper / 'packaging' / 'release-policy.json'
        policy = json.loads(policy_path.read_text()) if policy_path.exists() else {}
        if set(policy) - {'minimum_installable_version'}:
            raise ValueError('unknown release policy field')
        minimum = policy.get('minimum_installable_version')
        if minimum is not None and version_compare(minimum, version) > 0:
            raise ValueError('minimum_installable_version exceeds release version')
        for arch in ('amd64', 'arm64'):
            stage = Path(temporary) / arch
            target = stage / 'xray-core-helper'
            target.mkdir(parents=True)
            for name in ('packaging', 'templConf'):
                shutil.copytree(helper / name, target / name,
                                ignore=shutil.ignore_patterns('*.go', '__pycache__', '*.pyc'))
            shutil.copyfile(helper / 'config.example.toml', target / 'config.example.toml')
            web = stage / 'xrayui-web'
            web.mkdir()
            for name in ('app.js', 'server.js', 'package.json', 'package-lock.json', 'xrayui-web.service',
                         'packaging', 'config', 'controllers', 'lib', 'middleware', 'public', 'routes', 'services'):
                path = snapshot / 'xrayui-web' / name
                if path.is_dir():
                    shutil.copytree(path, web / name)
                else:
                    shutil.copyfile(path, web / name)
            for name in documentation + (['LICENSE'] if 'LICENSE' in tracked else []):
                destination = stage / name
                destination.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(snapshot / name, destination)
            binaries = target / 'bin'
            binaries.mkdir()
            hashes = {}
            env = dict(os.environ, GOOS='linux', GOARCH=arch, CGO_ENABLED='0', GOAMD64='v1')
            for name in ('xray-core-helper', 'xray-core-helperd'):
                flags = f'-s -w -X main.version={version}+{commit[:12]}'
                subprocess.run(['go', '-C', str(helper), 'build', '-mod=readonly', '-buildvcs=false', '-trimpath',
                                '-ldflags', flags, '-o', str(binaries / name), f'./cmd/{name}'],
                               env=env, check=True)
                hashes[name] = hashlib.sha256((binaries / name).read_bytes()).hexdigest()
            dlc_builder.build(binaries / 'domain-list-community',
                              target / 'licenses' / 'domain-list-community.txt', env)
            hashes['domain-list-community'] = hashlib.sha256(
                (binaries / 'domain-list-community').read_bytes()).hexdigest()
            metadata = dict(schemaVersion=1, version=version, commit=commit,
                            os='linux', arch=arch, binaries=hashes,
                            dependencies={'domain-list-community': dlc_builder.PIN})
            if minimum is not None:
                metadata['minimum_installable_version'] = minimum
            (stage / 'release.json').write_text(json.dumps(metadata, indent=2) + '\n')
            asset = archives[arch]
            with asset.open('xb') as raw, gzip.GzipFile(fileobj=raw, mode='wb', filename='', mtime=timestamp) as compressed:
                with tarfile.open(fileobj=compressed, mode='w') as packed:
                    for path in sorted(stage.rglob('*')):
                        info = packed.gettarinfo(str(path), arcname=path.relative_to(stage).as_posix())
                        if not info.isdir() and not info.isfile():
                            raise ValueError('release contains a link or special file')
                        info.uid = info.gid = 0
                        info.uname = info.gname = 'root'
                        info.mtime = timestamp
                        info.mode = 0o755 if path.is_dir() or os.access(path, os.X_OK) else 0o644
                        with path.open('rb') if path.is_file() else io.BytesIO() as stream:
                            packed.addfile(info, stream if path.is_file() else None)
            print(asset, flush=True)
            cli = clis[arch]
            shutil.copyfile(binaries / 'xray-core-helper', cli)
            cli.chmod(0o755)
            print(cli, flush=True)
        shutil.copyfile(helper / 'packaging' / 'install-from-github.sh', output / 'install-from-github.sh')
    assets = [archives[arch] for arch in ('amd64', 'arm64')] + [clis[arch] for arch in ('amd64', 'arm64')]
    with (output / 'SHA256SUMS').open('x') as sums:
        for path in assets:
            sums.write(f'{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n')


if __name__ == '__main__':
    if len(sys.argv) != 3:
        sys.exit('usage: build-release.py helper-vVERSION OUTPUT_DIRECTORY')
    build(sys.argv[1], sys.argv[2])
