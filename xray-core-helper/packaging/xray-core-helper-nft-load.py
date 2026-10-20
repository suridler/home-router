#!/usr/bin/python3
"""Fixed-purpose boot adapter: load only the reviewed gateway block.

No arguments, no configurable commands/paths, no config contents in diagnostics.
The calling routing unit is activated by the helper's confirmed transaction.
"""
import os
import stat
import subprocess
import sys

LIMIT = 1 << 20
BEGIN = b'# helper:begin gateway'
END = b'# helper:end gateway'


def block(raw):
    if len(raw) > LIMIT:
        raise ValueError('size')
    lines = raw.splitlines(keepends=True)
    begins = [i for i, line in enumerate(lines) if line.strip() == BEGIN]
    ends = [i for i, line in enumerate(lines) if line.strip() == END]
    if len(begins) != 1 or len(ends) != 1 or begins[0] >= ends[0]:
        raise ValueError('markers')
    return b''.join(lines[begins[0] + 1:ends[0]])


def main():
    if len(sys.argv) != 1 or os.geteuid() != 0:
        return 1
    try:
        descriptor = os.open('/etc/nftables.conf', os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor, 'rb') as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o6022:
                raise ValueError('untrusted')
            batch = block(stream.read(LIMIT + 1))
        for args in (['--check', '--file', '-'], ['--file', '-']):
            subprocess.run(['/usr/sbin/nft', *args], input=batch, stdout=subprocess.DEVNULL,
                           stderr=subprocess.DEVNULL, check=True, timeout=30)
        return 0
    except (OSError, ValueError, subprocess.SubprocessError):
        print('gateway_nft_load_failed', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
