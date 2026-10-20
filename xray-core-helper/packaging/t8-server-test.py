#!/usr/bin/env python3
"""Operator-run T8 acceptance for an already installed disposable server.

No installation, adoption, nft loading or automatic reboot. Python stdlib only.
"""

import argparse
import datetime
import fcntl
import grp
import hashlib
import http.client
import ipaddress
import json
import os
from pathlib import Path
import re
import shutil
import socket
import stat
import subprocess
import sys
import tempfile
import time
from urllib.parse import quote


SOCKET = Path('/run/xray-core-helper/helper.sock')
INSTALLATION = None
HELPER_CLI = '/usr/local/bin/xray-core-helper'
WORKING = Path('/etc/xray-core-helper/templConf/sysctl/99-xray.conf')
SYSTEM = (
    Path('/etc/nftables.conf'),
    Path('/etc/sysctl.d/99-xray.conf'),
    Path('/etc/systemd/system/xray-tproxy-routing.service'),
    Path('/etc/systemd/system/xray.service.d/50-tproxy.conf'),
)
BOOT = Path('/proc/sys/kernel/random/boot_id')
SERVICES = ('xray-core-helper.service', 'xrayui-web.service', 'xray.service')


class Failure(Exception):
    pass


def require(condition, message):
    if not condition:
        raise Failure(message)


def safe_error(error):
    return str(error) if isinstance(error, Failure) else type(error).__name__


def command(*args):
    result = subprocess.run(args, capture_output=True, text=True, timeout=45)
    # Never expose raw command output: it may contain configuration or tokens.
    require(result.returncode == 0, f'{args[0]} failed (exit {result.returncode})')
    return result.stdout


def load_installation(cli='/usr/local/bin/xray-core-helper', config='/etc/xray-core-helper/config.toml'):
    global SOCKET, INSTALLATION, HELPER_CLI
    HELPER_CLI = cli
    settings = json.loads(command(cli, '-config', config, 'installation-settings'))
    installation = json.loads(command(cli, 'discover-xray'))
    require(set(installation) == {'unit', 'binary', 'config', 'assetDir'}, 'Invalid discovery projection')
    for name in ('binary', 'config', 'assetDir'):
        path = installation[name]
        require(isinstance(path, str) and Path(path).is_absolute() and '\n' not in path, 'Invalid discovered path')
    require(isinstance(settings.get('socket'), str) and Path(settings['socket']).is_absolute(), 'Invalid helper socket')
    SOCKET = Path(settings['socket'])
    INSTALLATION = dict(installation, socket=str(SOCKET))
    return INSTALLATION


class UnixHTTP(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(str(SOCKET))


def api(path, body=None, method=None, timeout=180):
    connection = UnixHTTP('helper', timeout=timeout)
    try:
        connection.request(method or ('GET' if body is None else 'POST'), '/v1/' + path,
                           body=None if body is None else json.dumps(body),
                           headers={'Content-Type': 'application/json'})
        response = connection.getresponse()
        data = response.read(2 * 1024 * 1024 + 1)
        require(len(data) <= 2 * 1024 * 1024, 'API response too large')
        value = json.loads(data)
        require(isinstance(value, dict), 'Invalid API object')
        return response.status, value
    finally:
        connection.close()


def write_json(path, value):
    # Atomic progress is needed even if interrupted between apply and rollback.
    temporary = path.with_suffix('.tmp')
    with temporary.open('w') as stream:
        json.dump(value, stream, indent=2)
        stream.write('\n')
        stream.flush()
        os.fsync(stream.fileno())
    temporary.replace(path)
    descriptor = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def fingerprint(path):
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode), f'Regular file required: {path}')
    return dict(sha256=hashlib.sha256(path.read_bytes()).hexdigest(),
                mode=stat.S_IMODE(info.st_mode), uid=info.st_uid, gid=info.st_gid)


def restore_working(source, *, preserve_metadata=False):
    working = fingerprint(WORKING)
    metadata = working if preserve_metadata else fingerprint(source)
    descriptor, name = tempfile.mkstemp(prefix='.t8-', dir=WORKING.parent)
    try:
        with os.fdopen(descriptor, 'wb') as stream:
            stream.write(source.read_bytes())
            stream.flush()
            os.fchown(stream.fileno(), metadata['uid'], metadata['gid'])
            os.fchmod(stream.fileno(), metadata['mode'])
            os.fsync(stream.fileno())
        os.replace(name, WORKING)
    finally:
        if os.path.exists(name):
            os.unlink(name)


