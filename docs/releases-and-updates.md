# Releases and app updates

For the release introducing managed libraries, include the
[library management upgrade notes](onboarding-and-library-upgrade-notes.md),
especially the requirement to keep Pharos running for managed MCP access.

## Publish a release from a Mac

Pharos releases are built locally. GitHub Actions is not involved. A release
contains a universal macOS app (arm64 and x86_64) in a Finder disk image,
`vX.Y.Z-Pharos-macos-universal.dmg`, whose volume is named `vX.Y.Z Pharos`,
plus a matching `.sha256` file and signed-update metadata (`update.json`).
The tag, GitHub release, and both bundle
version fields use the same `X.Y.Z` version.

### Quick release (default)

Routine publishing requests use the short path below. Code review and testing
belong to the changes merged into `master`; publishing does not repeat those
test suites. Do not run extra Go, JavaScript, browser, native or end-to-end
tests, investigate unrelated test failures, or rewrite the generated release
notes unless the user asks. The release script already installs locked
dependencies, checks schemas, builds the frontend and universal app, verifies
signatures and the disk image, and creates the checksum before publishing.

Sign in to `gh` with permission to publish releases. From a clean workspace
branch, fetch master, temporarily check out its exact commit, and run the
release script with the requested version. The repository's main branch is
`master`. There is no need to merge it into the workspace branch just to
publish. For example, to publish the next patch after v0.7.0:

```sh
(
    set -eu
    release_branch=$(git branch --show-current)
    test -n "$release_branch"
    test -z "$(git status --porcelain)"
    git fetch origin master --tags
    git switch --detach origin/master
    trap 'git switch "$release_branch"' EXIT
    macos/release.sh 0.7.1
)
```

If the workspace has uncommitted work, use a separate clean release checkout
and preserve that work. If the release command fails, address that specific
failure and resume. On success, return the GitHub release and direct DMG
download links. No additional publishing confirmation is needed when the user
has requested the release.

### Signing and packaging details

The default is a Developer ID signed and notarized release.

