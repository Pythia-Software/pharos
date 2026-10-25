"""Release signing window tests; never access the real Keychain or secrets."""
import contextlib
import importlib.util
import io
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import MagicMock, patch

SOURCE = Path(__file__).resolve().parents[1] / 'macos/release_keychain.py'
spec = importlib.util.spec_from_file_location('release_keychain_test', SOURCE)
keychain = importlib.util.module_from_spec(spec)
spec.loader.exec_module(keychain)


class ReleaseKeychainTests(unittest.TestCase):
    @unittest.skipUnless(sys.platform == 'darwin' and os.environ.get('PHAROS_KEYCHAIN_TESTS') == '1',
                         'explicit macOS disposable-keychain integration test')
    def test_native_sparkle_access_checks_existing_item_without_dialogs(self):
        # Read only access-control metadata on disposable public dummy items.
        original = keychain.search_list()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'test.keychain-db'
            command = keychain.sparkle_access_command(directory, 'check', path,
                                                       Path('/unused'), 'test-pharos')
            command[-2:] = ['/usr/bin/true', '/usr/bin/false']
            source = SOURCE.with_name('sparkle-keychain-access.swift').read_text()
            fixture = """
if CommandLine.arguments.count == 4 && CommandLine.arguments[1] == "fixture" {
    var keychain: SecKeychain?
    try check(SecKeychainOpen(CommandLine.arguments[2], &keychain))
    var security: SecTrustedApplication?
    try check(SecTrustedApplicationCreateFromPath("/usr/bin/security", &security))
    var trusted = [security!]
    if CommandLine.arguments[3] != "test-pharos" {
        for path in ["/usr/bin/true", "/usr/bin/false"] {
            var app: SecTrustedApplication?
            try check(SecTrustedApplicationCreateFromPath(path, &app))
            trusted.append(app!)
        }
    }
    var access: SecAccess?
    try check(SecAccessCreate("public test fixture" as CFString, trusted as CFArray, &access))
    var partitions = ["apple:"]
    if CommandLine.arguments[3] == "test-authorized" {
        partitions += try ["/usr/bin/true", "/usr/bin/false"].map { try codePartition($0) }
    }
    let properties: [String: Any] = ["Partitions": partitions]
    let description = try partitionDescription(properties)
    guard try partitionProperties(description)["Partitions"] as? [String] == partitions else { exit(2) }
    var partitionACL: SecACL?
    try check(SecACLCreateWithSimpleContents(access!, nil, description, SecKeychainPromptSelector(), &partitionACL))
    try check(SecACLUpdateAuthorizations(partitionACL!, [kSecACLAuthorizationPartitionID] as CFArray))
    let query: [String: Any] = [
        kSecClass as String: kSecClassGenericPassword,
        kSecAttrService as String: "https://sparkle-project.org",
        kSecAttrAccount as String: CommandLine.arguments[3],
        kSecUseKeychain as String: keychain!,
        kSecAttrAccess as String: access!,
        kSecValueData as String: Data("public-test-value".utf8),
    ]
    try check(SecItemAdd(query as CFDictionary, nil))
    exit(0)
}
"""
            fixture_source = Path(directory) / 'fixture.swift'
            fixture_source.write_text(source.replace('do { try main() }', fixture + '\ndo { try main() }'))
            sdk = subprocess.check_output(['xcrun', '--sdk', 'macosx26.5', '--show-sdk-path'], text=True).strip()
            subprocess.run(['swiftc', '-sdk', sdk, '-suppress-warnings', str(fixture_source),
                            '-o', command[0]], check=True)
            try:
                keychain.security('create-keychain', '-p', 'disposable-test-password', path)
                keychain.security('list-keychains', '-d', 'user', '-s', *original)
                for account in ('test-pharos', 'test-paths-only', 'test-authorized'):
                    subprocess.run([command[0], 'fixture', str(path), account], check=True, timeout=10)
                missing = subprocess.run(command, capture_output=True, text=True, timeout=10)
                self.assertNotEqual(missing.returncode, 0)
                self.assertIn('--authorize-sparkle', missing.stderr)
                # This is the actual review finding: matching paths passed the old
                # check while Sparkle's code hashes were missing from partitions.
                command[3] = 'test-paths-only'
                missing_hashes = subprocess.run(command, capture_output=True, text=True, timeout=10)
                self.assertNotEqual(missing_hashes.returncode, 0)
                self.assertIn('partition authorization', missing_hashes.stderr)
                command[1] = 'partitions'
                planned = subprocess.check_output(command, text=True, timeout=10).strip().split(',')
                self.assertIn('apple:', planned)  # Preserve unrelated existing permissions.
                self.assertEqual(sum(value.startswith('cdhash:') for value in planned), 2)
                command[1] = 'check'
                self.assertNotEqual(subprocess.run(command, capture_output=True, timeout=10).returncode, 0)
                command[3] = 'test-authorized'
                subprocess.run(command, check=True, timeout=10)
                value = keychain.security('find-generic-password', '-a', 'test-pharos',
                                          '-s', 'https://sparkle-project.org', '-w', path, capture=True)
                self.assertEqual(value.strip(), 'public-test-value')
            finally:
                if path.exists():
                    keychain.security('delete-keychain', path)
                keychain.security('list-keychains', '-d', 'user', '-s', *original)

    def test_sparkle_access_check_uses_exact_tools_and_dedicated_keychain(self):
        with patch.dict(os.environ, {'PHAROS_RELEASE_KEYCHAIN': '/test/release.keychain-db', 'SDKROOT': ''}), \
             patch.object(keychain.subprocess, 'check_output', return_value='/test/sdk\n') as discover, \
             patch.object(keychain.subprocess, 'run') as run:
            keychain.check_sparkle_access(Path('/test/Sparkle/bin'), 'pharos')
        discover.assert_called_once_with(['xcrun', '--sdk', 'macosx26.5', '--show-sdk-path'], text=True)
        compile_command = run.call_args_list[0].args[0]
        self.assertIn('/test/sdk', compile_command)
        command = run.call_args_list[1].args[0]
        self.assertEqual(command[1:], ['check', '/test/release.keychain-db', 'pharos',
                                     '/test/Sparkle/bin/generate_keys', '/test/Sparkle/bin/sign_update'])

    def test_sparkle_helper_honors_sdkroot_without_discovering_default_sdk(self):
        sdk = '/custom SDK/MacOSX.sdk'
        with patch.dict(os.environ, {'SDKROOT': sdk}), \
             patch.object(keychain.subprocess, 'check_output', side_effect=FileNotFoundError('26.5 SDK unavailable')) as discover, \
             patch.object(keychain.subprocess, 'run') as compile:
            keychain.sparkle_access_command('/test/build', 'check', Path('/test/keychain'),
                                            Path('/test/tools'), 'pharos')
        discover.assert_not_called()
        command = compile.call_args.args[0]
        self.assertEqual(command[command.index('-sdk') + 1], sdk)

    def test_sparkle_access_failure_blocks_the_caller(self):
        with patch.object(keychain, 'sparkle_access_command', return_value=['check-access']), \
             patch.object(keychain.subprocess, 'run', side_effect=subprocess.CalledProcessError(1, 'check-access')):
            with self.assertRaises(subprocess.CalledProcessError):
                keychain.check_sparkle_access(Path('/test/tools'), 'pharos')

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
                self.assertEqual(env['PHAROS_NOTARY_KEYCHAIN'], '/test/release.keychain-db')
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


