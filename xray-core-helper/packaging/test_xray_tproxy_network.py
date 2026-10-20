"""Hermetic tests for xray-tproxy-network.py.

The module under test is loaded directly from its hyphenated filename.  All
external commands go through a fake ``subprocess.run``; marker, lock and config
paths are redirected into a per-test temporary directory.
"""

import builtins
import contextlib
import fcntl
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
import unittest
from unittest import mock


HERE = Path(__file__).resolve().parent
MODULE_PATH = HERE / "xray-tproxy-network.py"
SPEC = importlib.util.spec_from_file_location("xray_tproxy_network_under_test", MODULE_PATH)
xray = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(xray)


class RecordedCall:
    __slots__ = ("argv", "data", "step")

    def __init__(self, argv, data, step):
        self.argv = list(argv)
        self.data = data
        self.step = step


def identify_step(argv, data):
    if argv[0] == xray.NFT:
        if argv[1:] == ["-j", "list", "tables"]:
            return "nft_list_tables"
        if "--check" in argv[1:]:
            return "nft_validate"
        if data == xray.OFF:
            return "nft_off"
        if data == xray.ON:
            return "nft_on"
        return "nft_profile_load"
    if argv[0] == xray.IP:
        rest = argv[1:]
        if rest == ["-j", "-4", "rule", "show"]:
            return "rule_list"
        if rest == ["-j", "-4", "route", "show", "table", "all"]:
            return "route_list"
        if rest[:3] == ["-4", "rule", "add"]:
            return "rule_add"
        if rest[:3] == ["-4", "rule", "del"]:
            return "rule_delete"
        if rest[:3] == ["-4", "route", "add"]:
            return "route_add"
        if rest[:3] == ["-4", "route", "del"]:
            return "route_delete"
    if argv[0] == xray.SYSTEMCTL:
        if len(argv) >= 2 and argv[1] == "show":
            return "routing_environment"
        if len(argv) >= 2 and argv[1] == "is-enabled":
            return "routing_enabled"
        if len(argv) >= 2 and argv[1] == "is-active":
            return "routing_active"
        if len(argv) >= 2 and argv[1] == "restart":
            return "routing_start"
        if len(argv) >= 2 and argv[1] == "stop":
            return "routing_stop"
        if len(argv) >= 2 and argv[1] == "start" and "xray-tproxy-sysctl.service" in argv:
            return "sysctl_apply"
    return "unexpected"


def lock_is_free(path):
    """Return whether a fresh open file description can take the lock."""
    fd = os.open(path, os.O_RDWR | os.O_CREAT, 0o600)
    try:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return False
        fcntl.flock(fd, fcntl.LOCK_UN)
        return True
    finally:
        os.close(fd)