Use `macos/release.sh X.Y.Z --adhoc` to explicitly publish an ad hoc signed release.
GitHub will host an ad hoc release, but it cannot be notarized. macOS may block the downloaded
app on first launch. A user who trusts the download can try opening it, then
choose **System Settings → Privacy & Security → Open Anyway**. See
[Apple's instructions](https://support.apple.com/guide/mac-help/open-a-mac-app-from-an-unknown-developer-mh40616/mac).
Ad hoc code signatures also change when the app changes, so macOS may ask again
for privacy permissions after an update.

For a notarized release, set up a **Developer ID Application** certificate in
the Mac's keychain and a `notarytool` keychain profile. Apple requires Developer
ID signing, the hardened runtime, and a secure timestamp for notarization. See
Apple's [Developer ID](https://developer.apple.com/developer-id/) and
[notarization](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)
guides. Run `xcrun notarytool store-credentials pharos` to create the keychain
profile.

For a Developer ID release, the script defaults to the signing identity
`Developer ID Application: Pythia Software LLC` and the `pharos` notarytool
keychain profile:

```sh
macos/release.sh 0.7.1
```

Override `PHAROS_CODESIGN_IDENTITY` and `PHAROS_NOTARY_PROFILE` when using a
different certificate or keychain profile. The default identity is a
certificate-name prefix; if multiple certificates match, set the identity to
the intended certificate's SHA-1 fingerprint. Private keys and notarization
credentials stay in Keychain.

Releases select the macOS 26.5 SDK by default: the standalone macOS 27
Command Line Tools SDK references a SwiftUI macro plugin not included with
those tools. Set `SDKROOT` explicitly to use another compatible SDK.

The script fetches `origin/master`, refuses a dirty or different checkout and
an existing tag, builds and verifies the app, then creates an annotated tag
and publishes a GitHub release. The Developer ID path also signs the final
disk image, notarizes and staples both app and image, signs the final image
with Sparkle, verifies the uploaded bytes, and publishes the signed appcast last. It keeps the disk image and
checksum under `dist/releases/vX.Y.Z/`. If publication fails after the tag is
pushed, inspect the tag and draft release before retrying; never replace the
asset of a published version. The disk image's Finder layout is written by
[dmgbuild](https://dmgbuild.readthedocs.io/), which the script runs with `uvx`,
so install [uv](https://docs.astral.sh/uv/) on the release Mac. Finder is not
scripted, and its settings on the release Mac do not affect the layout.

The disk image opens in Finder. Drag `Pharos.app` onto the Applications shortcut
and open the installed copy. The setup guide asks where the library lives:
- **On this Mac** creates it under
  `~/Library/Application Support/Pharos/Libraries/<name>/`.
- **An attached SSD** creates it in a folder you choose on the drive, such as
  `/Volumes/YOUR-DRIVE/Pharos`.

Either way, the library folder holds `library.toml`, the catalog, captures,
staging and preserved data. Keep the folder together when moving it between
Macs. Source paths are disabled until you opt in. More libraries can be created,
opened or switched in **Settings → Libraries** (see
[Libraries and application setup](library-management.md)).

To update, quit Pharos, drag `Pharos.app` from the image onto Applications,
choose **Replace**, then open the new app and approve macOS access prompts.
Libraries, including those on drives, are separate folders, so an app update
never touches them. If macOS blocks an ad hoc signed build, use **System
Settings → Privacy & Security → Open Anyway** after trying to open it.

The bundled CLI can also initialize a library folder on a drive. Then open it
from **Settings → Libraries → Open a library…**:

```sh
/Applications/Pharos.app/Contents/MacOS/pharos init-library "/Volumes/YOUR-DRIVE/Pharos"
```

Agents' MCP servers run from the app in Applications as
`pharos mcp-app --application /Applications/Pharos.app`, and answer for the
library assigned in **Library used by MCP**. Quit Pharos and those clients
before replacing the app, then reconnect Pharos in each client.

**Older portable installs** ran a copy of `Pharos.app` from the drive, next to
the library. To migrate:
1. Install Pharos in Applications.
2. Open the drive's library with **Settings → Libraries → Open a library…**.
3. Re-register agents with the `mcp-app` command above.
4. Delete the drive's old app copy. An outdated copy refuses newer catalogs (for
   example, "schema version 21, but this build … supports up to 14").

The source checkout's `macos/install-library.sh` remains useful for development
builds and adoption from a per-user install.

## In-app updates

Developer ID releases embed pinned [Sparkle 2](https://sparkle-project.org/).
**Check for Updates…** uses Sparkle's native release notes, download progress,
verification, installation and relaunch UI. The same signed, notarized DMG is
used for first installs and updates. No browser or Finder replacement is
required after installing the first Sparkle-enabled release. v0.7.1 and older
still need one final manual replacement to obtain the updater.

When Sparkle offers an installable release, the library header shows
an update icon between drive status and Refresh. One click downloads,
verifies, installs and relaunches Pharos, after the same safe process handoff
as the native updater. A dot marks an available update, a ring shows download
progress, a package marks preparation, and a restart arrow marks installation.
Hover text includes the version, current stage and download percentage.
The icon stays the same width across states; the header measures the full
combo again as the icon appears or disappears and moves it
into **More tools** when space is limited. macOS may still request authorization
if the installed app requires it. The action is available in the native app;
browser sessions and local builds do not offer production updates.

**Automatically Check for Updates** uses Sparkle's daily schedule. An explicit
choice from the old **Check for Updates on Launch** setting is migrated once;
otherwise Sparkle asks permission on its second launch, after onboarding.
**Automatically Download and Install Updates** is a separate opt-in, initially
off. Sparkle can then download and verify silently and install on quit; a
long-running app receives its standard gentle reminders. System profiling and
release-note JavaScript stay off. Ad hoc/local builds disable production checks.

### Signing keys and dependency

`macos/sparkle.json` pins the upstream framework version and archive SHA-256,
the stable feed URL, the public Ed25519 key, and the Keychain account `pharos`.
`macos/fetch-sparkle.sh` verifies the upstream archive and extracts it into
ignored `build/`. Framework helpers are signed inside-out before the outer app;
Developer ID releases retain hardened runtime and library validation. Ad hoc
and self-signed local builds use a development-only library-validation exception.
Generated framework binaries must not be committed.

### Separate development and deployment Macs

Development Macs build and test without either release private key. Production
signing, notarization, update-feed signing and publication run only on the clean
deployment Mac. Apple's Developer ID key and Sparkle's Ed25519 key authorize
different parts of the update chain, and both stay on that deployment Mac.
The repository and development Macs receive only Sparkle's public key.

Before the first updater-enabled release, run this **on the deployment Mac**:

```sh
python3 macos/setup-release-machine.py --enable
```

This explicit setup generates the Sparkle key there if `public_key` in
`macos/sparkle.json` is still null. It writes only the public key to that file,
and a local opt-in under
`~/Library/Application Support/Pharos Release/release-machine.json`. Commit the
public configuration and bring that commit back to development; private key
material and the machine opt-in never enter the repository. Merge the public
configuration to master before using the ordinary quick-release workflow.
Apple's Developer ID certificate and `notarytool` profile are configured only
on the deployment Mac using the existing signing setup above.

Before publishing, complete [One unlock per deployment](#one-unlock-per-deployment)
to move the existing signing keys into the dedicated release keychain. The
ordinary release command requires that keychain and never creates it itself.

Until this deployment setup is complete, local builds still work with updates
disabled. Production builds and release/feed commands fail before accessing
Keychain. `macos/release.sh X.Y.Z` stays the normal release command on the
deployment Mac; no additional opt-in is needed for each release. The local
marker prevents accidental commands and prompts, while keeping private keys
off development Macs supplies the security boundary.

The private update key initially appears as **Private key for signing Sparkle
updates**, account **pharos**, in the deployment Mac's login Keychain; the
dedicated-keychain setup below moves it into the release keychain. The release
script verifies that it matches the committed public key before building. It never
prints or exports the private key. Back it up securely on the deployment side
before the first updater-enabled release. Do not transfer that private key to
a development Mac. When replacing a deployment Mac, use Sparkle's secure
export/import workflow between deployment machines only.

Local tests use disposable in-memory test keys and public signed fixtures.
`macos/verify-update.sh PUBLIC_KEY FILE [SIGNATURE]` verifies with CryptoKit and
never calls Keychain or `sign_update`. Even on the deployment Mac, verification
uses the public key; only signing reads the private key.

Do not replace the public key for an existing installed population. See
Sparkle's [key rotation guidance](https://sparkle-project.org/documentation/#rotating-signing-keys):
rotate only one trust anchor at a time. Our Developer ID-signed DMGs preserve
its recovery route when Ed25519 verification before extraction is enabled.

### One unlock per deployment

Developer ID releases and standalone feed commands run inside
`macos/release_keychain.py`. It prompts through macOS to unlock a dedicated
`~/Library/Keychains/pharos-release.keychain-db`, runs the command, and locks
the keychain when the command finishes, fails, or receives SIGINT, SIGTERM or
SIGHUP. Passwords never enter command arguments, environment variables or logs.
Nested feed commands share the release's unlock. Ad hoc builds do not unlock it.
For a staging build, wrap the entire build/sign/publish command:

```sh
python3 macos/release_keychain.py -- YOUR-STAGING-COMMAND
```

One-time setup, explicitly on the deployment Mac:

1. Run `python3 macos/setup-release-keychain.py --create`. Choose a new password
   in the macOS dialog and save it in the release administrators' 1Password
   vault. Do not save it in the login keychain. The script creates an empty
   keychain, configures locking on sleep and after four hours of inactivity,
   and leaves it locked. It never creates or imports signing keys.
2. In Keychain Access, use **File → Add Keychain** to open that file, then
   unlock it. Move the existing Developer ID certificate **and its private
   key** from login into this keychain. Preserve a secure recovery backup
   before removing the original. Verify the certificate and private key both
   appear under **My Certificates** in the new keychain. Do not issue a new
   certificate or generate a new Sparkle key.
3. In that private key's **Access Control**, authorize `/usr/bin/codesign`
   under **Always allow access by these applications**. Keep **Confirm before
   allowing access** selected for other applications. Do not allow all apps.
   Imported keys may also need the partition-list authorization described below.
4. Move **Private key for signing Sparkle updates**, account **pharos**, to
   the same keychain. Run `python3 macos/setup-release-keychain.py --authorize-sparkle`
   in your own Terminal once to authorize the pinned `bin/sign_update` and
   `bin/generate_keys` tools for that existing item. This setup can ask for unlock
   and access-control approval. Apple's `security` utility then asks for the release-keychain
   password in Terminal to update partition permissions; the password is not
   supplied with `-k` and never enters Python, arguments, environment or logs.
   Setup never reads, exports, creates or imports the key. It preserves
   unrelated trusted applications and partition permissions. It updates only
   that item's decrypt ACL and adds the exact code-directory hashes of both
   pinned helpers to its partition ACL. Both layers are required: trusted paths
   alone can still produce one password prompt per Sparkle invocation.
   Repeat this explicit setup after changing the pinned Sparkle tools or moving
   the release checkout. Ordinary release/feed commands check this authorization
   (both paths and code hashes) without password dialogs before accessing the key,
   so missing setup fails before building instead of interrupting publication.
   The deployment itself is intended to need only the wrapper's initial unlock; confirm the observed
   prompt count with the rehearsal below.
5. In your own Terminal, run
   `python3 macos/setup-release-keychain.py --configure-notary`.
   It unlocks the release keychain and runs Apple's interactive
   `notarytool store-credentials pharos --keychain ...` against that keychain.
   At the optional App Store Connect API private-key prompt, press Return to use
   Apple ID authentication. Enter your Apple ID, team ID and app-specific password
   directly in Apple's prompts; never paste credentials into agent chat or shell arguments. Existing
   profiles in login or Local Items are not moved or deleted. The command then
   rehearses credential access in the same unlocked session and locks on exit.
   Ordinary releases explicitly select this profile in the dedicated keychain;
   there is no fallback to login or Local Items. GitHub authentication remains
   managed separately by `gh` and is checked in the rehearsal.

Do not duplicate the signing private keys in login; otherwise locking the
release keychain would leave the login copies available. Lock the release
keychain when setup is finished.

To verify an already configured deployment Mac, run
`python3 macos/check-release-credentials.py`. It starts a fresh unlock session,
creates and verifies two disposable Developer ID signatures and two Sparkle
signatures, authenticates to Apple's history API using the dedicated keychain,
and checks GitHub authentication and repository write permission. It publishes
nothing. Its success message confirms credential operations, not a prompt count.
Successful output plus observing only the initial unlock prompt is
the acceptance check for a one-password deployment on this Mac. Repeat after
changing credentials or tool permissions. A timed-out or sleeping keychain,
revoked credentials or changed tools may require setup or another unlock.

If `codesign` still prompts separately after the keychain is unlocked, configure
the imported key's partition list once in your own Terminal. For a key whose
label is **Imported Private Key**, run:

```sh
(
    trap '/usr/bin/security lock-keychain "$HOME/Library/Keychains/pharos-release.keychain-db"' EXIT
    /usr/bin/security set-key-partition-list -S apple: -s -t private \
        -l 'Imported Private Key' "$HOME/Library/Keychains/pharos-release.keychain-db"
)
```

Enter the release-keychain password at the Terminal prompt. Do not supply `-k`
with the password: that would expose it in command arguments. This selects only
the named private signing key in the dedicated keychain; the application's
existing Access Control authorization still applies. Adjust the label/path for
a different imported key. Verify two signatures within one wrapper invocation
before removing the original identity from login. Apple's `security(1)` requires
the `apple:` partition for `/usr/bin/codesign`.

The wrapper temporarily puts this keychain first in the user's search list so
Sparkle and code signing find its keys ahead of any remaining login copies. It
restores the previous order afterward, preserving unrelated additions/removals,
and explicitly selects the release keychain for code signing. It serializes
deployments using the same keychain.
It does not make the keychain the default or lock the login keychain. Use
`PHAROS_RELEASE_KEYCHAIN` to select a different dedicated keychain path.

Any process running as this user can still invoke the authorized tools during
the unlocked deployment window. This bounds access in time; it does not isolate
signing from other processes running as the same user. A forced kill (SIGKILL),
power loss, or machine failure cannot run cleanup; the sleep/inactivity lock
is a fallback. Lock the keychain manually after a forced termination. A release
long enough to hit that inactivity timeout can require another unlock.

### Feed publishing, recovery and withdrawal

The stable feed is:

`https://github.com/Pythia-Software/pharos/releases/download/update-feed/appcast.xml`

It lives on a dedicated **prerelease** called `update-feed`, which is excluded
from GitHub's latest stable app release. No GitHub Pages or custom-domain setup
is required. Both the appcast and its embedded notes are signed; the app also
requires the archive's Ed25519 signature before extraction. Only published,
Developer ID-signed, stapled stable releases enter this feed.

The normal `macos/release.sh X.Y.Z` command performs all steps. It verifies or
bootstraps an empty signed feed, builds and notarizes, creates a draft release,
signs the final stapled DMG, uploads immutable `update.json`, publishes the
release, downloads and checks the public artifact, then signs and replaces the
feed. The archive signature covers the exact final bytes, including stapling.
`CFBundleVersion` and the appcast's machine version remain the same monotonic
`X.Y.Z` value; macOS compatibility is declared separately in the feed.
The feed's minimum macOS version is read from `LSMinimumSystemVersion` in the
app inside the final DMG and checked again against the downloaded artifact.

On the deployment Mac, the feed can also be initialized before the first release:

```sh
python3 macos/update-feed.py bootstrap
```

If a release was published but feed publication failed, leave the tag and assets
alone and resume just the final step:

```sh
python3 macos/update-feed.py publish X.Y.Z --directory dist/releases/vX.Y.Z
```

This downloads the published DMG and metadata, verifies size, SHA-256, Ed25519,
Developer ID signature and notarization ticket, and verifies the previous signed
feed before adding an item. Repeating an already advertised identical version
is a no-op. If the failure occurred while the release was still a draft, inspect
its assets first: `prepare X.Y.Z --directory ...` creates `update.json` only if
it does not already exist; upload that file without `--clobber`, then publish
the draft and run `publish`. Never rebuild/re-upload an existing app version.

To stop offering a bad update without deleting its immutable release artifacts:

```sh
python3 macos/update-feed.py withdraw X.Y.Z
```

Withdrawal cannot recall downloads that have already been staged for
installation or cryptographically revoke an authentic archive. Sparkle 2.10
uses a safe fallback when feed signature validation fails: it suppresses release
notes, links and some metadata, but can still offer a correctly signed archive.
Archive Ed25519 verification and Apple code signing protect the installed code;
feed signing alone does not prevent a compromised feed host from replaying a
withdrawn, previously signed release. Recover affected users with a
higher-version corrective release.
Executable rollback is not a database rollback: an older app deliberately
refuses a newer catalog schema. Back up libraries before incompatible schema
upgrades and retain the existing schema guard.

Feed publication is serialized on one release checkout. Coordinate publishers
across different Macs/checkouts: GitHub has no conditional/atomic asset
replacement API. `--clobber` can briefly make the feed unavailable; Sparkle fails
to check during that gap and the installed app keeps working. Publication downloads the asset
again and compares exact bytes before reporting success. If replacement leaves
no feed, restore the already signed `dist/releases/vX.Y.Z/appcast.xml` to the
`update-feed` release and verify readback; do not re-sign unknown remote content.

### Shutdown and portable libraries

Every application termination passes an asynchronous catalog-close gate,
including ordinary quit and Sparkle installation on quit. Installation marks the
app pending, refuses new bundled CLI/MCP launches, lets the service finish its
writes, stops its MCP connections, and takes an exclusive lease only after old
processes exit. Another CLI command or another app instance defers installation;
it is never force-killed for an update. Before an explicit Install and Relaunch,
Pharos performs this preparation through Sparkle's public user-driver callbacks.
If an install-on-quit handoff is unsafe, Pharos resumes the staged installer and
cancels it through the public Skip reply before allowing ordinary Quit. Ordinary
Quit sends SIGTERM to its owned service, which can finish closing independently;
a busy catalog does not veto logout or restart. Agent clients may need to reconnect.
If Sparkle reports an installation failure after preparation, Pharos releases the
handoff marker and lease immediately and clears its shutdown state. A stopped
service shows instructions to reopen the app; the same version can reopen and
CLI/MCP can reconnect without waiting for stale-marker recovery.

The per-install lease and handoff marker live in this Mac's Application Support
`Pharos/updates/`, keyed by the canonical app path. They survive the old app's
exit, preventing reconnects in the replacement gap. The new app or a bundled
CLI/MCP launch recognizes a changed installed version after acquiring its lease;
CLI/MCP do not require a GUI relaunch after installation on quit. PID recovery
checks the executable path and process start time to distinguish reused PIDs. A
failed handoff to the same old version is recoverable after five minutes once
the original updater is gone and no CLI lease remains. State does not travel
with the library; installs at different paths have separate locks.

Sparkle replaces only the running app bundle, in Applications or beside
`library.toml`. Catalog, captures, configuration and preserved files remain
outside it. Updates require a writable, local APFS or Mac OS Extended volume;
read-only disk images, translocated apps, network volumes and FAT/exFAT installs
receive an actionable error. Permissions for protected install locations are
handled by Sparkle. Eject cancels any active download or staged installation
through Sparkle before releasing the library and unmounting the portable drive.
Sparkle may skip a staged version when cancellation uses its Skip reply; the user
can offer it again with Check for Updates. If Sparkle cannot confirm cancellation,
Pharos reports the failure and keeps the drive mounted.

### Verifying an updater release

Run the fast Go tier, Python tools tests and `macos/test/run-light.sh` after
updater changes. These checks use no production private keys or Keychain access.
The native light harness covers staged-update Skip versus Later, explicit
installer/download cancellation, shutdown before the service listener opens,
ordinary Quit with a busy/stubborn child, and readiness recovery after a deferred
installation. It also covers installer failure after a successful handoff,
immediate lease/marker cleanup and duplicate failure callbacks. It
uses disposable child processes and stubbed HTTP, and fetches pinned Sparkle
only when uncached. `macos/test/swift-harness.sh` tests the real Swift/Go lease
handoff and MCP reconnect refusal on a throwaway APFS volume, as well as service
release and eject. It never launches the GUI or touches a user's library.

Before the first public updater release, perform a signed/notarized round trip
on the release Mac. Use a separate checkout with a staging HTTPS feed URL in
`macos/sparkle.json`, and two increasing test versions signed with the production
trust anchors. Keep this test feed separate from the production `update-feed`;
`sign_update` signs a manually generated staging appcast just as it signs the
production feed. Install the older app in a throwaway portable library (unused
port, never 8765/8766), and offer the newer DMG through that feed. Repeat on both
supported CPU architectures and with the app installed in Applications.

Check explicit Install and Relaunch and opt-in installation on ordinary quit;
confirm the new version, a healthy service, catalog integrity, retained privacy
grants, and unchanged sibling library files. Repeat with active indexing and
MCP connections, a CLI writer, an unavailable network, a tampered archive/feed,
insufficient disk space, a read-only installation, an external-drive disconnect,
and another installed copy. Rejected/deferred updates must preserve the old app
and library. A native build/signature check or lease harness alone is not this
full signed installation test.

## Upgrading an existing library

This build upgrades catalogs to schema 21 and writes capture manifest version 5,
combining durable preservation with managed-library delivery safeguards. Earlier
builds refuse these versions. Take and verify a backup of
both the catalog and captures before upgrading: rollback requires restoring
the pre-upgrade catalog and capture tree together, not just replacing the app.
New automatic retained-input options remain off unless explicitly configured.
SQLite row export during Capture requires `sqlite_record_capture = true`.

Cloud archives containing shared retained inodes use manifest version 2 and
hard-link entries. This build reads versions 1 and 2; earlier builds cannot
restore version 2. Keep the pre-upgrade backup for rollback. Local backup copies
and cloud restores on filesystems without hard links use ordinary files.

Before release, validate an upgrade and rollback against a separately reviewed
copy of a real library, including disk budget and destination. Fixture tests do
not substitute for that rollout validation; no production backfill is implied.

A new version can derive more from the transcripts a library already holds.
When it opens a catalog indexed by an older version, Pharos includes the
required upgrade in **Capture & index**, in the drive panel and on Settings →
Capture & index. The same action captures and indexes sources, then starts any
pending upgrade. It also runs the upgrade when this Mac has no enabled
sources. The drive panel shows one Capture & index line with its current step
and progress; **Upgrade details** opens the upgrade steps
and repository merge preview. A library already current has no upgrade work.

The upgrade runs these steps in order, as one background job:

1. **Merge repository identities.** Rows for one repository reached through
   different remote URLs, renames, worktrees, or checkouts without a remote
   become one. Pharos always checks with GitHub, through the `gh`
   command-line tool, which repositories were renamed or moved; nothing else
   contacts the network. **Preview repository merges** lists the merges first.
   Without `gh`, or when it isn't signed in (`gh auth login`), the step still
   runs, the panel says renamed repositories may stay separate, and the
   service merges them a few minutes after `gh` works. A new version of
   Pharos that recognizes more split repositories raises the repository
   merge's version, and a library that ran the older one is offered just this
   step again (about a minute, with no other step pending).
2. **Correct token attribution** for Claude sessions whose sub-agents were
   counted twice.
3. **Record harness versions**: which Claude Code or Codex version ran each
   conversation.
4. **Record loaded instructions**: which instruction files (`CLAUDE.md`,
   `AGENTS.md`, memory) and skills each Claude Code or Codex conversation
   loaded. A conversation whose transcript is gone from this Mac and the
   library's captures is marked unavailable rather than read again.
5. **Index saved reasoning**: extract saved thinking and reasoning summaries
   from retained messages, so the reasoning search target finds them.
6. **Repair message search**: restore proven Claude parent/child links, make
   delivered sub-agent reports readable, and repair phrase matching in
   retained tool and context text.
7. **Rebuild the tool ledger** with error signatures, test failures, and
   repository-relative paths. This is the longest step.

Only steps with pending work run. After them, the job rebuilds the Tools
rollup, so the first visit to Tools doesn't wait.

Repositories merge first because the tool ledger and the loaded-instructions
record store repository-relative paths. Every step reads the catalog's
retained messages; the harness and instructions steps also read source files
and captures where they still exist. Transcripts are never changed.

The job commits in small units. Closing the panel doesn't stop it, and the
drive badge shows its progress. Ejecting stops it after the unit in
progress, and using **Capture & index** again resumes where it stopped. Saved
table views that filter on a repository name the merge retired are pointed at
the surviving name.

From the command line, `pharos upgrade --status` reports what is pending,
`pharos upgrade --preview` lists the repository merges, and `pharos upgrade`
runs every step, printing progress and the time each step took. (`--github`
is still accepted and ignored; the lookup is no longer optional.)
