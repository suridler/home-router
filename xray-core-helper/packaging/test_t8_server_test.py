"""Hermetic runner tests: no host units, config files, sockets or reboot."""

import importlib.util
import json
import stat
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location('t8', Path(__file__).with_name('t8-server-test.py'))
t8 = importlib.util.module_from_spec(spec)
spec.loader.exec_module(t8)


class RunnerTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        root = Path(self.directory.name)
        self.audit = root / 'audit'
        self.audit.mkdir()
        self.system = tuple(root / name for name in ('nft', 'sysctl', 'unit'))
        for path in self.system:
            path.write_text('# baseline\n')
            path.chmod(0o644)
        self.working = root / 'working'
        self.working.write_text('# original working template\n')
        self.working.chmod(0o600)
        self.original = self.working.read_bytes()
        self.boot = root / 'boot'
        self.boot.write_text('boot-one\n')
        for name, value in (('SYSTEM', self.system), ('WORKING', self.working), ('BOOT', self.boot)):
            patcher = patch.object(t8, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        # copy owner is unchanged for the unprivileged test process.
        patcher = patch.object(t8.os, 'chown')
        patcher.start()
        self.addCleanup(patcher.stop)
        self.runner = t8.Runner(self.audit)
        self.pending = None
        self.calls = []
        self.restart_count = 0
        self.apply_count = 0
        self.partial = False
        self.fail_apply = False
        patcher = patch.object(t8, 'api', side_effect=self.api)
        patcher.start()
        self.addCleanup(patcher.stop)
        patcher = patch.object(t8, 'command', side_effect=self.command)
        patcher.start()
        self.addCleanup(patcher.stop)

    def command(self, *args):
        if args == ('systemctl', 'restart', t8.SERVICES[0]):
            self.restart_count += 1
            return ''
        raise AssertionError(f'Unexpected command: {args}')

    def api(self, path, body=None, method=None, timeout=180):
        self.calls.append((path, body, method))
        if path == 'safe-apply':
            return 200, dict(pending=self.pending)
        if path == 'health':
            return 200, dict(status='ok')
        if path == 'templates/sysctl/backups':
            return 200, dict(generation=20 + self.apply_count, completedGeneration=10 + self.apply_count)
        if path == 'templates/sysctl/apply':
            if self.fail_apply:
                return 409, dict(error=dict(code='generation_conflict'))
            self.apply_count += 1
            self.assertEqual(body, dict(expectedGeneration=9 + self.apply_count))
            self.system[1].write_bytes(self.working.read_bytes())
            self.pending = dict(transactionId=f'tx-{self.apply_count}', phase='applied',
                                state='pending', rebootRequired=False, recoveryRequired=False)
            return 200, dict(applied=True, transactionId=self.pending['transactionId'])
        if path.endswith('/confirm'):
            self.assertEqual(path, 'safe-apply/tx-1/confirm')
            self.pending = None
            return 200, {}
        if path.endswith('/rollback'):
            self.assertEqual(path, 'safe-apply/tx-2/rollback')
            self.system[1].write_text('# baseline\n')
            self.pending.update(phase='rolling_back' if self.partial else 'awaiting_reboot',
                                rebootRequired=not self.partial, recoveryRequired=self.partial)
            return 200, dict(transaction=self.pending)
        if path == 'ready':
            return 503, dict(status='degraded', checks=[dict(code='reboot_required')])
        if path == 'templates/refresh':
            self.assertIsNone(body)
            self.assertEqual(method, 'POST')
            return 200, dict(templates=[])
        raise AssertionError(f'Unexpected API call: {path}')

    def smoke(self):
        # Tests below focus on state transitions; preflight gates tested separately.
        with patch.object(self.runner, 'check'):
            self.runner.smoke(False)

    def test_confirm_rollback_restart_and_new_boot_restore(self):
        self.smoke()
        self.assertEqual(self.restart_count, 1)
        state = json.loads((self.audit / 'state.json').read_text())
        self.assertEqual(state['phase'], 'awaiting_reboot')
        self.assertEqual(self.boot.read_text(), 'boot-one\n')
        self.assertNotEqual(self.working.read_bytes(), self.original)
        self.boot.write_text('boot-two\n')
        self.pending = None  # Helper completes recovery on the later boot.
        with patch.object(self.runner, 'check'):
            self.runner.post_reboot()
        self.assertEqual(self.working.read_bytes(), self.original)
        self.assertEqual(json.loads((self.audit / 'state.json').read_text())['phase'], 'complete')

    def test_same_boot_cannot_clear_recovery_or_restore_template(self):
        self.smoke()
        before = self.working.read_bytes()
        with self.assertRaisesRegex(t8.Failure, 'Boot ID unchanged'):
            self.runner.post_reboot()
        self.assertEqual(self.working.read_bytes(), before)

    def test_boot_permissions_keep_private_fixture_and_original(self):
        original_metadata = t8.fingerprint(self.working)
        self.smoke()
        fixture = t8.fingerprint(self.working)
        self.assertEqual(fixture['mode'], 0o600)
        self.assertEqual((fixture['uid'], fixture['gid']),
                         (original_metadata['uid'], original_metadata['gid']))
        self.assertEqual(stat.S_IMODE(self.system[1].stat().st_mode), 0o644)
        # systemd-tmpfiles reapplies the installed working-file mode at boot.
        self.working.chmod(0o600)
        self.boot.write_text('boot-two\n')
        self.pending = None
        with patch.object(self.runner, 'check'):
            self.runner.post_reboot()
        self.assertEqual(t8.fingerprint(self.working), original_metadata)

    def test_operator_template_mode_change_is_preserved(self):
        self.smoke()
        self.boot.write_text('boot-two\n')
        self.pending = None
        self.working.chmod(0o640)
        with self.assertRaisesRegex(t8.Failure, 'Working template changed'):
            self.runner.post_reboot()
        self.assertEqual(stat.S_IMODE(self.working.stat().st_mode), 0o640)

    def test_partial_restore_never_restarts_helper_or_marks_reboot_ready(self):
        self.partial = True
        with self.assertRaisesRegex(t8.Failure, 'Rollback incomplete'):
            self.smoke()
        self.assertEqual(self.restart_count, 0)
        self.assertNotEqual(json.loads((self.audit / 'state.json').read_text())['phase'], 'awaiting_reboot')

    def test_failed_apply_does_not_reuse_transaction_or_confirm(self):
        self.fail_apply = True
        with self.assertRaisesRegex(t8.Failure, 'HTTP 409'):
            self.smoke()
        self.assertFalse(any(path.endswith('/confirm') or path.endswith('/rollback')
                             for path, _, _ in self.calls))
        self.assertTrue((self.audit / 'sysctl-working.before').exists())

    def test_changed_baseline_blocks_post_reboot(self):
        self.smoke()
        self.boot.write_text('boot-two')
        self.system[0].write_text('external change')
        with self.assertRaisesRegex(t8.Failure, 'Baseline content'):
            self.runner.post_reboot()

    def test_operator_template_edit_is_preserved(self):
        self.smoke()
        self.boot.write_text('boot-two')
        self.pending = None
        self.working.write_text('operator edit')
        with self.assertRaisesRegex(t8.Failure, 'Working template changed'):
            self.runner.post_reboot()
        self.assertEqual(self.working.read_text(), 'operator edit')

    def test_repeat_smoke_keeps_original_evidence(self):
        self.smoke()
        before = (self.audit / 'state.json').read_bytes()
        with self.assertRaisesRegex(t8.Failure, 'Smoke already started'):
            self.smoke()
        self.assertEqual((self.audit / 'state.json').read_bytes(), before)

    def test_preflight_failure_prevents_any_file_mutation(self):
        with patch.object(self.runner, 'check', side_effect=t8.Failure('not ready')):
            with self.assertRaisesRegex(t8.Failure, 'not ready'):
                self.runner.smoke(False)
        self.assertEqual(self.working.read_bytes(), self.original)
        self.assertFalse((self.audit / 'state.json').exists())

    def test_each_service_must_be_active(self):
        def service_state(*args):
            return 'inactive' if args[-1] == t8.SERVICES[1] else 'active'
        with patch.object(t8, 'command', side_effect=service_state):
            with self.assertRaisesRegex(t8.Failure, 'xrayui-web.service is not active'):
                self.runner.services()

    def test_degraded_ready_is_saved_and_blocks_apply(self):
        with self.assertRaisesRegex(t8.Failure, 'HTTP 503'):
            self.runner.ready()
        saved = json.loads((self.audit / 'ready.json').read_text())
        self.assertEqual(saved['body']['checks'][0]['code'], 'reboot_required')

    def test_pending_and_malformed_status_fail_closed(self):
        for value in ({}, dict(pending=dict(transactionId='other'))):
            with self.subTest(value=value), patch.object(t8, 'api', return_value=(200, value)):
                with self.assertRaisesRegex(t8.Failure, 'Pending safe-apply'):
                    self.runner.no_pending()

    def test_symlink_baseline_is_rejected(self):
        self.system[0].unlink()
        self.system[0].symlink_to(self.working)
        with self.assertRaisesRegex(t8.Failure, 'Regular file required'):
            t8.fingerprint(self.system[0])

    def test_timeout_uses_timer_without_rollback_request(self):
        original_pending = self.runner.pending

        def timer_fired(transaction):
            # Simulate timer restoring files before the next status read.
            if self.pending and self.pending['transactionId'] == 'tx-2':
                self.system[1].write_text('# baseline\n')
                self.pending.update(phase='awaiting_reboot', rebootRequired=True)
            return original_pending(transaction)

        with patch.object(self.runner, 'check'), patch.object(self.runner, 'pending', side_effect=timer_fired):
            self.runner.smoke(True)
        self.assertFalse(any(path.endswith('/rollback') for path, _, _ in self.calls))
        self.assertEqual(json.loads((self.audit / 'state.json').read_text())['phase'], 'awaiting_reboot')

    def test_post_reboot_pending_blocks_template_restore(self):
        self.smoke()
        self.boot.write_text('boot-two')
        fixture = self.working.read_bytes()
        with self.assertRaisesRegex(t8.Failure, 'Pending safe-apply'):
            self.runner.post_reboot()
        self.assertEqual(self.working.read_bytes(), fixture)

    def test_degraded_post_reboot_can_be_retried_after_working_restore(self):
        self.smoke()
        self.boot.write_text('boot-two')
        self.pending = None
        with patch.object(self.runner, 'check', side_effect=t8.Failure('unrelated drift')):
            with self.assertRaisesRegex(t8.Failure, 'unrelated drift'):
                self.runner.post_reboot()
        self.assertEqual(self.working.read_bytes(), self.original)
        self.assertEqual(json.loads((self.audit / 'state.json').read_text())['phase'], 'working_restored')
        with patch.object(self.runner, 'check'):
            self.runner.post_reboot()
        self.assertEqual(json.loads((self.audit / 'state.json').read_text())['phase'], 'complete')

    def test_confinement_rejects_daemon_proc_and_modified_oneshot(self):
        daemon = 'ProtectKernelTunables=yes\nReadWritePaths=/etc/xray-core-helper\n'
        oneshot = ('ReadWritePaths=/proc/sys\nProtectKernelTunables=no\n'
                   'ExecStart={ path=/usr/sbin/sysctl ; '
                   'argv[]=/usr/sbin/sysctl -p /etc/sysctl.d/99-xray.conf ; ignore_errors=no ; }\n')
        for daemon_properties, oneshot_properties, succeeds in (
            (daemon, oneshot, True),
            (daemon.replace('/etc/xray-core-helper', '/proc/sys'), oneshot, False),
            (daemon.replace('yes', 'no'), oneshot, False),
            (daemon, oneshot.replace('-p ', '--system '), False),
            (daemon, oneshot.replace('ReadWritePaths=/proc/sys', 'ReadWritePaths=/proc'), False),
        ):
            with self.subTest(daemon=daemon_properties, oneshot=oneshot_properties):
                with patch.object(t8, 'command', side_effect=[daemon_properties, oneshot_properties]):
                    if succeeds:
                        self.runner.confinement()
                    else:
                        with self.assertRaises(t8.Failure):
                            self.runner.confinement()


class PreflightStatusTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.audit = Path(self.directory.name)
        self.runner = t8.Runner(self.audit)

    @staticmethod
    def event(identifier=1, message='failure', observed_at='2026-01-01T00:00:00Z'):
        return dict(id=identifier, message=message, observedAt=observed_at)

    @staticmethod
    def probe(stage, *, reachable=True, http_status=200, expected_status=200,
              https_status='ok', lan_forwarding='requires_client_test'):
        return dict(stage=stage, reachable=reachable, httpStatus=http_status,
                    expectedStatus=expected_status, https=dict(status=https_status),
                    lanForwarding=lan_forwarding)

    def test_error_history_empty_and_duplicate_identical_messages_accepted(self):
        histories = (
            dict(capacity=100, errors=[]),
            dict(capacity=100, errors=[self.event(2, 'same message'),
                                       self.event(1, 'same message')]),
        )
        for history in histories:
            with self.subTest(history=history), patch.object(
                    self.runner, 'get', return_value=history) as get:
                self.runner.error_history()
                get.assert_called_once_with('errors', 'error-history')

    def test_error_history_malformed_and_boolean_ids_rejected(self):
        cases = (
            ('non-object', []),
            ('missing capacity', dict(errors=[])),
            ('boolean capacity', dict(capacity=True, errors=[])),
            ('wrong capacity', dict(capacity=99, errors=[])),
            ('missing list', dict(capacity=100)),
            ('non-list errors', dict(capacity=100, errors={})),
            ('too many errors', dict(capacity=100,
                                     errors=[self.event(n) for n in range(1, 102)])),
            ('non-object entry', dict(capacity=100, errors=['event'])),
            ('boolean id', dict(capacity=100, errors=[self.event(True)])),
            ('zero id', dict(capacity=100, errors=[self.event(0)])),
            ('negative id', dict(capacity=100, errors=[self.event(-1)])),
            ('float id', dict(capacity=100, errors=[self.event(1.0)])),
            ('string id', dict(capacity=100, errors=[self.event('1')])),
            ('duplicate id', dict(capacity=100, errors=[self.event(1, 'first'),
                                                        self.event(1, 'second')])),
            ('non-string message', dict(capacity=100, errors=[self.event(1, None)])),
            ('missing observedAt', dict(capacity=100,
                                        errors=[dict(id=1, message='failure')])),
            ('non-string observedAt', dict(capacity=100,
                                           errors=[self.event(1, 'failure', None)])),
        )
        for label, history in cases:
            with self.subTest(label), patch.object(self.runner, 'get', return_value=history):
                with self.assertRaises(t8.Failure):
                    self.runner.error_history()

    def test_error_history_uses_runner_get_and_saves_audit(self):
        payload = dict(capacity=100, errors=[self.event(7)])
        with patch.object(t8, 'api', return_value=(200, payload)) as api:
            self.runner.error_history()
        api.assert_called_once_with('errors', None, method=None)
        saved = json.loads((self.audit / 'error-history.json').read_text())
        self.assertEqual(saved, dict(httpStatus=200, body=payload))

    def test_network_probe_empty_history_accepted(self):
        with patch.object(self.runner, 'get', return_value={}) as get:
            self.runner.network_probe_status()
            get.assert_called_once_with('network/probe', 'network-probe-status')

    def test_network_probe_valid_history_accepted(self):
        history = dict(
            before=self.probe('before', reachable=False, http_status=0, https_status='failed'),
            after=dict(self.probe('after'), transactionId='tx-1'),
            latest=self.probe('manual'),
        )
        with patch.object(self.runner, 'get', return_value=history):
            self.runner.network_probe_status()

    def test_network_probe_false_lan_success_rejected(self):
        history = dict(latest=self.probe('manual', lan_forwarding='verified'))
        with patch.object(self.runner, 'get', return_value=history):
            with self.assertRaisesRegex(t8.Failure, 'LAN client test'):
                self.runner.network_probe_status()

    def test_network_probe_reachable_with_mismatched_http_rejected(self):
        for label, result in (
            ('error status', self.probe('manual', http_status=500)),
            ('different expected status', self.probe('manual', http_status=204, expected_status=200)),
            ('failed https', self.probe('manual', https_status='failed')),
        ):
            with self.subTest(label), patch.object(
                    self.runner, 'get', return_value=dict(latest=result)):
                with self.assertRaisesRegex(t8.Failure, 'expected HTTPS success'):
                    self.runner.network_probe_status()

    def test_network_probe_after_requires_transaction(self):
        for key in ('after', 'latest'):
            for transaction in (None, '', '   '):
                result = self.probe('after')
                if transaction is not None:
                    result['transactionId'] = transaction
                with self.subTest(key=key, transaction=transaction), patch.object(
                        self.runner, 'get', return_value={key: result}):
                    with self.assertRaisesRegex(t8.Failure, 'transaction ID'):
                        self.runner.network_probe_status()

    def test_network_probe_malformed_stages_and_fields_rejected(self):
        cases = (
            ('before stage', {'before': self.probe('after')}),
            ('after stage', {'after': dict(self.probe('before'), transactionId='tx-1')}),
            ('latest stage', {'latest': self.probe('other')}),
            ('non-string stage', {'latest': dict(self.probe('manual'), stage=[])}),
            ('non-object entry', {'before': 'result'}),
            ('boolean reachable', {'latest': dict(self.probe('manual'), reachable=1)}),
            ('boolean http status', {'latest': self.probe('manual', http_status=True)}),
            ('http status range', {'latest': self.probe('manual', http_status=600)}),
            ('expected status range', {'latest': self.probe('manual', expected_status=199)}),
            ('missing https', {'latest': dict(self.probe('manual'), https=None)}),
        )
        for label, history in cases:
            with self.subTest(label), patch.object(self.runner, 'get', return_value=history):
                with self.assertRaises(t8.Failure):
                    self.runner.network_probe_status()

    def test_network_probe_status_uses_runner_get_and_saves_audit(self):
        payload = dict(before=self.probe('before', reachable=False, http_status=0,
                                         https_status='failed'))
        with patch.object(t8, 'api', return_value=(200, payload)):
            self.runner.network_probe_status()
        saved = json.loads((self.audit / 'network-probe-status.json').read_text())
        self.assertEqual(saved, dict(httpStatus=200, body=payload))


class InstallationPathsTest(unittest.TestCase):
    def test_custom_socket_and_discovered_paths(self):
        descriptor = dict(unit='xray.service', binary='/opt/xray/xray',
                          config='/etc/custom-xray/config.json', assetDir='/usr/local/share/xray')
        with patch.object(t8, 'SOCKET'), patch.object(t8, 'INSTALLATION'), patch.object(
                t8, 'command', side_effect=[json.dumps(dict(socket='/run/xray-core-helper/custom.sock')), json.dumps(descriptor)]):
            result = t8.load_installation()
            self.assertEqual(str(t8.SOCKET), '/run/xray-core-helper/custom.sock')
            self.assertEqual(result['config'], descriptor['config'])

    def test_changed_installation_blocks_baseline_comparison(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(t8, 'INSTALLATION', dict(config='/etc/new.json')):
            runner = t8.Runner(Path(directory))
            with self.assertRaisesRegex(t8.Failure, 'Installation paths changed'):
                runner.baseline_matches(dict(installation=dict(config='/etc/old.json')))


class NetworkGateTest(unittest.TestCase):
    def test_gate_records_readiness_without_claiming_forwarding(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(t8, 'command', return_value=json.dumps(dict(status='ready', forwarding='unverified'))) as command:
            root = Path(directory)
            t8.Runner(root).network_xray_ready()
            command.assert_called_once_with(t8.HELPER_CLI, 'network-check')
            self.assertEqual(json.loads((root / 'network-check.json').read_text())['forwarding'], 'unverified')

    def test_gate_rejects_missing_or_false_forwarding_claim(self):
        with tempfile.TemporaryDirectory() as directory:
            for value in ({'status': 'ready'}, {'status': 'ready', 'forwarding': 'verified'}, {'status': 'failed', 'forwarding': 'unverified'}):
                with patch.object(t8, 'command', return_value=json.dumps(value)), self.assertRaises(t8.Failure):
                    t8.Runner(Path(directory)).network_xray_ready()


if __name__ == '__main__':
    unittest.main()