class FakeRunner:
    """A stateful stand-in for subprocess.run.

    It understands only the command vocabulary used by the adapter under test.
    Unknown commands and commands from outside the shipped command paths fail
    the test immediately.
    """

    def __init__(self, test):
        self.test = test
        self.calls = []
        self.env = "TPROXY_MARK=100 TPROXY_TABLE=1000"
        self.enabled_rc = 1
        self.enabled_output = "disabled\n"
        self.active = False
        self.xray_table = False
        self.rules = []
        self.routes = []
        self.failures = {}
        self.probe_steps = set()
        self.probe_results = {}
        self.after_step = {}
        self.marker_exists_during_stop = None
        self.steps_before_stop = None
        self.profile_load_count = 0
        self.nft_on_applied = False
        self._next_priority = 100

    def clear_calls(self):
        self.calls = []
        self.probe_results = {}
        self.marker_exists_during_stop = None
        self.steps_before_stop = None
        self.nft_on_applied = False

    def __call__(self, argv, input=None, stdout=None, stderr=None, timeout=None):
        argv = list(argv)
        step = identify_step(argv, input)
        self.calls.append(RecordedCall(argv, input, step))
        self.test.assertNotEqual(step, "unexpected", f"unexpected command: {argv!r}")
        self.test.assertIn(argv[0], (xray.NFT, xray.IP, xray.SYSTEMCTL))

        if step in self.probe_steps:
            self.probe_results[step] = lock_is_free(xray.LOCK)
            self.test.assertTrue(
                self.probe_results[step],
                f"network lock was held while {step} was waiting",
            )

        if step == "routing_stop":
            self.marker_exists_during_stop = xray.STARTED.exists()
            self.steps_before_stop = [call.step for call in self.calls[:-1]]

        failure = self.failures.get(step)
        if failure is not None:
            if isinstance(failure, BaseException):
                raise failure
            return self._result(argv, returncode=int(failure))

        result = self._success(argv, step)
        hook = self.after_step.get(step)
        if hook is not None:
            hook(self)
        return result

    def _result(self, argv, returncode=0, stdout=b"", stderr=b""):
        return xray.subprocess.CompletedProcess(argv, returncode, stdout, stderr)

    def _success(self, argv, step):
        if step == "routing_environment":
            return self._result(argv, stdout=self.env.encode())
        if step == "routing_enabled":
            return self._result(
                argv,
                returncode=self.enabled_rc,
                stdout=self.enabled_output.encode(),
            )
        if step == "routing_active":
            return self._result(argv, returncode=0 if self.active else 3)
        if step == "nft_list_tables":
            tables = [{"table": {"family": "inet", "name": "unrelated"}}]
            if self.xray_table:
                tables.insert(0, {"table": {"family": "inet", "name": "xray"}})
            return self._result(argv, stdout=json.dumps({"nftables": tables}).encode())
        if step == "rule_list":
            return self._result(argv, stdout=json.dumps(self.rules).encode())
        if step == "route_list":
            return self._result(argv, stdout=json.dumps(self.routes).encode())
        if step == "nft_profile_load":
            self.xray_table = True
            self.profile_load_count += 1
            return self._result(argv)
        if step == "nft_on":
            self.nft_on_applied = True
            return self._result(argv)
        if step == "route_add":
            table = int(argv[argv.index("table") + 1], 0)
            self.routes.append(
                {"table": table, "type": "local", "dst": "default", "dev": "lo"}
            )
            return self._result(argv)
        if step == "rule_add":
            mark = int(argv[argv.index("fwmark") + 1], 0)
            table = int(argv[argv.index("table") + 1], 0)
            self.rules.append(
                {
                    "fwmark": mark,
                    "fwmask": "0xffffffff",
                    "table": table,
                    "priority": self._next_priority,
                }
            )
            self._next_priority += 1
            return self._result(argv)
        if step == "rule_delete":
            self._delete_rule(argv)
            return self._result(argv)
        if step == "route_delete":
            self._delete_route(argv)
            return self._result(argv)
        if step == "routing_start":
            self.active = True
            return self._result(argv)
        if step == "routing_stop":
            self.active = False
            return self._result(argv)
        # nft_validate, nft_off and sysctl_apply have no additional state here.
        return self._result(argv)

    def _delete_rule(self, argv):
        priority = argv[argv.index("priority") + 1]
        mark = int(argv[argv.index("fwmark") + 1], 0)
        table = argv[argv.index("table") + 1]
        kept = []
        removed = False
        for rule in self.rules:
            matches = (
                not removed
                and str(rule.get("priority")) == priority
                and int(str(rule.get("fwmark", 0)), 0) == mark
                and str(rule.get("table")) == table
            )
            if matches:
                removed = True
            else:
                kept.append(rule)
        self.rules = kept
        self.test.assertTrue(removed, f"rule delete did not match owned rule: {argv!r}")

    def _delete_route(self, argv):
        table = int(argv[argv.index("table") + 1], 0)
        kept = []
        removed = False
        for route in self.routes:
            matches = (
                not removed
                and str(route.get("table")) == str(table)
                and route.get("type") == "local"
                and route.get("dst") == "default"
                and route.get("dev") == "lo"
            )
            if matches:
                removed = True
            else:
                kept.append(route)
        self.routes = kept
        self.test.assertTrue(
            removed, f"route delete did not match owned route: {argv!r}"
        )

    def matching_rules(self, mark, table):
        return [
            rule
            for rule in self.rules
            if int(str(rule.get("fwmark", 0)), 0) == mark
            and int(str(rule.get("fwmask", "0xffffffff")), 0) == 0xFFFFFFFF
            and str(rule.get("table")) == str(table)
        ]

    def matching_routes(self, table):
        return [
            route
            for route in self.routes
            if str(route.get("table")) == str(table)
            and route.get("type") == "local"
            and route.get("dst") == "default"
            and route.get("dev") == "lo"
        ]


class XrayTestCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmp = Path(self._tmp.name)
        self.started = self.tmp / "xray-tproxy.started"
        self.lock = str(self.tmp / "xray-tproxy.lock")
        self.config = str(self.tmp / "nftables.conf")

        for name, value in (
            ("STARTED", self.started),
            ("LOCK", self.lock),
            ("CONFIG", self.config),
        ):
            patcher = mock.patch.object(xray, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)

        self.runner = FakeRunner(self)
        self._run_patcher = mock.patch("subprocess.run", side_effect=self.runner)
        self._run_patcher.start()
        self.addCleanup(self._run_patcher.stop)

        self.body = b"chain prerouting_proxy {\n}\nchain output_proxy {\n}\n"

    def create_marker(self):
        self.started.write_bytes(b"")

    def write_config(self, body=None):
        body = self.body if body is None else body
        self.assertTrue(body.endswith(b"\n"))
        content = (
            b"#!/usr/sbin/nft -f\n"
            + xray.BEGIN
            + b"\n"
            + body
            + xray.END
            + b"\n# trailing configuration\n"
        )
        Path(self.config).write_bytes(content)
        return body

    def steps(self):
        return [call.step for call in self.runner.calls]

    def calls_for(self, step):
        return [call for call in self.runner.calls if call.step == step]

    def count(self, step):
        return self.steps().count(step)

    def assert_ordered(self, *steps):
        positions = []
        for step in steps:
            self.assertIn(step, self.steps())
            positions.append(self.steps().index(step))
        self.assertEqual(positions, sorted(positions), f"out of order: {steps!r}")
        self.assertEqual(len(positions), len(set(positions)))

    def run_main(self, action):
        errors = io.StringIO()
        with mock.patch.object(
            sys, "argv", ["xray-tproxy-network.py", action]
        ), contextlib.redirect_stderr(errors):
            return xray.main(), errors.getvalue()

    def patch_config_open_error(self, error):
        real_open = builtins.open

        def guarded(path, *args, **kwargs):
            if os.fspath(path) == self.config:
                raise error
            return real_open(path, *args, **kwargs)

        return mock.patch("builtins.open", side_effect=guarded)


