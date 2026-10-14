# Query Table source snapshot packages

The four active npm archives are built together from the Phoenix source snapshot
recorded in `query-table-provenance.json`. All four packages share one source
manifest and one retained source archive. Their package versions still say 0.6.0;
these are prerelease source snapshots, not registry 0.6.0.

`query-table-source-manifest.json` records the Phoenix baseline commit, whether
local edits were retained, and the SHA-256 of each source file. The provenance
file records each package/archive SHA-256. `package-lock.json` records npm
integrities and external dependency versions. The source archive contains the
exact inputs used for the build. Each package includes its README and MIT license.

Install with `npm ci` in `web`. The checked-in Pharos JS/CSS bundle is built from
these installed archives, and `npm run check-bundle` verifies both files. This
snapshot includes visible row/series limits, configurable Other remainders,
compact axes, underline lists, and label-line limits used by card covers.

To refresh all packages and the checked-in bundle together:

```sh
python3 tools/vendor-query-table.py /path/to/query-table
```

The script captures tracked and non-ignored source files into an isolated build,
runs its lockfile install and package builds, packs all four modules, records
hashes/provenance, updates Pharos's pins, and rebuilds Pharos from an isolated
install of those archives. It does not publish packages or edit the Query Table
checkout. The Go adapter has independent provenance in
`third_party/query-table-go/source-manifest.json`.

Local development uses `node tools/link-query-table.mjs`, replacing installed
packages with Phoenix symlinks without updating the archived pins. `npm ci`
restores the archives. Use the refresh script before shipping any linked-package
changes. After Query Table publishes the intended release, pin that release,
remove the source snapshots, and regenerate schemas and rebuild/test.
