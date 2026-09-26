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
asset of a published version. The disk image's Finder layout is prepared on the release
Mac, so release from a logged-in desktop session; Terminal may ask for
permission to control Finder.

The disk image opens in Finder with an illustrated guide. To update, quit
Pharos, drag `Pharos.app` from the image to the folder where the original app
was installed (beside `library.toml` on a portable drive, or in Applications),
choose **Replace**, then open the new app and approve macOS access prompts.
Only replace the app; leave the library's other files in place. If macOS blocks
an ad hoc signed build, use **System Settings → Privacy & Security → Open
Anyway** after trying to open it.

To create a new portable library from the disk image, drag `Pharos.app` into
the intended library folder and initialize that folder with its embedded CLI:

```sh
"/Volumes/YOUR-DRIVE/Pharos/Pharos.app/Contents/MacOS/pharos" init-library "/Volumes/YOUR-DRIVE/Pharos"
open "/Volumes/YOUR-DRIVE/Pharos/Pharos.app"
```

If an MCP process is running directly from the old bundle, quit its client
before replacing the app. MCP processes using a local runtime copy continue
using that build until their clients restart them. The source checkout's
`macos/install-library.sh` remains useful for development builds and adoption
from a per-user install.

## In-app update checks

The Pharos menu has **Check for Updates…** and an optional **Check for Updates
on Launch** setting. Checks contact GitHub's latest published release endpoint
for `gbdubs/pharos`, compare numeric version components against the running
bundle, and require the matching versioned DMG asset. The app shows release
notes and a **Download Disk Image** button, which opens that asset in the
default browser. The browser downloads the image; Finder and the user handle
installation. Checks on launch show a notice only when a newer release exists.
Nothing is downloaded in the background without a user action.

The durable app in a portable library is the `Pharos.app` beside
`library.toml`. Opening it launches a separate local runtime copy so that the
library's drive can be ejected. Replace the durable copy on the drive when
updating; replacing the running local copy would not last. An ad hoc signature
changes with each build, so macOS may ask for access to the library drive
again. Developer ID signing and notarization improve that experience, but are
optional in the current release script.