class OnActivationTests(XrayTestCase):
    MARK = 100
    TABLE = 1000

    def test_on_orders_sysctl_profile_route_rule_then_both_jumps_last(self):
        self.create_marker()
        body = self.write_config()

        xray.on(self.MARK, self.TABLE)

        self.assert_ordered(
            "nft_validate",
            "sysctl_apply",
            "nft_profile_load",
            "route_add",
            "rule_add",
            "nft_on",
        )
        self.assertEqual(self.steps()[-1], "nft_on")
        self.assertEqual(self.count("nft_on"), 1)
        self.assertEqual(self.calls_for("nft_on")[0].data, xray.ON)
        self.assertIn(b"jump prerouting_proxy", xray.ON)
        self.assertIn(b"jump output_proxy", xray.ON)
        self.assertEqual(xray.ON.count(b"jump"), 2)

        self.assertEqual(self.calls_for("nft_validate")[0].data, body)
        self.assertEqual(self.calls_for("nft_profile_load")[0].data, body)
        self.assertNotIn(b"jump", self.calls_for("nft_profile_load")[0].data)

        self.assertEqual(
            self.calls_for("sysctl_apply")[0].argv,
            [xray.SYSTEMCTL, "start", "xray-tproxy-sysctl.service"],
        )

        route_add = self.calls_for("route_add")[0].argv
        for token in ("route", "add", "local", "default", "dev", "lo"):
            self.assertIn(token, route_add)
        self.assertEqual(route_add[route_add.index("table") + 1], str(self.TABLE))

        rule_add = self.calls_for("rule_add")[0].argv
        for token in ("rule", "add", "fwmark", "table"):
            self.assertIn(token, rule_add)
        self.assertEqual(rule_add[rule_add.index("fwmark") + 1], str(self.MARK))
        self.assertEqual(rule_add[rule_add.index("table") + 1], str(self.TABLE))

        self.assertEqual(len(self.runner.matching_rules(self.MARK, self.TABLE)), 1)
        self.assertEqual(len(self.runner.matching_routes(self.TABLE)), 1)

    def test_failure_at_each_activation_stage_runs_off_cleanup(self):
        activation = (
            "sysctl_apply",
            "nft_profile_load",
            "route_add",
            "rule_add",
            "nft_on",
        )
        for stage in activation:
            with self.subTest(stage=stage):
                self.runner.clear_calls()
                self.runner.failures = {stage: 1}
                self.runner.xray_table = True
                self.runner.rules = []
                self.runner.routes = []
                self.runner.after_step = {}
                self.create_marker()
                self.write_config()

                with self.assertRaises(xray.Failure) as caught:
                    xray.on(self.MARK, self.TABLE)
                self.assertIn(stage, str(caught.exception))

                steps = self.steps()
                failure_at = steps.index(stage)
                cleanup = steps[failure_at + 1 :]
                self.assertIn("nft_list_tables", cleanup)
                self.assertIn("nft_off", cleanup)
                self.assertIn("rule_list", cleanup)
                self.assertIn("route_list", cleanup)
                for later in activation[activation.index(stage) + 1 :]:
                    self.assertNotIn(later, cleanup)
                self.assertFalse(self.runner.nft_on_applied)
                self.assertEqual(self.runner.matching_rules(self.MARK, self.TABLE), [])
                self.assertEqual(self.runner.matching_routes(self.TABLE), [])

    def test_marker_missing_refuses_on_without_commands(self):
        with self.assertRaises(xray.Failure) as caught:
            xray.on(self.MARK, self.TABLE)
        self.assertEqual(str(caught.exception), "xray_not_started")
        self.assertEqual(self.runner.calls, [])

    def test_marker_disappearing_during_activation_runs_off_cleanup(self):
        self.create_marker()
        self.write_config()
        self.runner.xray_table = True

        def remove_marker(_runner):
            xray.STARTED.unlink(missing_ok=True)

        self.runner.after_step = {"rule_add": remove_marker}

        with self.assertRaises(xray.Failure) as caught:
            xray.on(self.MARK, self.TABLE)
        self.assertIn("xray_stopped_during_activation", str(caught.exception))

        steps = self.steps()
        self.assertNotIn("nft_on", steps)
        self.assertIn("nft_off", steps)
        self.assertEqual(self.runner.matching_rules(self.MARK, self.TABLE), [])
        self.assertEqual(self.runner.matching_routes(self.TABLE), [])


