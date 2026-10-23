"""Release-machine opt-in. Ordinary builds/tests never read private keys."""
import base64
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CONFIG = ROOT / 'macos/sparkle.json'
MACHINE_FILE = Path.home() / 'Library/Application Support/Pharos Release/release-machine.json'


def validate_public_key(value):
    if not isinstance(value, str) or len(base64.b64decode(value, validate=True)) != 32:
        raise ValueError('Release blocked: the Sparkle public key is not configured. Report the missing initial deployment setup; do not create or import signing keys during a release task.')
    return value


def require_release_machine(config, machine_file=None):
    """Fail before fetching tools or accessing Keychain on development Macs.

    This opt-in prevents accidental prompts; the absence of private keys on
    development Macs is the actual security boundary.
    """
    path = machine_file or MACHINE_FILE
    if not path.is_file():
        raise ValueError('Release signing and feed publication are disabled on this Mac. Report this blocker and use the already configured deployment Mac. Do not run setup or create/import keys to repair a release failure. Development builds and tests need no release credentials.')
    public_key = validate_public_key(config.get('public_key'))
    machine = json.loads(path.read_text())
    if machine.get('enabled') is not True or machine.get('public_key') != public_key:
        raise ValueError('Release blocked: this Mac is not configured for the committed Sparkle public key. Report the configuration mismatch; do not run setup or create/import keys during a release task.')


if __name__ == '__main__':
    try:
        require_release_machine(json.loads(CONFIG.read_text()))
    except (ValueError, OSError) as error:
        raise SystemExit(str(error))
