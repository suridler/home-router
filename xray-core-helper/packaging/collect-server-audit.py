#!/usr/bin/env python3
"""Read-only privileged host inventory. No config, database or log contents."""
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import stat
import subprocess
import tempfile
from datetime import datetime, timezone


UNITS = ('xray.service', 'xray-tproxy-routing.service',
         'xray-core-helper.service', 'xrayui-web.service',
         'update-xray-geodata.service', 'update-xray-geodata.timer',
         'xray-core-helper-errors.timer', 'xray-core-helper-routing.timer',
         'xray-core-helper-rollback.timer')


def run(args):
    try:
        result = subprocess.run(args, capture_output=True, text=True,
                                timeout=20, check=False,
                                env=dict(os.environ, LC_ALL='C', SYSTEMD_PAGER='cat'))
        # Error output may contain command arguments or private paths.
        return {'exit_code': result.returncode, 'stdout': result.stdout}
    except FileNotFoundError:
        return {'exit_code': None, 'error': 'command_unavailable'}
    except subprocess.TimeoutExpired:
        return {'exit_code': None, 'error': 'timeout'}


def cron_entries(text, system=False):
    entries = []
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith('#') or re.match(r'^[\w]+\s*=', line):
            continue
        fields = line.split(None, 1 if line.startswith('@') else 5)
        count = 1 if line.startswith('@') else 5
        if len(fields) <= count:
            continue
        schedule, command = ' '.join(fields[:count]), fields[count]
        if system:
            parts = command.split(None, 1)
            if len(parts) != 2:
                continue
            command = parts[1]  # Do not retain command bodies or environment.
        try:
            tokens = shlex.split(command)
        except ValueError:
            tokens = []
        executable = tokens[0] if tokens else ''
        if not re.fullmatch(r'[/A-Za-z0-9_.+-]{1,256}', executable):
            executable = '(compound command)'
        entries.append({'schedule': schedule, 'executable': executable,
                        'command_sha256': hashlib.sha256(command.encode()).hexdigest(),
                        'related': [key for key in ('xray', 'geodata', 'geosite', 'geoip', 'proxy-domain')
                                    if key in command.lower()]})
    return entries


def service_inventory():
    units = {}
    properties = ('LoadState', 'ActiveState', 'SubState', 'Result', 'ExecMainStatus',
                  'FragmentPath', 'DropInPaths', 'User', 'Group', 'UnitFileState')
    for unit in UNITS:
        result = run(['systemctl', 'show', '--no-pager', unit,
                      *['--property=' + p for p in properties]])
        units[unit] = {'exit_code': result['exit_code']}
        if result['exit_code'] == 0:
            units[unit].update(line.split('=', 1) for line in result['stdout'].splitlines() if '=' in line)
    hooks = run(['systemctl', 'show', '--no-pager', 'xray.service',
                 '-p', 'ExecStartPre', '-p', 'ExecStartPost', '-p', 'ExecStopPost'])
    units['xray.service']['legacy_tproxy_hooks'] = 'xray-tproxy-routing' in hooks.get('stdout', '')
    units['xray.service']['legacy_domain_hook'] = 'xray-proxy-domain' in hooks.get('stdout', '')
    units['xray.service']['hook_query_exit_code'] = hooks['exit_code']
    return units


def main():
    if os.geteuid() != 0:
        raise SystemExit('Run with sudo python3 collect-server-audit.py')
    os.umask(0o077)
    output = Path(tempfile.mkdtemp(prefix='xray-server-audit-', dir='/var/tmp'))
    summary = {'collected_at': datetime.now(timezone.utc).isoformat(),
               'host': os.uname().nodename, 'services': service_inventory()}
    commands = {
        'nft-ruleset.json': ['nft', '-j', 'list', 'ruleset'],
        'ip-address.json': ['ip', '-j', '-4', 'address', 'show'],
        'ip-rules.json': ['ip', '-j', '-4', 'rule', 'show'],
        'ip-routes.json': ['ip', '-j', '-4', 'route', 'show', 'table', 'all'],
        'timers.txt': ['systemctl', 'list-timers', '--all', '--no-pager'],
    }
    summary['commands'] = {}
    for name, command in commands.items():
        result = run(command)
        summary['commands'][name] = {k: v for k, v in result.items() if k != 'stdout'}
        if result['exit_code'] == 0:
            (output / name).write_text(result['stdout'])
    root_cron = run(['crontab', '-u', 'root', '-l'])
    summary['root_cron'] = {'exit_code': root_cron['exit_code'],
                            'entries': cron_entries(root_cron.get('stdout', ''))}
    summary['system_cron'] = {}
    for path in [Path('/etc/crontab'), *sorted(Path('/etc/cron.d').glob('*'))]:
        if path.is_file() and not path.is_symlink():
            summary['system_cron'][str(path)] = cron_entries(path.read_text(), system=True)
    summary['periodic_scripts'] = {}
    for period in ('hourly', 'daily', 'weekly', 'monthly'):
        directory = Path('/etc/cron.' + period)
        summary['periodic_scripts'][str(directory)] = [p.name for p in sorted(directory.glob('*')) if p.is_file()]
    summary['files'] = []
    # Metadata only: never copy Xray JSONC, TOML, databases, tokens or logs.
    for name in ('/usr/local/etc', '/usr/local/etc/xray',
                 '/usr/local/etc/xray/config.json', '/etc/nftables.conf',
                 '/etc/sysctl.d/99-xray.conf', '/etc/xray-core-helper/config.toml',
                 '/usr/local/share/xray/geosite.dat', '/usr/local/share/xray/geoip.dat',
                 '/usr/local/share/xray/geosite_user.dat'):
        path = Path(name)
        try:
            info = path.lstat()
            summary['files'].append({'path': name, 'mode': oct(stat.S_IMODE(info.st_mode)),
                                     'uid': info.st_uid, 'gid': info.st_gid,
                                     'size': info.st_size, 'mtime_ns': info.st_mtime_ns,
                                     'symlink': path.is_symlink()})
        except OSError:
            summary['files'].append({'path': name, 'unavailable': True})
    (output / 'summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
    # Allow the invoking operator to read the private report without more sudo.
    if os.environ.get('SUDO_UID', '').isdigit() and os.environ.get('SUDO_GID', '').isdigit():
        uid, gid = int(os.environ['SUDO_UID']), int(os.environ['SUDO_GID'])
        for path in output.iterdir():
            os.chown(path, uid, gid)
        os.chown(output, uid, gid)
    print('audit=' + str(output))
    print('summary=' + str(output / 'summary.json'))
    for name, status in summary['commands'].items():
        print(('PASS ' if status['exit_code'] == 0 else 'UNAVAILABLE ') + name)
    print('Private report: loaded rules may contain internal network addresses.')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
