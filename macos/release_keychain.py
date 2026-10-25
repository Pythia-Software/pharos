"""One interactive unlock for a release command; never handles its password.

Only explicit release commands use this module. Setup and key migration are
separate operator actions; missing keys must never trigger imports or generation.
"""
import fcntl
import os
from pathlib import Path
import shlex
import signal
import subprocess
import sys
import tempfile

KEYCHAIN = Path.home() / 'Library/Keychains/pharos-release.keychain-db'
SESSION = 'PHAROS_RELEASE_KEYCHAIN_SESSION'


def sparkle_access_command(directory, action, keychain, tools, account):
    """Build a metadata-only ACL helper; no signing secrets enter Python."""
    source = Path(__file__).with_name('sparkle-keychain-access.swift')
    binary = Path(directory) / 'sparkle-keychain-access'
    sdk = subprocess.check_output(
        ['xcrun', '--sdk', 'macosx26.5', '--show-sdk-path'], text=True).strip()
    subprocess.run(['swiftc', '-sdk', sdk, '-suppress-warnings', str(source),
                    '-o', str(binary)], check=True)
    return [str(binary), action, str(keychain), account,
            str(tools / 'generate_keys'), str(tools / 'sign_update')]


def check_sparkle_access(tools, account):
    keychain = Path(os.environ.get('PHAROS_RELEASE_KEYCHAIN', KEYCHAIN)).expanduser().resolve()
    with tempfile.TemporaryDirectory(prefix='pharos-keychain-access-') as directory:
        subprocess.run(sparkle_access_command(directory, 'check', keychain, tools, account),
                       check=True)


def security(*args, capture=False):
    result = subprocess.run(['/usr/bin/security', *map(str, args)], check=True,
                            text=True, stdout=subprocess.PIPE if capture else None)
    return result.stdout if capture else None


def search_list():
    return shlex.split(security('list-keychains', '-d', 'user', capture=True))


def with_keychain(command):
    """Run a command with serialized access to the dedicated release keychain."""
    keychain = Path(os.environ.get('PHAROS_RELEASE_KEYCHAIN', KEYCHAIN)).expanduser().resolve()
    if os.environ.get(SESSION) == str(keychain):
        return subprocess.call(command)
    if not keychain.is_file():
        raise ValueError(f'Release keychain missing: {keychain}. Complete the dedicated-keychain setup in docs/releases-and-updates.md; release commands never create or import signing keys.')
    # A second deployment must not unlock or relock an active deployment's keys.
    # The persistent lock file contains no credentials. flock releases on exit.
    with keychain.with_suffix(keychain.suffix + '.release-lock').open('a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError('Another deployment is using this release keychain.') from None
        return run_session(keychain, command)


def run_session(keychain, command):
    previous_handlers = {}
    child = None
    original = search_list()
    # codesign can resolve a private key through the global search list even
    # with --keychain. An existing login copy must never take precedence.
    preferred = [str(keychain), *[path for path in original if path != str(keychain)]]
    changed_search = preferred != original
    cleanup_errors = []

    def interrupted(number, frame):
        raise InterruptedError(number)

    try:
        for number in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
            previous_handlers[number] = signal.signal(number, interrupted)
        # Establish a new window even if an operator left the keychain unlocked.
        security('lock-keychain', keychain)
        print('Unlock the Pharos release keychain for this deployment in the macOS prompt.', flush=True)
        security('unlock-keychain', '-u', keychain)
        # Preserve login/system keychains for certificate chains and gh auth.
        if changed_search:
            security('list-keychains', '-d', 'user', '-s', *preferred)
        environment = dict(os.environ, PHAROS_RELEASE_KEYCHAIN=str(keychain),
                           PHAROS_CODESIGN_KEYCHAIN=str(keychain),
                           PHAROS_NOTARY_KEYCHAIN=str(keychain))
        environment[SESSION] = str(keychain)
        # Keep the whole build tree addressable so interruption stops signing
        # descendants before the wrapper closes its signing window.
        child = subprocess.Popen(command, env=environment, start_new_session=True)
        return child.wait()
    except InterruptedError as error:
        return 128 + error.args[0]
    finally:
        # Further interrupts cannot skip the keychain lock during cleanup.
        for number in previous_handlers:
            signal.signal(number, signal.SIG_IGN)
        try:
            if child is not None:
                try:
                    os.killpg(child.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
            # Lock before waiting for child termination or restoring search paths.
            try:
                security('lock-keychain', keychain)
            except (OSError, subprocess.CalledProcessError) as error:
                cleanup_errors.append(error)
            if child is not None:
                try:
                    child.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    try:
                        os.killpg(child.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    child.wait()
            if changed_search:
                try:
                    # Restore precedence, preserving unrelated additions/removals.
                    current = search_list()
                    restored = [path for path in original if path in current]
                    restored.extend(path for path in current
                                    if path not in original and path != str(keychain))
                    security('list-keychains', '-d', 'user', '-s',
                             *restored)
                except (OSError, subprocess.CalledProcessError) as error:
                    cleanup_errors.append(error)
        finally:
            for number, handler in previous_handlers.items():
                signal.signal(number, handler)
        if cleanup_errors:
            raise ValueError('Release keychain cleanup failed. Lock the Pharos release keychain in Keychain Access and check its search list.') from cleanup_errors[0]
        print('Pharos release keychain locked.', flush=True)


def main():
    command = sys.argv[1:]
    if command[:1] == ['--']:
        command = command[1:]
    if not command:
        raise ValueError('Usage: python3 macos/release_keychain.py -- COMMAND [ARGUMENT ...]')
    from release_support import CONFIG, require_release_machine
    import json
    require_release_machine(json.loads(CONFIG.read_text()))
    return with_keychain(command)


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))