class OffTests(XrayTestCase):
    MARK = 100
    TABLE = 1000

    def test_off_absent_objects_succeeds_without_mutation(self):
        self.runner.xray_table = False

        xray.off(self.MARK, self.TABLE)

        steps = self.steps()
        self.assertIn("nft_list_tables", steps)
        self.assertIn("rule_list", steps)
        self.assertIn("route_list", steps)
        self.assertNotIn("nft_off", steps)
        self.assertNotIn("rule_delete", steps)
        self.assertNotIn("route_delete", steps)

    def test_off_removes_only_owned_rule_and_route(self):
        self.runner.xray_table = True
        self.runner.rules = [
            {
                "fwmark": 100,
                "fwmask": "0xffffffff",
                "table": 1000,
                "priority": 100,
            },
            {
                "fwmark": "0x64",
                "fwmask": "0xffffffff",
                "table": "1000",
                "priority": 101,
            },
            {
                "fwmark": 101,
                "fwmask": "0xffffffff",
                "table": 1000,
                "priority": 102,
            },
            {
                "fwmark": 100,
                "fwmask": "0xffffff00",
                "table": 1000,
                "priority": 103,
            },
            {
                "fwmark": 100,
                "fwmask": "0xffffffff",
                "table": 2000,
                "priority": 104,
            },
        ]
        self.runner.routes = [
            {"table": 1000, "type": "local", "dst": "default", "dev": "lo"},
            {"table": 2000, "type": "local", "dst": "default", "dev": "lo"},
            {"table": 1000, "type": "unicast", "dst": "default", "dev": "lo"},
            {"table": 1000, "type": "local", "dst": "192.0.2.0/24", "dev": "lo"},
            {"table": 1000, "type": "local", "dst": "default", "dev": "eth0"},
        ]

        xray.off(self.MARK, self.TABLE)

        self.assertEqual(self.count("nft_off"), 1)
        deleted_priorities = {
            call.argv[call.argv.index("priority") + 1]
            for call in self.calls_for("rule_delete")
        }
        self.assertEqual(deleted_priorities, {"100", "101"})
        for call in self.calls_for("rule_delete"):
            argv = call.argv
            self.assertEqual(argv[:4], [xray.IP, "-4", "rule", "del"])
            self.assertEqual(argv[argv.index("fwmark") + 1], str(self.MARK))
            self.assertEqual(argv[argv.index("table") + 1], str(self.TABLE))

        self.assertEqual(self.count("route_delete"), 1)
        self.assertEqual(
            self.calls_for("route_delete")[0].argv,
            [
                xray.IP,
                "-4",
                "route",
                "del",
                "local",
                "default",
                "dev",
                "lo",
                "table",
                str(self.TABLE),
            ],
        )

        self.assertEqual(
            {rule["priority"] for rule in self.runner.rules}, {102, 103, 104}
        )
        self.assertEqual(
            [
                (route["table"], route["type"], route["dst"], route["dev"])
                for route in self.runner.routes
            ],
            [
                (2000, "local", "default", "lo"),
                (1000, "unicast", "default", "lo"),
                (1000, "local", "192.0.2.0/24", "lo"),
                (1000, "local", "default", "eth0"),
            ],
        )

    def test_off_nft_failure_still_cleans_rule_and_route_and_raises(self):
        self.runner.xray_table = True
        self.runner.rules = [
            {
                "fwmark": self.MARK,
                "fwmask": "0xffffffff",
                "table": self.TABLE,
                "priority": 100,
            }
        ]
        self.runner.routes = [
            {"table": self.TABLE, "type": "local", "dst": "default", "dev": "lo"}
        ]
        self.runner.failures = {"nft_off": 1}

        with self.assertRaises(xray.Failure) as caught:
            xray.off(self.MARK, self.TABLE)
        self.assertIn("nft_off", str(caught.exception))
        self.assertIn("exit 1", str(caught.exception))

        steps = self.steps()
        self.assert_ordered("nft_off", "rule_list", "rule_delete", "route_list", "route_delete")
        self.assertGreater(steps.index("rule_delete"), steps.index("nft_off"))
        self.assertGreater(steps.index("route_delete"), steps.index("nft_off"))
        self.assertEqual(self.runner.matching_rules(self.MARK, self.TABLE), [])
        self.assertEqual(self.runner.matching_routes(self.TABLE), [])

    def test_off_repeated_is_idempotent(self):
        self.runner.xray_table = True
        self.runner.rules = [
            {
                "fwmark": self.MARK,
                "fwmask": "0xffffffff",
                "table": self.TABLE,
                "priority": 100,
            }
        ]
        self.runner.routes = [
            {"table": self.TABLE, "type": "local", "dst": "default", "dev": "lo"}
        ]

        xray.off(self.MARK, self.TABLE)
        xray.off(self.MARK, self.TABLE)

        self.assertEqual(self.count("nft_off"), 2)
        self.assertEqual(self.count("rule_delete"), 1)
        self.assertEqual(self.count("route_delete"), 1)
        self.assertEqual(self.runner.matching_rules(self.MARK, self.TABLE), [])
        self.assertEqual(self.runner.matching_routes(self.TABLE), [])


