# DMG format libraries

Pharos packages DMGs with `../build_dmg.py`. It uses Apple's disk-image tools
and these checked-in Python sources, without pip, uvx, network access or a
site-packages dependency. Python 3.10 or newer is required.

| Source | Version | License | Purpose |
| --- | --- | --- | --- |
| [ds_store](https://github.com/dmgbuild/ds_store) | 1.3.3 | [MIT](ds_store/LICENSE) | Finder `.DS_Store` database |
| [mac_alias](https://github.com/dmgbuild/mac_alias) | 2.2.3 | [MIT](mac_alias/LICENSE) | Background Alias and filesystem identity |
| [dmgbuild](https://github.com/dmgbuild/dmgbuild/tree/v1.6.7) | 1.6.7 | [MIT](dmgbuild-LICENSE) | Adapted Finder settings and image-size calculation |

The `ds_store` and `mac_alias` runtime Python files, including their CLIs, are
unmodified copies of their published source distributions. Their format and
filesystem support stay intact to avoid introducing a bespoke binary writer.
There is no vendored or installed `dmgbuild` package; Pharos owns the packaging
driver and uses only the icon view, TIFF background and HFS+/ULMO pathway.
The MIT terms here apply to the imported code independently of the repository's
root license. Retain all notices when copying or updating it.

`sources.json` records the exact source archive URLs, SHA-256 digests and
individual imported-file digests. To update, download and verify an explicitly
selected archive, review the changes (particularly native structures and Alias
encoding), replace whole runtime files and licenses, and refresh the manifest.
Do not resolve or update packages as part of a release. Check upstream fixes
when reviewing a macOS compatibility issue or changing these libraries.

Run `python3 -m unittest discover -s tools -p test_release_dmg.py` for portable
format, provenance and failure-path checks. On macOS, run the same command with
`PHAROS_DMG_TESTS=1` for a real image using a disposable ad hoc signed app. The
test exercises the ordinary shell entry point and Retina artwork, remounts at
a different path, verifies signatures and resolves the original background
Alias with CoreFoundation. It never accesses release keys or publishes.
Before shipping changes, check actual Finder appearance on macOS 14 and the
current supported release; metadata tests cannot prove Finder's rendering.
