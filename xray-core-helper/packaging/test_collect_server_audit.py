"""Collector tests use fixtures only; no host services or privileged reads."""
import importlib.util
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    'collector', Path(__file__).with_name('collect-server-audit.py'))
collector = importlib.util.module_from_spec(spec)
spec.loader.exec_module(collector)


class CollectorTest(unittest.TestCase):
    def test_cron_redacts_arguments_environment_and_compound_commands(self):
        source = '''# ignored
TOKEN=do-not-save
0 4 * * * /usr/local/bin/update-geodata --password hidden
@reboot /bin/sh -c 'curl https://user:password@example.invalid/'
bad line
'''
        records = collector.cron_entries(source)
        self.assertEqual(len(records), 2)
        self.assertEqual(records[0]['schedule'], '0 4 * * *')
        self.assertEqual(records[0]['executable'], '/usr/local/bin/update-geodata')
        self.assertEqual(records[0]['related'], ['geodata'])
        self.assertNotIn('hidden', str(records))
        self.assertNotIn('password', str(records))
        self.assertNotIn('do-not-save', str(records))

    def test_system_cron_removes_user_and_retains_only_executable(self):
        records = collector.cron_entries('15 3 * * * root /bin/bash /secret/script.sh token', system=True)
        self.assertEqual(records[0]['executable'], '/bin/bash')
        self.assertNotIn('secret', str(records))
        self.assertNotIn('token', str(records))

    def test_timeout_and_missing_commands_do_not_leak_errors(self):
        for exception, expected in [(FileNotFoundError('secret'), 'command_unavailable'),
                                    (subprocess.TimeoutExpired(['private'], 20), 'timeout')]:
            with patch.object(collector.subprocess, 'run', side_effect=exception):
                self.assertEqual(collector.run(['nft', '-j', 'list', 'ruleset']),
                                 {'exit_code': None, 'error': expected})

    def test_service_projection_reports_hooks_without_command_bodies(self):
        def fake(args):
            self.assertEqual(args[:3], ['systemctl', 'show', '--no-pager'])
            if '-p' in args:
                return {'exit_code': 0, 'stdout': 'ExecStartPost=xray-proxy-domain secret\nExecStopPost=xray-tproxy-routing'}
            return {'exit_code': 0, 'stdout': 'ActiveState=active\nResult=success\n'}
        with patch.object(collector, 'run', side_effect=fake):
            inventory = collector.service_inventory()
        self.assertTrue(inventory['xray.service']['legacy_domain_hook'])
        self.assertTrue(inventory['xray.service']['legacy_tproxy_hooks'])
        self.assertNotIn('secret', str(inventory))


if __name__ == '__main__':
    unittest.main()