class ParametersTests(XrayTestCase):
    def test_parameters_uses_effective_unit_environment(self):
        cases = [
            ("TPROXY_MARK=100 TPROXY_TABLE=1000", (100, 1000)),
            ("TPROXY_MARK=0x64 TPROXY_TABLE=0x3e8", (100, 1000)),
            ('UNRELATED="a b" TPROXY_MARK="0x1" TPROXY_TABLE="1"', (1, 1)),
        ]
        for env, expected in cases:
            with self.subTest(env=env):
                self.runner.clear_calls()
                self.runner.env = env
                self.assertEqual(xray.parameters(), expected)
                self.assertEqual(self.steps(), ["routing_environment"])
                self.assertEqual(
                    self.runner.calls[0].argv,
                    [
                        xray.SYSTEMCTL,
                        "show",
                        xray.UNIT,
                        "--property=Environment",
                        "--value",
                    ],
                )

    def test_parameters_accepts_valid_numeric_boundaries(self):
        cases = [
            ("TPROXY_MARK=1 TPROXY_TABLE=1", (1, 1)),
            ("TPROXY_MARK=0xffffffff TPROXY_TABLE=252", (0xFFFFFFFF, 252)),
            (
                "TPROXY_MARK=4294967295 TPROXY_TABLE=4294967295",
                (0xFFFFFFFF, 0xFFFFFFFF),
            ),
        ]
        for env, expected in cases:
            with self.subTest(env=env):
                self.runner.env = env
                self.assertEqual(xray.parameters(), expected)

    def test_parameters_rejects_missing_and_non_numeric_values(self):
        invalid = [
            "TPROXY_TABLE=1000",
            "TPROXY_MARK=100",
            "TPROXY_MARK=abc TPROXY_TABLE=1000",
            "TPROXY_MARK=100 TPROXY_TABLE=1.5",
            "TPROXY_MARK= TPROXY_TABLE=1000",
            "TPROXY_MARK=100 TPROXY_TABLE=0x",
            "TPROXY_MARK=0 TPROXY_TABLE=1000",
            "TPROXY_MARK=100 TPROXY_TABLE=0",
            "TPROXY_MARK=-1 TPROXY_TABLE=1000",
            "TPROXY_MARK=100 TPROXY_TABLE=-1",
            "TPROXY_MARK=0x100000000 TPROXY_TABLE=1000",
            "TPROXY_MARK=100 TPROXY_TABLE=0x100000000",
        ]
        for env in invalid:
            with self.subTest(env=env):
                self.runner.env = env
                with self.assertRaises(xray.Failure) as caught:
                    xray.parameters()
                self.assertEqual(str(caught.exception), "routing_parameters")

    def test_parameters_rejects_default_and_reserved_tables(self):
        for table in (253, 254, 255, "0xfd", "0xfe", "0xff"):
            with self.subTest(table=table):
                self.runner.env = f"TPROXY_MARK=100 TPROXY_TABLE={table}"
                with self.assertRaises(xray.Failure) as caught:
                    xray.parameters()
                self.assertEqual(str(caught.exception), "routing_parameters")

    def test_parameters_reports_environment_command_failure(self):
        self.runner.failures = {"routing_environment": 1}
        with self.assertRaises(xray.Failure) as caught:
            xray.parameters()
        self.assertIn("routing_environment", str(caught.exception))

    def test_action_on_invalid_table_does_not_touch_network(self):
        self.create_marker()
        for table in (253, 254, 255):
            with self.subTest(table=table):
                self.runner.clear_calls()
                self.runner.env = f"TPROXY_MARK=100 TPROXY_TABLE={table}"
                with self.assertRaises(xray.Failure):
                    xray.action("on")
                self.assertIn("routing_environment", self.steps())
                self.assertFalse(
                    any(
                        call.argv[0] in (xray.NFT, xray.IP)
                        for call in self.runner.calls
                    )
                )


class ConfigTests(XrayTestCase):
    def test_read_block_extracts_only_marked_section(self):
        body = b"# inside\nchain prerouting_proxy {\n}\nchain output_proxy {\n}\n"
        self.write_config(body)
        self.assertEqual(xray.read_block(), body)

    def test_block_rejects_invalid_markers(self):
        raw_cases = [
            b"no markers at all\n",
            xray.BEGIN + b"\n" + xray.BEGIN + b"\n" + xray.END + b"\n",
            xray.BEGIN + b"\n" + xray.END + b"\n" + xray.END + b"\n",
            xray.END + b"\n" + xray.BEGIN + b"\n",
            xray.BEGIN + b"\nwithout end\n",
        ]
        for raw in raw_cases:
            with self.subTest(raw=raw):
                with self.assertRaises(xray.Failure) as caught:
                    xray.block(raw)
                self.assertEqual(str(caught.exception), "nft_block_markers")

    def test_read_block_access_error(self):
        with self.patch_config_open_error(
            PermissionError(13, "permission denied", self.config)
        ):
            with self.assertRaises(PermissionError):
                xray.read_block()
        self.assertEqual(self.runner.calls, [])

    def test_read_block_rejects_non_regular_file(self):
        self.write_config()
        non_regular = os.stat_result(
            (stat.S_IFIFO | 0o600, 0, 0, 0, 0, 0, 0, 0, 0, 0)
        )
        with mock.patch.object(xray.os, "fstat", return_value=non_regular):
            with self.assertRaises(xray.Failure) as caught:
                xray.read_block()
        self.assertEqual(str(caught.exception), "nft_config_type")
        self.assertEqual(self.runner.calls, [])

    def test_main_reports_config_access_error_without_network(self):
        self.create_marker()
        with self.patch_config_open_error(
            PermissionError(13, "permission denied", self.config)
        ):
            returncode, errors = self.run_main("on")
        self.assertEqual(returncode, 1)
        self.assertIn("xray_tproxy_on_failed", errors)
        self.assertEqual(self.steps(), ["routing_environment"])
        self.assertFalse(
            any(call.argv[0] in (xray.NFT, xray.IP) for call in self.runner.calls)
        )

    def test_marker_creation_error_returns_failure_without_commands(self):
        missing_parent = self.tmp / "absent" / "xray-tproxy.started"
        with mock.patch.object(xray, "STARTED", missing_parent):
            returncode, errors = self.run_main("xray-start")
        self.assertEqual(returncode, 1)
        self.assertIn("xray_tproxy_xray-start_failed", errors)
        self.assertFalse(missing_parent.exists())
        self.assertEqual(self.runner.calls, [])


