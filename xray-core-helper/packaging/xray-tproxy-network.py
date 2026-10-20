#!/usr/bin/python3
"""Native Xray lifecycle adapter. Installed with the reviewed network profile.

No helper imports, config, DB, socket or API. Only the shipped profile's owned
base chains and exact policy route/rule are modified. Unrelated nft tables stay.
"""
import contextlib
import fcntl
import json
import os
from pathlib import Path
import shlex
import stat
import subprocess
import sys

NFT = '/usr/sbin/nft'
IP = '/sbin/ip'
SYSTEMCTL = '/usr/bin/systemctl'
UNIT = 'xray-tproxy-routing.service'
STARTED = Path('/run/xray-tproxy/started')
LOCK = '/run/xray-tproxy/lock'
CONFIG = '/etc/nftables.conf'
LIMIT = 1 << 20
BEGIN = b'# helper:begin gateway'
END = b'# helper:end gateway'
ON = b'flush chain inet xray prerouting\nflush chain inet xray output\nadd rule inet xray prerouting jump prerouting_proxy\nadd rule inet xray output jump output_proxy\n'
OFF = b'flush chain inet xray prerouting\nflush chain inet xray output\n'


class Failure(Exception):
    pass


def run(step, argv, data=None, check=True):
    try:
        result = subprocess.run(argv, input=data, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, timeout=35)
    except (OSError, subprocess.SubprocessError) as error:
        raise Failure(f'{step}: {type(error).__name__}') from error
    if check and result.returncode:
        # Do not copy native diagnostics that may contain config text/secrets.
        raise Failure(f'{step}: exit {result.returncode}')
    return result


def block(raw):
    if len(raw) > LIMIT:
        raise Failure('nft_config_size')
    lines = raw.splitlines(keepends=True)
    first = [i for i, line in enumerate(lines) if line.strip() == BEGIN]
    last = [i for i, line in enumerate(lines) if line.strip() == END]
    if len(first) != 1 or len(last) != 1 or first[0] >= last[0]:
        raise Failure('nft_block_markers')
    return b''.join(lines[first[0] + 1:last[0]])


def read_block():
    with open(CONFIG, 'rb') as stream:
        if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
            raise Failure('nft_config_type')
        return block(stream.read(LIMIT + 1))


def parameters():
    raw = run('routing_environment', [SYSTEMCTL, 'show', UNIT, '--property=Environment', '--value']).stdout
    values = dict(item.split('=', 1) for item in shlex.split(raw.decode()) if '=' in item)
    try:
        mark = int(values['TPROXY_MARK'], 0)
        table = int(values['TPROXY_TABLE'], 0)
    except (ValueError, KeyError) as error:
        raise Failure('routing_parameters') from error
    if not 0 < mark <= 0xffffffff or not 0 < table <= 0xffffffff or table in (253, 254, 255):
        raise Failure('routing_parameters')
    return mark, table


def nft(step, data):
    run(step, [NFT, '--file', '-'], data)


def off(mark, table):
    errors = []
    # Distinguish absent owned table from an actual netlink/listing error.
    try:
        tables = json.loads(run('nft_list_tables', [NFT, '-j', 'list', 'tables']).stdout)
        present = any(item.get('table', {}).get('family') == 'inet' and
                      item.get('table', {}).get('name') == 'xray' for item in tables.get('nftables', []))
        if present:
            nft('nft_off', OFF)
    except (Failure, ValueError) as error:
        errors.append(str(error))
    # Even if nft-off fails, removing this rule returns OUTPUT to main routing.
    try:
        rules = json.loads(run('rule_list', [IP, '-j', '-4', 'rule', 'show']).stdout)
        for rule in rules:
            value = rule.get('fwmark', 0)
            mask = rule.get('fwmask', '0xffffffff')
            if int(str(value), 0) == mark and int(str(mask), 0) == 0xffffffff and str(rule.get('table')) == str(table):
                run('rule_delete', [IP, '-4', 'rule', 'del', 'priority', str(rule['priority']),
                                    'fwmark', str(mark), 'table', str(table)])
    except (Failure, ValueError, KeyError) as error:
        errors.append(str(error))
    try:
        # Dump all tables: an absent dedicated table must not masquerade as an I/O error.
        routes = json.loads(run('route_list', [IP, '-j', '-4', 'route', 'show', 'table', 'all']).stdout)
        for route in routes:
            if (str(route.get('table')) == str(table) and route.get('type') == 'local'
                    and route.get('dst') == 'default' and route.get('dev') == 'lo'):
                run('route_delete', [IP, '-4', 'route', 'del', 'local', 'default', 'dev', 'lo', 'table', str(table)])
    except (Failure, ValueError) as error:
        errors.append(str(error))
    if errors:
        raise Failure('; '.join(errors))


