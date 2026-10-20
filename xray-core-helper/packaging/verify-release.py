#!/usr/bin/env python3
"""Validate the fixed prebuilt bundle before executing either helper binary."""
import hashlib
import json
import platform
import re
import stat
import struct
import sys
from pathlib import Path

from dlc_builder import PIN


def verify(root):
    root = Path(root)
    manifest = json.loads((root / 'release.json').read_text())
    arch = {'x86_64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(platform.machine())
    if (platform.system() != 'Linux' or not arch or manifest.get('schemaVersion') != 1
            or manifest.get('os') != 'linux' or manifest.get('arch') != arch
            or not re.fullmatch(r'helper-v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?', manifest.get('version', ''))
            or not re.fullmatch(r'[0-9a-f]{40}', manifest.get('commit', ''))):
        raise ValueError('unsupported release metadata or platform')
    if manifest.get('dependencies', {}).get('domain-list-community') != PIN:
        raise ValueError('unsupported domain-list-community build')
    for name in ('xray-core-helper', 'xray-core-helperd', 'domain-list-community'):
        path = root / 'xray-core-helper' / 'bin' / name
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or not info.st_mode & 0o111:
            raise ValueError('release binary must be a regular executable')
        content = path.read_bytes()
        if (len(content) < 64 or content[:6] != b'\x7fELF\x02\x01'
                or struct.unpack_from('<H', content, 18)[0] != {'amd64': 62, 'arm64': 183}[arch]
                or hashlib.sha256(content).hexdigest() != manifest['binaries'].get(name)):
            raise ValueError('release binary checksum or ELF architecture mismatch')
    return manifest


if __name__ == '__main__':
    try:
        verify(sys.argv[1])
    except (OSError, ValueError, KeyError, TypeError, IndexError):
        sys.exit('release_validation_failed')