class LifecycleTests(XrayTestCase):
    def test_adopt_checks_xray_under_lock_and_does_not_touch_network(self):
        self.runner.active = True
        def probe(_):
            self.assertFalse(lock_is_free(self.lock))
        self.runner.after_step["routing_active"] = probe
        xray.action("adopt")
        self.assertTrue(self.started.exists())
        self.assertEqual(self.steps(), ["routing_active"])
        self.assertEqual(self.runner.calls[0].argv[-1], "xray.service")

    def test_adopt_inactive_xray_cannot_leave_marker(self):
        self.runner.active = False
        with self.assertRaises(xray.Failure):
            xray.action("adopt")
        self.assertFalse(self.started.exists())
        self.assertEqual(self.steps(), ["routing_active"])

    def test_resume_loaded_candidate_never_reads_durable_profile(self):
        self.create_marker()
        self.runner.active = True
        with mock.patch.object(xray, "read_block", side_effect=AssertionError("old profile read")):
            xray.action("resume")
        self.assertEqual(self.steps(), ["routing_environment", "routing_active", "nft_on"])
        self.assertEqual(self.runner.calls[-1].data, xray.ON)

    def test_resume_cannot_activate_when_marker_or_service_absent(self):
        for marker, active in [(False, True), (True, False)]:
            with self.subTest(marker=marker, active=active):
                self.started.unlink(missing_ok=True)
                if marker:
                    self.create_marker()
                self.runner.active = active
                self.runner.clear_calls()
                xray.action("resume")
                self.assertNotIn("nft_on", self.steps())

    def test_xray_start_disabled_creates_marker_without_activation(self):
        self.runner.enabled_rc = 1
        self.runner.enabled_output = "disabled\n"

        xray.action("xray-start")

        self.assertTrue(self.started.exists())
        self.assertIn("routing_enabled", self.steps())
        self.assertNotIn("routing_start", self.steps())
        self.assertFalse(
            any(call.argv[0] in (xray.NFT, xray.IP) for call in self.runner.calls)
        )

    def test_xray_start_enabled_restarts_even_when_service_is_active(self):
        self.runner.enabled_rc = 0
        self.runner.enabled_output = "enabled\n"
        self.runner.active = True

        xray.action("xray-start")

        self.assertTrue(self.started.exists())
        self.assertEqual(self.count("routing_start"), 1)
        self.assertEqual(
            self.calls_for("routing_start")[0].argv,
            [xray.SYSTEMCTL, "restart", xray.UNIT],
        )
        self.assertFalse(
            any(call.argv[0] in (xray.NFT, xray.IP) for call in self.runner.calls)
        )

    def test_xray_start_restart_failure_cleans_marker_and_network(self):
        self.runner.enabled_rc = 0
        self.runner.enabled_output = "enabled\n"
        self.runner.active = True
        self.runner.failures = {"routing_start": 1}
        self.runner.xray_table = True
        self.runner.rules = [
            {
                "fwmark": 100,
                "fwmask": "0xffffffff",
                "table": 1000,
                "priority": 100,
            }
        ]
        self.runner.routes = [
            {"table": 1000, "type": "local", "dst": "default", "dev": "lo"}
        ]

        with self.assertRaises(xray.Failure) as caught:
            xray.action("xray-start")
        self.assertIn("routing_start", str(caught.exception))

        self.assertFalse(self.started.exists())
        self.assertEqual(self.count("routing_stop"), 1)
        self.assert_ordered(
            "routing_start",
            "routing_stop",
            "routing_environment",
            "nft_off",
            "rule_delete",
            "route_delete",
        )
        self.assertEqual(self.runner.matching_rules(100, 1000), [])
        self.assertEqual(self.runner.matching_routes(1000), [])

    def test_xray_stop_removes_marker_cleans_then_stops_even_inactive(self):
        self.create_marker()
        self.runner.active = False
        self.runner.xray_table = True
        self.runner.rules = [
            {
                "fwmark": 100,
                "fwmask": "0xffffffff",
                "table": 1000,
                "priority": 100,
            }
        ]
        self.runner.routes = [
            {"table": 1000, "type": "local", "dst": "default", "dev": "lo"}
        ]

        xray.action("xray-stop")

        self.assertFalse(self.started.exists())
        self.assertIs(self.runner.marker_exists_during_stop, False)
        self.assertIn("nft_off", self.runner.steps_before_stop)
        self.assertIn("rule_delete", self.runner.steps_before_stop)
        self.assertIn("route_delete", self.runner.steps_before_stop)
        self.assertEqual(self.count("routing_stop"), 1)
        self.assertEqual(self.runner.matching_rules(100, 1000), [])
        self.assertEqual(self.runner.matching_routes(1000), [])

    def test_xray_stop_cleanup_failure_still_stops_and_raises(self):
        self.create_marker()
        self.runner.active = False
        self.runner.xray_table = True
        self.runner.failures = {"nft_off": 1}

        with self.assertRaises(xray.Failure) as caught:
            xray.action("xray-stop")
        self.assertIn("nft_off", str(caught.exception))

        self.assertFalse(self.started.exists())
        self.assertEqual(self.count("routing_stop"), 1)
        self.assertIn("rule_list", self.steps())
        self.assertIn("route_list", self.steps())


