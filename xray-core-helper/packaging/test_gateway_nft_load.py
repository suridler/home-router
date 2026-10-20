"""Hermetic tests for the fixed boot adapter; never execute nft on this host."""
import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('gateway_loader', Path(__file__).with_name('xray-core-helper-nft-load.py'))
loader = importlib.util.module_from_spec(spec)
spec.loader.exec_module(loader)


class LoaderTest(unittest.TestCase):
    def test_only_managed_block_is_loaded(self):
        self.assertEqual(loader.block(b'flush ruleset\n# helper:begin gateway\ntable inet test {}\n# helper:end gateway\nadd table ip outside\n'), b'table inet test {}\n')

    def test_invalid_markers_and_size_are_rejected(self):
        for raw in (b'no markers', loader.BEGIN, loader.END+b'\n'+loader.BEGIN,
                    loader.BEGIN+b'\n'+loader.BEGIN+b'\n'+loader.END, b'x' * (loader.LIMIT+1)):
            with self.assertRaises(ValueError):
                loader.block(raw)

    def test_fixed_validator_precedes_apply_and_failure_stops(self):
        with tempfile.NamedTemporaryFile() as stream:
            stream.write(loader.BEGIN+b'\ntable inet test {}\n'+loader.END+b'\n'); stream.flush(); stream.seek(0)
            with patch.object(sys, 'argv', ['adapter']), patch.object(loader.os, 'geteuid', return_value=0), patch.object(loader.os, 'open', return_value=loader.os.dup(stream.fileno())), patch.object(loader.os, 'fstat') as info, patch.object(loader.subprocess, 'run') as run:
                import stat
                info.return_value.st_mode = stat.S_IFREG | 0o600; info.return_value.st_uid = 0
                self.assertEqual(loader.main(), 0)
                self.assertEqual(run.call_args_list[0].args[0], ['/usr/sbin/nft', '--check', '--file', '-'])
                self.assertEqual(run.call_args_list[1].args[0], ['/usr/sbin/nft', '--file', '-'])
            stream.seek(0)
            with patch.object(sys, 'argv', ['adapter']), patch.object(loader.os, 'geteuid', return_value=0), patch.object(loader.os, 'open', return_value=loader.os.dup(stream.fileno())), patch.object(loader.os, 'fstat') as info, patch.object(loader.subprocess, 'run', side_effect=subprocess.CalledProcessError(1, 'nft')) as run:
                info.return_value.st_mode = stat.S_IFREG | 0o600; info.return_value.st_uid = 0
                self.assertEqual(loader.main(), 1)
                self.assertEqual(run.call_count, 1)


if __name__ == '__main__':
    unittest.main()