def on(mark, table):
    # Every start executes, including a repeated start after an external nft reload.
    if not STARTED.exists():
        raise Failure('xray_not_started')
    data = read_block()
    run('nft_validate', [NFT, '--check', '--file', '-'], data)
    off(mark, table)
    try:
        run('sysctl_apply', [SYSTEMCTL, 'start', 'xray-tproxy-sysctl.service'])
        nft('nft_profile_load', data)  # The persistent profile has empty base chains.
        run('route_add', [IP, '-4', 'route', 'add', 'local', 'default', 'dev', 'lo', 'table', str(table)])
        run('rule_add', [IP, '-4', 'rule', 'add', 'fwmark', str(mark), 'table', str(table)])
        if not STARTED.exists():
            raise Failure('xray_stopped_during_activation')
        nft('nft_on', ON)  # Both entry points are enabled in one transaction, last.
    except Failure as error:
        try:
            off(mark, table)
        except Failure as cleanup:
            raise Failure(f'{error}; cleanup: {cleanup}') from error
        raise


@contextlib.contextmanager
def locked():
    Path(LOCK).parent.mkdir(mode=0o755, parents=True, exist_ok=True)
    with open(LOCK, 'a') as stream:
        fcntl.flock(stream, fcntl.LOCK_EX)
        yield


def action(name):
    if name == 'xray-start':
        # The marker belongs to Xray lifecycle, not to helper or persistent state.
        with locked():
            STARTED.touch(mode=0o600)
        enabled = run('routing_enabled', [SYSTEMCTL, 'is-enabled', UNIT], check=False)
        state = enabled.stdout.decode().strip()
        if enabled.returncode == 0 and state == 'enabled':
            try:
                run('routing_start', [SYSTEMCTL, 'restart', UNIT])
            except Failure:
                # Cancel any late job before returning a failed Xray post-start.
                run('routing_cancel', [SYSTEMCTL, 'stop', UNIT], check=False)
                with locked():
                    STARTED.unlink(missing_ok=True)
                    off(*parameters())
                raise
        elif state not in ('disabled', 'static', 'indirect'):
            raise Failure('routing_enablement_unavailable')
        return
    if name == 'xray-stop':
        with locked():
            STARTED.unlink(missing_ok=True)
            try:
                off(*parameters())
            except Failure as error:
                failure = error
            else:
                failure = None
        # Never hold the network lock while waiting for a unit whose cleanup uses it.
        result = run('routing_stop', [SYSTEMCTL, 'stop', UNIT], check=False)
        if failure:
            raise failure
        if result.returncode:
            raise Failure(f'routing_stop: exit {result.returncode}')
        return
    if name == 'adopt':
        # Explicit native apply may attach hooks to an already running Xray.
        with locked():
            run('xray_active', [SYSTEMCTL, 'is-active', '--quiet', 'xray.service'])
            STARTED.touch(mode=0o600)
        return
    with locked():
        mark, table = parameters()
        if name == 'on':
            on(mark, table)
        elif name == 'off':
            off(mark, table)
        elif name == 'resume':
            if STARTED.exists() and run('routing_active', [SYSTEMCTL, 'is-active', '--quiet', UNIT], check=False).returncode == 0:
                nft('nft_resume', ON)
        elif name == 'reload':
            if run('routing_active', [SYSTEMCTL, 'is-active', '--quiet', UNIT], check=False).returncode == 0:
                on(mark, table)
        else:
            raise Failure('invalid_action')


def main():
    if len(sys.argv) != 2 or sys.argv[1] not in ('on', 'off', 'adopt', 'resume', 'reload', 'xray-start', 'xray-stop'):
        return 2
    try:
        action(sys.argv[1])
        return 0
    except (Failure, OSError, ValueError) as error:
        print(f'xray_tproxy_{sys.argv[1]}_failed: {error}', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