class LockTests(XrayTestCase):
    def test_lock_probe_detects_a_held_lock(self):
        with xray.locked():
            self.assertFalse(lock_is_free(xray.LOCK))
        self.assertTrue(lock_is_free(xray.LOCK))

    def test_no_lock_held_while_restart_waits(self):
        self.runner.enabled_rc = 0
        self.runner.enabled_output = "enabled\n"
        self.runner.active = True
        self.runner.probe_steps = {"routing_start"}

        xray.action("xray-start")

        self.assertEqual(self.runner.probe_results, {"routing_start": True})

    def test_no_lock_held_while_stop_waits(self):
        self.create_marker()
        self.runner.xray_table = True
        self.runner.probe_steps = {"routing_stop"}

        xray.action("xray-stop")

        self.assertEqual(self.runner.probe_results, {"routing_stop": True})

    def test_no_lock_held_while_cancel_stop_waits(self):
        self.runner.enabled_rc = 0
        self.runner.enabled_output = "enabled\n"
        self.runner.failures = {"routing_start": 1}
        self.runner.xray_table = True
        self.runner.probe_steps = {"routing_stop"}

        with self.assertRaises(xray.Failure):
            xray.action("xray-start")

        self.assertEqual(self.runner.probe_results, {"routing_stop": True})


class HermeticTests(XrayTestCase):
    def test_state_paths_are_temporary_and_commands_are_mocked(self):
        for path in (xray.STARTED, Path(xray.LOCK), Path(xray.CONFIG)):
            self.assertEqual(path.parent, self.tmp)
        self.assertNotEqual(Path(xray.CONFIG), Path("/etc/nftables.conf"))
        self.assertIsInstance(xray.subprocess.run, mock.Mock)

        self.create_marker()
        self.write_config()
        xray.action("on")

        self.assertTrue(self.runner.calls)
        for call in self.runner.calls:
            self.assertIn(call.argv[0], (xray.NFT, xray.IP, xray.SYSTEMCTL))
            for argument in call.argv:
                self.assertFalse(
                    argument.startswith("/etc/") or argument.startswith("/run/"),
                    f"system path used in command: {call.argv!r}",
                )


if __name__ == "__main__":
    unittest.main()
