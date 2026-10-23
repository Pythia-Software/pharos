"""Release signing window tests; never access the real Keychain or secrets."""
import importlib.util
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import unittest
from unittest.mock import MagicMock, patch

SOURCE = Path(__file__).resolve().parents[1] / 'macos/release_keychain.py'
spec = importlib.util.spec_from_file_location('release_keychain_test', SOURCE)
keychain = importlib.util.module_from_spec(spec)
spec.loader.exec_module(keychain)


class ReleaseKeychainTests(unittest.TestCase):
    def session(self, exit_code=0, wait_error=None, unlock_error=None):
        events = []
        child = MagicMock(pid=987654321)
        child.wait.side_effect = [wait_error, 0] if wait_error else None
        child.wait.return_value = exit_code

        def security(*args, **kwargs):
            events.append(args)
            if args[0] == 'unlock-keychain' and unlock_error:
                raise unlock_error
            if args[0] == 'list-keychains' and '-s' not in args:
                if any(event[0] == 'unlock-keychain' for event in events):
                    return '"/test/release.keychain-db"\n"/test/login.keychain-db"\n"/test/added-by-another-app.keychain-db"\n'
                return '"/test/login.keychain-db"\n'

        with patch.object(keychain, 'security', side_effect=security), \
             patch.object(keychain.subprocess, 'Popen', return_value=child) as launch, \
             patch.object(keychain.os, 'killpg') as kill:
            try:
                result = keychain.run_session(Path('/test/release.keychain-db'), ['release', '1.2.3'])
            except subprocess.CalledProcessError:
                result = 'unlock failed'
        return result, events, launch, kill

    def test_success_and_failure_both_close_the_signing_window(self):
        for exit_code in (0, 42):
            with self.subTest(exit_code=exit_code):
                result, events, launch, kill = self.session(exit_code)
                self.assertEqual(result, exit_code)
                self.assertEqual(sum(event[0] == 'lock-keychain' for event in events), 2)
                last_lock = max(i for i, event in enumerate(events) if event[0] == 'lock-keychain')
                self.assertLess(last_lock, len(events) - 1)
                restored = events[-1]
                self.assertNotIn('/test/release.keychain-db', restored)
                self.assertIn('/test/added-by-another-app.keychain-db', restored)
                env = launch.call_args.kwargs['env']
                self.assertEqual(env['PHAROS_CODESIGN_KEYCHAIN'], '/test/release.keychain-db')
                self.assertEqual(env[keychain.SESSION], '/test/release.keychain-db')
                self.assertTrue(launch.call_args.kwargs['start_new_session'])
                self.assertIn(('unlock-keychain', '-u', Path('/test/release.keychain-db')), events)

    def test_cancelled_unlock_does_not_start_release_and_still_locks(self):
        result, events, launch, kill = self.session(unlock_error=subprocess.CalledProcessError(1, 'security'))
        self.assertEqual(result, 'unlock failed')
        launch.assert_not_called()
        self.assertIn(('lock-keychain', Path('/test/release.keychain-db')), events)

    def test_termination_stops_children_and_locks(self):
        result, events, launch, kill = self.session(wait_error=InterruptedError(signal.SIGTERM))
        self.assertEqual(result, 128 + signal.SIGTERM)
        kill.assert_called_with(987654321, signal.SIGTERM)
        self.assertEqual(sum(event[0] == 'lock-keychain' for event in events), 2)

    def test_lock_failure_is_reported_even_after_success(self):
        calls = 0
        def security(*args, **kwargs):
            nonlocal calls
            if args[0] == 'list-keychains' and '-s' not in args:
                return '"/test/release.keychain-db"'
            if args[0] == 'lock-keychain':
                calls += 1
                if calls == 2:
                    raise subprocess.CalledProcessError(1, 'security')
        with patch.object(keychain, 'security', side_effect=security), \
             patch.object(keychain.subprocess, 'Popen') as launch, \
             patch.object(keychain.os, 'killpg'):
            launch.return_value.wait.return_value = 0
            with self.assertRaisesRegex(ValueError, 'cleanup failed'):
                keychain.run_session(Path('/test/release.keychain-db'), ['release'])

    def test_missing_keychain_never_prompts_or_creates_keys(self):
        with tempfile.TemporaryDirectory() as temp, \
             patch.dict(os.environ, {'PHAROS_RELEASE_KEYCHAIN': str(Path(temp) / 'absent')}, clear=True), \
             patch.object(keychain, 'security') as security:
            with self.assertRaisesRegex(ValueError, 'missing'):
                keychain.with_keychain(['release'])
            security.assert_not_called()

    def test_concurrent_deployment_cannot_relock_active_keychain(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'release.keychain-db'
            path.touch()
            with patch.dict(os.environ, {'PHAROS_RELEASE_KEYCHAIN': str(path)}, clear=True), \
                 path.with_suffix(path.suffix + '.release-lock').open('a') as lock, \
                 patch.object(keychain, 'run_session') as run:
                keychain.fcntl.flock(lock, keychain.fcntl.LOCK_EX | keychain.fcntl.LOCK_NB)
                with self.assertRaisesRegex(ValueError, 'Another deployment'):
                    keychain.with_keychain(['release'])
                run.assert_not_called()

    def test_nested_command_reuses_unlock(self):
        with patch.dict(os.environ, {'PHAROS_RELEASE_KEYCHAIN': '/test/release.keychain-db',
                                     keychain.SESSION: '/test/release.keychain-db'}, clear=True), \
             patch.object(keychain.subprocess, 'call', return_value=7), \
             patch.object(keychain, 'security') as security:
            self.assertEqual(keychain.with_keychain(['feed']), 7)
            security.assert_not_called()

    def test_existing_release_keychain_takes_precedence_over_login_copy(self):
        original = ['/test/login.keychain-db', '/test/release.keychain-db']
        current = list(original)
        def security(*args, **kwargs):
            if args[:3] == ('list-keychains', '-d', 'user'):
                if '-s' in args:
                    current[:] = args[4:]
                else:
                    return '\n'.join('"' + path + '"' for path in current)
        def launch(*args, **kwargs):
            self.assertEqual(current[0], '/test/release.keychain-db')
            child = MagicMock(pid=987654321)
            child.wait.return_value = 0
            return child
        with patch.object(keychain, 'security', side_effect=security), \
             patch.object(keychain.subprocess, 'Popen', side_effect=launch), \
             patch.object(keychain.os, 'killpg'):
            self.assertEqual(keychain.run_session(Path('/test/release.keychain-db'), ['release']), 0)
        self.assertEqual(current, original)


if __name__ == '__main__':
    unittest.main()