class Runner:
    def __init__(self, audit):
        self.audit = audit
        self.results = []

    def probe(self, name, action):
        try:
            action()
            self.results.append(dict(check=name, status='pass'))
            print(f'PASS {name}', flush=True)
        except (Failure, OSError, ValueError, http.client.HTTPException,
                subprocess.SubprocessError) as error:
            # Only our own stable messages go into the report.
            message = safe_error(error)
            self.results.append(dict(check=name, status='fail', reason=message))
            print(f'FAIL {name}: {message}', flush=True)

    def get(self, path, label, body=None, statuses=(200,), method=None):
        status_code, data = api(path, body, method=method)
        write_json(self.audit / (label + '.json'), dict(httpStatus=status_code, body=data))
        require(status_code in statuses, f'{label}: HTTP {status_code}; see audit JSON')
        return data

    def wait_health(self):
        deadline = time.monotonic() + 60
        while True:
            try:
                code, data = api('health', timeout=2)
                if code == 200 and data.get('status') == 'ok':
                    return
            except (OSError, ValueError, http.client.HTTPException):
                pass
            require(time.monotonic() < deadline, 'Healthy helper API not available after 60 seconds')
            time.sleep(1)

    def services(self):
        # systemctl is-active with multiple units succeeds if ANY one is active.
        for service in SERVICES:
            try:
                active = command('systemctl', 'is-active', service).strip() == 'active'
            except Failure:
                active = False
            require(active, f'{service} is not active')

    def socket_permissions(self):
        info = SOCKET.stat()
        require(stat.S_ISSOCK(info.st_mode) and info.st_uid == 0
                and info.st_gid == grp.getgrnam('xray-core-helper').gr_gid
                and stat.S_IMODE(info.st_mode) == 0o660, 'Socket must be root:xray-core-helper 660')

    def confinement(self):
        daemon = command('systemctl', 'show', SERVICES[0],
                         '-p', 'ProtectKernelTunables', '-p', 'ReadWritePaths')
        write_json(self.audit / 'daemon-confinement.json', dict(properties=daemon))
        require('ProtectKernelTunables=yes' in daemon.splitlines(),
                'Daemon kernel protection is disabled')
        writes = next((line.split('=', 1)[1] for line in daemon.splitlines()
                       if line.startswith('ReadWritePaths=')), '')
        require(not re.search(r'(^|\s)[+\-]?/proc(?:/|\s|$)', writes),
                'Daemon has writable /proc scope')
        oneshot = command('systemctl', 'show', 'xray-core-helper-sysctl.service',
                          '-p', 'ExecStart', '-p', 'ReadWritePaths', '-p', 'ProtectKernelTunables')
        write_json(self.audit / 'sysctl-confinement.json', dict(properties=oneshot))
        require('ReadWritePaths=/proc/sys' in oneshot.splitlines(),
                'Sysctl oneshot writable scope must be /proc/sys')
        require('ProtectKernelTunables=no' in oneshot.splitlines(),
                'Sysctl oneshot cannot write kernel tunables')
        actions = [line for line in oneshot.splitlines() if line.startswith('ExecStart=')]
        require(len(actions) == 1 and actions[0].count('argv[]=') == 1
                and 'path=/usr/sbin/sysctl ;' in actions[0]
                and 'argv[]=/usr/sbin/sysctl -p /etc/sysctl.d/99-xray.conf ;' in actions[0],
                'Sysctl oneshot ExecStart differs from the fixed action')

    def dry_run(self):
        require('--dry-run' in command('/usr/sbin/sysctl', '--help'),
                'sysctl lacks --dry-run; apply blocked (check sysctl --version)')

    def no_pending(self):
        data = self.get('safe-apply', 'safe-apply')
        require('pending' in data and data['pending'] is None, 'Pending safe-apply; resolve it first')

    def ready(self):
        data = self.get('ready', 'ready')
        require(data.get('status') == 'ready', 'Readiness is degraded; inspect ready.json; no auto-adopt')

    def error_history(self):
        data = self.get('errors', 'error-history')
        require(isinstance(data, dict), 'Invalid error history object')
        require(type(data.get('capacity')) is int and data['capacity'] == 100,
                'Invalid error history capacity')
        errors = data.get('errors')
        require(isinstance(errors, list) and len(errors) <= 100, 'Invalid error history list')
        identifiers = set()
        for entry in errors:
            require(isinstance(entry, dict), 'Invalid error history entry')
            identifier = entry.get('id')
            require(type(identifier) is int and identifier > 0 and identifier not in identifiers,
                    'Invalid error history ID')
            identifiers.add(identifier)
            require(isinstance(entry.get('message'), str), 'Invalid error history message')
            require(isinstance(entry.get('observedAt'), str), 'Invalid error history timestamp')

    def network_probe_status(self):
        data = self.get('network/probe', 'network-probe-status')
        require(isinstance(data, dict), 'Invalid network probe history object')
        for name, stages in (('before', ('before',)), ('after', ('after',)),
                             ('latest', ('manual', 'before', 'after'))):
            if name not in data:
                continue
            result = data[name]
            require(isinstance(result, dict), f'Invalid {name} network probe result')
            require(result.get('stage') in stages, f'Invalid {name} network probe stage')
            reachable = result.get('reachable')
            require(type(reachable) is bool, f'Invalid {name} network probe reachable flag')
            require(result.get('lanForwarding') == 'requires_client_test',
                    f'{name} network probe must require a LAN client test')
            http_status = result.get('httpStatus')
            require(type(http_status) is int and 0 <= http_status <= 599,
                    f'Invalid {name} network probe HTTP status')
            expected = result.get('expectedStatus')
            require(type(expected) is int and 200 <= expected <= 299,
                    f'Invalid {name} network probe expected status')
            if reachable:
                https = result.get('https')
                require(isinstance(https, dict) and https.get('status') == 'ok'
                        and http_status == expected,
                        f'{name} network probe reachable result lacks expected HTTPS success')
            if result['stage'] == 'after':
                transaction = result.get('transactionId')
                require(isinstance(transaction, str) and transaction.strip(),
                        'After network probe lacks a transaction ID')

    def web(self):
        data = self.get('web/settings', 'web-settings')
        require(data.get('enabled') is True, 'This T8 scenario requires enabled web')
        user = command('systemctl', 'show', SERVICES[1], '-p', 'User', '--value').strip()
        require(user not in ('', 'root', '0'), 'Web must have an unprivileged systemd User')
        host = str(ipaddress.ip_address(data['host']))
        host = {'0.0.0.0': '127.0.0.1', '::': '::1'}.get(host, host)
        port = data['port']
        require(type(port) is int and 1 <= port <= 65535, 'Invalid web port')
        connection = http.client.HTTPConnection(host, port, timeout=10)
        try:
            connection.request('GET', '/')
            response = connection.getresponse()
            require(response.status == 200, f'Web root: HTTP {response.status}')
        finally:
            connection.close()

    def network_xray_ready(self):
        result = json.loads(command(HELPER_CLI, 'network-check'))
        require(result.get('status') == 'ready' and result.get('forwarding') == 'unverified',
                'Xray network gate is not ready; no LAN forwarding proof')
        write_json(self.audit / 'network-check.json', {
            'status': result['status'], 'forwarding': result['forwarding']})

    def check(self):
        for name, action in (
            ('services', self.services), ('health', self.wait_health),
            ('socket-permissions', self.socket_permissions), ('safe-apply-clean', self.no_pending),
            ('ready', self.ready), ('error-history', self.error_history),
            ('network-probe-status', self.network_probe_status),
            ('network-xray-ready', self.network_xray_ready),
            ('templates', lambda: self.get('templates', 'templates')),
            ('confinement', self.confinement), ('sysctl-dry-run', self.dry_run),
            ('web-http', self.web),
        ):
            self.probe(name, action)
        require(all(item['status'] == 'pass' for item in self.results),
                'Preflight failed; inspect report-check.json (or report-smoke.json) and endpoint JSON')

    def baseline_matches(self, state):
        require(state.get('installation') == INSTALLATION, 'Installation paths changed after smoke; review manually')
        for name, expected in state.get('installationFiles', {}).items():
            path = Path(name)
            actual = fingerprint(path) if path.exists() else None
            require(actual == expected, 'Xray config/assets changed after smoke; review manually')
        for path in SYSTEM:
            require(fingerprint(path) == state['baseline'][str(path)],
                    f'Baseline content/permissions/owner mismatch: {path}')

    def pending(self, transaction):
        pending = self.get('safe-apply', 'safe-apply-current').get('pending')
        require(isinstance(pending, dict) and pending.get('transactionId') == transaction,
                'Expected pending transaction is missing or changed')
        return pending

    def awaiting(self, transaction):
        pending = self.pending(transaction)
        require(pending.get('phase') == 'awaiting_reboot' and pending.get('state') == 'pending'
                and pending.get('rebootRequired') is True and pending.get('recoveryRequired') is False,
                'Rollback incomplete; do not reboot; inspect safe-apply-current.json')

    def smoke(self, timeout_rollback):
        require(not (self.audit / 'state.json').exists(),
                'Smoke already started in this audit directory; retain evidence and inspect pending')
        self.check()
        baseline = {str(path): fingerprint(path) for path in SYSTEM}
        working = fingerprint(WORKING)
        shutil.copy2(WORKING, self.audit / 'sysctl-working.before')
        os.chown(self.audit / 'sysctl-working.before', working['uid'], working['gid'])
        installation_files = {}
        if INSTALLATION:
            paths = [Path(INSTALLATION['config'])] + [Path(INSTALLATION['assetDir']) / name for name in ('geoip.dat', 'geosite.dat')]
            installation_files = {str(path): fingerprint(path) if path.exists() else None for path in paths}
        state = dict(bootId=BOOT.read_text().strip(), baseline=baseline, installation=INSTALLATION, installationFiles=installation_files,
                     working=working, phase='prepared')
        write_json(self.audit / 'state.json', state)
        # The operator reviewed this fixed system file before authorizing smoke.
        # The working file has its own permissions (0600 in tmpfiles), even
        # when the system source is 0644. Boot must not change the fixture.
        restore_working(SYSTEM[1], preserve_metadata=True)
        for cycle in ('confirm', 'rollback'):
            print(f'Sysctl cycle: apply/{cycle}', flush=True)
            self.no_pending()
            self.baseline_matches(state)
            if cycle == 'rollback':
                with WORKING.open('a') as stream:
                    stream.write('\n# T8 rollback fixture\n')
            generation = self.get('templates/sysctl/backups', 'backups-' + cycle)['completedGeneration']
            require(type(generation) is int and generation >= 0, 'Invalid generation')
            state['phase'] = 'applying-' + cycle
            write_json(self.audit / 'state.json', state)
            result = self.get('templates/sysctl/apply', 'apply-' + cycle,
                              dict(expectedGeneration=generation))
            transaction = result.get('transactionId')
            require(isinstance(transaction, str) and transaction, 'Apply returned no transaction ID')
            require(result.get('applied') is True, 'Sysctl apply was not performed')
            state.update(transactionId=transaction, phase=cycle)
            write_json(self.audit / 'state.json', state)
            route = 'safe-apply/' + quote(transaction, safe='') + '/'
            if cycle == 'confirm':
                self.get(route + 'confirm', 'confirm', {})
                self.no_pending()
            elif timeout_rollback:
                # Bounded wait; timer remains owned by systemd, never confirm here.
                print('Waiting for systemd rollback timer (up to 240 seconds)', flush=True)
                deadline = time.monotonic() + 240
                while self.pending(transaction).get('phase') != 'awaiting_reboot':
                    require(time.monotonic() < deadline, 'Timeout rollback not complete after 240s; do not reboot')
                    time.sleep(2)
            else:
                self.get(route + 'rollback', 'rollback', {})
        self.awaiting(transaction)
        self.baseline_matches(state)
        state['fixture'] = fingerprint(WORKING)
        write_json(self.audit / 'state.json', state)
        command('systemctl', 'restart', SERVICES[0])
        self.wait_health()
        self.awaiting(transaction)
        ready = self.get('ready', 'ready-after-helper-restart', statuses=(503,))
        require(ready.get('status') == 'degraded'
                and any(item.get('code') == 'reboot_required' for item in ready.get('checks', [])),
                'Readiness must report degraded/reboot_required after helper restart')
        self.baseline_matches(state)
        state['phase'] = 'awaiting_reboot'
        write_json(self.audit / 'state.json', state)
        print('PASS grouped baseline and helper restart. Manual reboot required; see T8 guide.', flush=True)

    def post_reboot(self):
        state = json.loads((self.audit / 'state.json').read_text())
        require(state['phase'] in ('awaiting_reboot', 'working_restored'),
                'Pre-reboot smoke did not finish; inspect recovery first')
        require(BOOT.read_text().strip() != state['bootId'], 'Boot ID unchanged; helper restart is not reboot')
        self.baseline_matches(state)
        self.wait_health()
        self.no_pending()
        require(fingerprint(self.audit / 'sysctl-working.before') == state['working'],
                'Saved working template changed; do not restore')
        if state['phase'] == 'awaiting_reboot':
            # Avoid overwriting operator edits made while waiting for reboot.
            require(fingerprint(WORKING) == state['fixture'], 'Working template changed after smoke; review manually')
            restore_working(self.audit / 'sysctl-working.before')
            state['phase'] = 'working_restored'
            write_json(self.audit / 'state.json', state)
        require(fingerprint(WORKING) == state['working'], 'Restored working template changed')
        self.get('templates/refresh', 'templates-refreshed', method='POST')
        self.check()
        state['phase'] = 'complete'
        write_json(self.audit / 'state.json', state)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('phase', choices=('check', 'smoke', 'post-reboot'))
    parser.add_argument('--helper-cli', default='/usr/local/bin/xray-core-helper', help='Trusted installed CLI')
    parser.add_argument('--config', default='/etc/xray-core-helper/config.toml', help='Helper bootstrap config')
    parser.add_argument('--audit', type=Path, help='Existing root-owned 0700 audit directory (required after reboot)')
    parser.add_argument('--start-services', action='store_true', help='Start installed helper/web before check or smoke')
    parser.add_argument('--reviewed-sysctl', action='store_true', help='Authorize sysctl runtime apply after local review and with independent console')
    parser.add_argument('--timeout-rollback', action='store_true', help='Use systemd timeout instead of explicit rollback in cycle 2')
    args = parser.parse_args()
    require(os.geteuid() == 0, 'Run with sudo on the disposable test server')
    require(args.phase != 'post-reboot' or args.audit is not None, 'post-reboot requires --audit')
    require(args.phase != 'smoke' or args.reviewed_sysctl, 'smoke requires --reviewed-sysctl after local file review')
    require(not args.timeout_rollback or args.phase == 'smoke', '--timeout-rollback requires smoke')
    require(not args.start_services or args.phase != 'post-reboot', 'Post-reboot must verify automatic service startup')
    os.umask(0o077)
    # Serialize even when two invocations use different audit directories.
    with open('/run/xray-t8-test.lock', 'a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Failure('Another T8 runner is active') from None
        audit = args.audit or Path(tempfile.mkdtemp(prefix='xray-T8.', dir='/var/backups'))
        info = audit.lstat()
        require(stat.S_ISDIR(info.st_mode) and info.st_uid == 0 and stat.S_IMODE(info.st_mode) == 0o700,
                'Audit directory must be root-owned, mode 0700, and not a symlink')
        print(f'audit={audit}', flush=True)
        runner = Runner(audit)
        success = False
        failure = None
        try:
            load_installation(args.helper_cli, args.config)
            write_json(audit / ('installation-' + args.phase + '.json'), INSTALLATION)
            if args.start_services:
                command('systemctl', 'start', SERVICES[0], SERVICES[1])
            if args.phase == 'check':
                runner.check()
            elif args.phase == 'smoke':
                runner.smoke(args.timeout_rollback)
            else:
                runner.post_reboot()
            success = True
        except Exception as error:
            failure = safe_error(error)
            raise
        finally:
            write_json(audit / ('report-' + args.phase + '.json'),
                       dict(phase=args.phase, success=success, failure=failure, checks=runner.results,
                            capturedAt=datetime.datetime.now(datetime.timezone.utc).isoformat(),
                            manual=['GUI review', 'LAN forwarding', 'reviewed nft/unit scenarios',
                                    'helper crash and partial recovery scenarios']))
            print(f'Report: {audit}/report-{args.phase}.json', flush=True)


if __name__ == '__main__':
    try:
        main()
    except (Failure, OSError, ValueError, KeyError, TypeError, http.client.HTTPException,
            subprocess.SubprocessError) as error:
        print('STOP: ' + safe_error(error)
              + '. Keep audit evidence; inspect safe-apply before further changes.', file=sys.stderr)
        sys.exit(1)
