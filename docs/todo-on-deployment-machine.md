# To do on the deployment Mac

Everything here runs on the one Mac that signs and publishes Pharos releases.
Do it in this order. Development Macs never need any of it, and none of the
private material created here may leave this Mac except as the 1Password
backup described in step 4. Background and rationale live in
[releases-and-updates.md](releases-and-updates.md).

## 1. One-time machine prerequisites

Confirm each item before touching keys.

- Xcode Command Line Tools, with the macOS 26.5 SDK the release script selects
  by default. Check with `xcrun --sdk macosx26.5 --show-sdk-path`. If that SDK
  is absent, decide on another compatible SDK and export `SDKROOT` for every
  release command.
- Go, Node 24 LTS via `nvm use` in the checkout, Python 3, `curl`, `uv` (the
  release script runs `dmgbuild` with `uvx`).
- `gh auth login` for an account that can create releases and push tags in
  `Pythia-Software/pharos`. Protect that account with hardware-backed 2FA: the
  GitHub release asset that holds the update feed is a trust anchor.
- A clone whose `origin` is `github.com/Pythia-Software/pharos`. Run
  `tools/setup.sh` once so the frontend bundle exists.
- The **Developer ID Application: Pythia Software LLC** certificate and its
  private key in the login Keychain. Check with
  `security find-identity -v -p codesigning`. If several match, plan to set
  `PHAROS_CODESIGN_IDENTITY` to the intended certificate's SHA-1.
- A `notarytool` Keychain profile named `pharos`:
  `xcrun notarytool store-credentials pharos`.
- Fetch and verify the pinned Sparkle distribution: `macos/fetch-sparkle.sh`.
  It checks the SHA-256 in `macos/sparkle.json` against the download.

## 2. Create the Sparkle signing key (once, ever)

On a branch, with a clean checkout:

```sh
python3 macos/setup-release-machine.py --enable
```

Because `public_key` in `macos/sparkle.json` is `null`, this generates the
Ed25519 key in the login Keychain as the item **Private key for signing
Sparkle updates**, account **pharos**. It writes only the public key into
`macos/sparkle.json` and creates the opt-in marker at
`~/Library/Application Support/Pharos Release/release-machine.json`.

Do not run this script again on any other Mac. Running it elsewhere with a
null public key would create a second, unrelated key.

## 3. Verify the key before backing it up

```sh
build/Sparkle-2.10.0/bin/generate_keys --account pharos -p
python3 macos/release_support.py
```

The first command must print exactly the `public_key` now in
`macos/sparkle.json`. The second must exit silently with status 0. Record
the public key; step 4 and step 5 compare against it.

## 4. Back up the private key to 1Password

Do this before the first updater-enabled release. Installed apps will only
ever accept update feeds signed by this key. If the key is lost, every
installed copy loses automatic updates permanently; there is no rotation
path without the old key. A compromised key is recoverable, a lost key is not.

1. Export it to a temporary file:

   ```sh
   TMP=$(mktemp -d)
   build/Sparkle-2.10.0/bin/generate_keys --account pharos -x "$TMP/sparkle-private.key"
   ```

   Keychain asks to allow access to the item. Choose **Allow**, not
   **Always Allow**.

2. In 1Password, create a new **Password** item in a vault only you can
   access:
   - Title: `Pharos Sparkle EdDSA signing key`
   - Password field: the single line from
     `cat "$TMP/sparkle-private.key"`. Keep the field concealed.
   - A text field `public key`: the value from step 3.
   - Notes: the Keychain account name `pharos`, the Sparkle version used to
     create it (`2.10.0`), and the restore command from step 5.

   Do not attach the file itself. The string is the entire secret, and a
   concealed field gets 1Password's clipboard clearing and reveal-on-demand.

3. Destroy the temporary file and clear the clipboard:

   ```sh
   rm -P "$TMP/sparkle-private.key" 2>/dev/null || rm "$TMP/sparkle-private.key"
   rmdir "$TMP"
   pbcopy < /dev/null
   ```

4. Confirm the vault is covered by your 1Password Emergency Kit and that a
   second trusted device is signed in. Losing the 1Password account would
   lose this backup too.

## 5. Test the restore once

Use a scratch macOS user account or a second Mac, never the deployment Mac's
own login Keychain. Paste the value from 1Password into a file and import it:

```sh
printf '%s' '<value from 1Password>' > sparkle-private.key
generate_keys --account pharos -f sparkle-private.key
generate_keys --account pharos -p
rm -P sparkle-private.key
```

The printed public key must match step 3. Then delete the imported
**Private key for signing Sparkle updates** item from that scratch account in
Keychain Access, so the key exists only on the deployment Mac and in
1Password. Do not skip this step: it is the only proof the backup works.

## 6. Commit the public configuration

Commit `macos/sparkle.json` with the new `public_key`, open a PR, and merge it
to `master`. Nothing else from this process enters the repository. The
release script refuses to build an updater-enabled release until this commit
is on `origin/master` and the local Keychain key matches it.

## 7. Create the update feed

Optional, since `macos/release.sh` does it on the first Developer ID release,
but running it alone surfaces GitHub permission problems early:

```sh
python3 macos/update-feed.py bootstrap
```

This creates the `update-feed` prerelease with an empty signed `appcast.xml`.
Verify it with the public key only:

```sh
curl -fsSL -o /tmp/appcast.xml \
  https://github.com/Pythia-Software/pharos/releases/download/update-feed/appcast.xml
macos/verify-update.sh "$(plutil -extract public_key raw macos/sparkle.json)" /tmp/appcast.xml
```

## 8. First updater-enabled release

Follow the normal quick release workflow in
[releases-and-updates.md](releases-and-updates.md#quick-release-default).
Two things are specific to this first release:

- Copies installed before it have no updater, so users install this one
  manually. Automatic updates begin with the release after it.
- Before publishing the *second* updater-enabled release, install the first
  one on a test Mac from its DMG, copy it into Applications, and confirm
  **Check for Updates…** finds the new version, installs it, and relaunches
  with the service running. That exercises the full chain once with a real
  installed population of one.

## 9. Ongoing rules

- Never run `setup-release-machine.py` again, and never change `public_key`
  in `macos/sparkle.json` for an installed population.
- To move to a new deployment Mac, import the key from 1Password with
  `generate_keys --account pharos -f`, re-run step 3, then recreate the
  opt-in marker by running `setup-release-machine.py --enable` (with the
  public key already committed it only verifies and writes the marker).
  Remove the key from the old Mac's Keychain.
- If the key is ever compromised, keep it until rotation is complete:
  installed apps need one more feed signed by the old key to learn the new
  one. See the key rotation notes in
  [releases-and-updates.md](releases-and-updates.md#separate-development-and-deployment-macs).
- Treat the GitHub account and repository settings as part of the trust
  chain: tag protection on `v*` and `update-feed`, and no shared
  credentials.
