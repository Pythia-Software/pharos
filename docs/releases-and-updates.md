# Releases and app updates

## Publish a release from a Mac

Pharos releases are built locally. GitHub Actions is not involved. A release
contains a universal macOS app (arm64 and x86_64) in a Finder disk image,
`Pharos-vX.Y.Z-macos-universal.dmg`, plus a matching `.sha256` file. The tag,
GitHub release, and both bundle version fields use the same `X.Y.Z` version.
Sign in to `gh` with permission to publish releases. Run the script only after
the release commit has been merged into `master`, from a clean checkout at
`origin/master`.

By default, publish an ad hoc signed release:

```sh
macos/release.sh 0.3.0
```

GitHub will host it, but it cannot be notarized. macOS may block the downloaded
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

For a Developer ID release:

```sh
export PHAROS_CODESIGN_IDENTITY='Developer ID Application: Your Name (TEAMID)'
export PHAROS_NOTARY_PROFILE=pharos
macos/release.sh 0.3.0 --developer-id
```

The script fetches `origin/master`, refuses a dirty or different checkout and
an existing tag, builds and verifies the app, then creates an annotated tag
and publishes a GitHub release. The Developer ID path also notarizes and
staples both the app and the final disk image. It keeps the disk image and
checksum under `dist/releases/vX.Y.Z/`. If publication fails after the tag is
pushed, inspect the tag and draft release before retrying; never replace the
asset of a published version. The disk image's Finder layout is written by
[dmgbuild](https://dmgbuild.readthedocs.io/), which the script runs with `uvx`,
so install [uv](https://docs.astral.sh/uv/) on the release Mac. Finder is not
scripted, and its settings on the release Mac do not affect the layout.

The disk image opens in Finder with an illustrated guide. For a **new portable
library**, create a dedicated folder such as `/Volumes/YOUR-DRIVE/Pharos`, drag
`Pharos.app` into it, and open that copy. Choose **Create Library Beside App**.
The app creates `library.toml`, catalog, captures, staging, and preserved data
in that folder. Keep the folder together when moving it between Macs.

For a **new install in Applications**, drag `Pharos.app` onto the Applications
shortcut in the disk image and open the installed copy. Choose **Set Up on This
Mac**. The app creates
`archive.toml`, the catalog, captures, staging, and preserved data under
`~/Library/Application Support/Pharos`; those files stay out of Applications.
Source paths are disabled until you opt in.

To update either installation, quit Pharos, drag `Pharos.app` from the image to
the folder where the original app was installed, choose **Replace**, then open
the new app and approve macOS access prompts. Only replace the app; leave the
library's other files in place. If macOS blocks an ad hoc signed build, use
**System Settings → Privacy & Security → Open Anyway** after trying to open it.

The bundled CLI can also initialize a portable library after copying the app:

```sh
"/Volumes/YOUR-DRIVE/Pharos/Pharos.app/Contents/MacOS/pharos" init-library "/Volumes/YOUR-DRIVE/Pharos"
open "/Volumes/YOUR-DRIVE/Pharos/Pharos.app"
```

A library's app runs from the drive, and so do the MCP servers agent clients
start from it. Quit Pharos and those clients before replacing the app, then
reconnect Pharos in each client. The source checkout's
`macos/install-library.sh` remains useful for development builds and adoption
from a per-user install.

## In-app update checks

The Pharos menu has **Check for Updates…** and an optional **Check for Updates
on Launch** setting. Checks contact GitHub's latest published release endpoint
for `Pythia-Software/pharos`, compare numeric version components against the running
bundle, and require the matching versioned DMG asset. The app shows release
notes and a **Download Disk Image** button, which opens that asset in the
default browser. The browser downloads the image; Finder and the user handle
installation. Checks on launch show a notice only when a newer release exists.
Nothing is downloaded in the background without a user action.

The app in a portable library is the `Pharos.app` beside `library.toml`, and
it runs from there; replace that copy when updating. An ad hoc signature
changes with each build, so macOS may ask for access to the library drive
again. Developer ID signing and notarization improve that experience, but are
optional in the current release script.

## Upgrading an existing library

A new version can derive more from the transcripts a library already holds.
When it opens a catalog indexed by an older version, Pharos offers a one-time
**Upgrade this library** panel, once per launch, and keeps an **Upgrade
library** button in the header until the upgrade is done. A library that is
new to this version has nothing to upgrade and never sees the panel.

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
4. **Rebuild the tool ledger** with error signatures, test failures, and
   repository-relative paths. This is the longest step.
5. **Rebuild the Tools rollup**, so the first visit to Tools doesn't wait.

Repositories merge first because the tool ledger records the repository each
file belongs to. Every step reads the catalog's retained messages; the
harness step also reads source files and captures where they still exist.
Transcripts are never changed.

The job commits in small units. Closing the panel doesn't stop it, and the
drive badge shows its progress. Ejecting stops it after the unit in
progress, and starting the upgrade again resumes where it stopped. Saved
table views that filter on a repository name the merge retired are pointed at
the surviving name.

From the command line, `pharos upgrade --status` reports what is pending,
`pharos upgrade --preview` lists the repository merges, and `pharos upgrade`
runs every step, printing progress and the time each step took. (`--github`
is still accepted and ignored; the lookup is no longer optional.)