class CredentialRehearsalTests(unittest.TestCase):
    def load(self, name):
        source = SOURCE.with_name(name)
        spec = importlib.util.spec_from_file_location('credential_rehearsal_test', source)
        module = importlib.util.module_from_spec(spec)
        sys.path.insert(0, str(source.parent))
        try:
            spec.loader.exec_module(module)
        finally:
            sys.path.pop(0)
        return module

    def test_rehearsal_uses_dedicated_apple_credentials_and_never_prints_gh_token(self):
        probe = self.load('check-release-credentials.py')
        feed = MagicMock()
        feed.config_and_tools.return_value = ({'keychain_account': 'pharos'}, Path('/test/tools'))
        commands = []
        def run(*args):
            commands.append(args)
            if args[:3] == ('xcrun', 'notarytool', 'history'):
                return '{"message":"Successfully received submission history."}'
            if args[:2] == ('gh', 'api'):
                return '{"push":true}'
            if args[:3] == ('gh', 'auth', 'status'):
                self.assertIn('--jq', args)
                self.assertEqual(args[-1], '[.hosts["github.com"][] | select(.active) | .tokenSource]')
                return '["keyring"]'
            return 'public-signature'
        output = io.StringIO()
        with patch.object(probe, 'load_feed', return_value=feed), \
             patch.object(probe, 'run', side_effect=run), \
             patch.object(probe.subprocess, 'run', return_value=SimpleNamespace(stderr='Authority=Developer ID Application: Test\nTimestamp=today')) as codesign, \
             patch.dict(os.environ, {'PHAROS_RELEASE_KEYCHAIN': '/test/release.keychain-db'}), \
             contextlib.redirect_stdout(output):
            probe.exercise()
        self.assertEqual(feed.verify_signature.call_count, 2)
        self.assertEqual(sum(call.args[0][1] == '--force' for call in codesign.call_args_list), 2)
        notary = next(command for command in commands if command[:3] == ('xcrun', 'notarytool', 'history'))
        self.assertEqual(notary[notary.index('--keychain') + 1], '/test/release.keychain-db')
        self.assertIn('Credential operations passed', output.getvalue())
        self.assertIn('does not count macOS dialogs', output.getvalue())

    def test_notary_setup_leaves_password_input_to_apple_and_reuses_unlock(self):
        setup = self.load('setup-release-keychain.py')
        path = str(Path('/test/release.keychain-db').resolve())
        with patch.object(setup, 'require_release_machine'), \
             patch.object(sys, 'argv', ['setup', '--configure-notary']), \
             patch.dict(os.environ, {'PHAROS_RELEASE_KEYCHAIN': path, setup.SESSION: path}), \
             patch.object(setup, 'with_keychain') as unlock, \
             patch.object(setup.subprocess, 'run') as store, \
             patch.object(setup.subprocess, 'call', return_value=0) as rehearse:
            setup.main()
        unlock.assert_not_called()
        store.assert_called_once_with(['xcrun', 'notarytool', 'store-credentials', 'pharos',
                                       '--keychain', path], check=True)
        self.assertEqual(Path(rehearse.call_args.args[0][1]).name, 'check-release-credentials.py')

    def test_sparkle_partition_setup_leaves_password_input_to_security(self):
        setup = self.load('setup-release-keychain.py')
        path = str(Path('/test/release.keychain-db').resolve())
        with patch.object(setup, 'require_release_machine'), \
             patch.object(sys, 'argv', ['setup', '--authorize-sparkle']), \
             patch.dict(os.environ, {'PHAROS_RELEASE_KEYCHAIN': path, setup.SESSION: path}), \
             patch.object(setup.sys.stdin, 'isatty', return_value=True), \
             patch.object(setup, 'sparkle_access_command', return_value=['acl-helper', 'authorize']), \
             patch.object(setup.subprocess, 'check_output', side_effect=['/test/Sparkle', 'apple:,cdhash:a,cdhash:b']), \
             patch.object(setup.subprocess, 'run') as run:
            setup.main()
        security = next(call for call in run.call_args_list if call.args[0][0] == '/usr/bin/security')
        self.assertEqual(security.args[0], ['/usr/bin/security', 'set-generic-password-partition-list',
                                          '-a', 'pharos', '-s', 'https://sparkle-project.org',
                                          '-S', 'apple:,cdhash:a,cdhash:b', path])
        self.assertNotIn('input', security.kwargs)
        self.assertNotIn('env', security.kwargs)

    def test_sparkle_partition_setup_requires_operator_terminal_before_unlock(self):
        setup = self.load('setup-release-keychain.py')
        with patch.object(setup, 'require_release_machine'), \
             patch.object(sys, 'argv', ['setup', '--authorize-sparkle']), \
             patch.object(setup.sys.stdin, 'isatty', return_value=False), \
             patch.object(setup, 'with_keychain') as unlock:
            with self.assertRaisesRegex(ValueError, 'own Terminal'):
                setup.main()
        unlock.assert_not_called()


if __name__ == '__main__':
    unittest.main()
