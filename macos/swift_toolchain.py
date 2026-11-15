"""Explicit SDK and isolated Swift cache for release tools; no Keychain access."""
import hashlib
import os
from pathlib import Path
import platform
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def configuration():
    if os.environ.get('ADDITIONAL_SWIFT_DRIVER_FLAGS'):
        raise ValueError('Unset ADDITIONAL_SWIFT_DRIVER_FLAGS; Pharos passes Swift options explicitly, not through global driver flags.')
    sdk = Path(os.environ.get('SDKROOT') or subprocess.check_output(
        ['xcrun', '--sdk', 'macosx26.5', '--show-sdk-path'], text=True).strip()).resolve()
    if not (sdk / 'SDKSettings.json').is_file():
        raise ValueError(f'Invalid macOS SDK: {sdk}. Set SDKROOT to an installed compatible SDK.')
    compiler = subprocess.check_output(['xcrun', '--find', 'swiftc'], text=True).strip()
    version = subprocess.check_output([compiler, '--version'], text=True, stderr=subprocess.STDOUT).strip()
    identity = hashlib.sha256(str(sdk).encode() + (sdk / 'SDKSettings.json').read_bytes()
                              + compiler.encode() + version.encode()).hexdigest()[:20]
    cache = ROOT / 'build/swift-module-cache' / identity
    return sdk, compiler, version, cache


def compile_swift(arguments, config=None):
    sdk, compiler, _, cache = config or configuration()
    cache.mkdir(parents=True, exist_ok=True)
    subprocess.run([compiler, '-sdk', str(sdk), '-module-cache-path', str(cache), *arguments],
                   env=dict(os.environ, SDKROOT=str(sdk)), check=True)


def preflight():
    config = configuration()
    print(f'Pharos Swift SDK: {config[0]}\n{config[2]}\nModule cache: {config[3]}', flush=True)
    # Exercise executable generation and SwiftUI State, not just typechecking:
    # a missing SwiftUIMacros plugin must fail before an interactive unlock.
    with tempfile.TemporaryDirectory(prefix='pharos-swift-preflight-') as directory:
        source = Path(directory) / 'probe.swift'
        source.write_text('''import SwiftUI
struct Probe: View {
    @State private var copied = false
    var body: some View { Text(copied ? "yes" : "no") }
}
@main enum Main {
    static func main() { precondition(CommandLine.arguments.dropFirst() == ["argument with spaces"]) }
}
''')
        for arch in ('arm64', 'x86_64'):
            print(f'Checking SwiftUI executable ({arch})…', flush=True)
            binary = str(Path(directory) / arch)
            compile_swift(['-parse-as-library', '-target', f'{arch}-apple-macos14.0',
                           str(source), '-o', binary], config)
            if arch == platform.machine():
                subprocess.run([binary, 'argument with spaces'], check=True)


def main():
    try:
        action, *arguments = sys.argv[1:]
        if action == 'sdk':
            print(configuration()[0])
        elif action == 'compile':
            compile_swift(arguments)
        elif action == 'preflight':
            preflight()
        else:
            raise ValueError('Usage: swift_toolchain.py sdk|compile|preflight')
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(f'Pharos Swift toolchain: {error}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
