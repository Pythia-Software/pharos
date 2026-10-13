# Query Table source snapshot packages

These four npm archives contain the built MIT-licensed packages from Query Table
commit `43b018279b44b2a91b8ccd8c553744a99093cff7`. The manifests still say 0.6.0;
the archives are an explicit prerelease source snapshot, not registry 0.6.0.
`query-table-provenance.json` records the commit and SHA-256 for each archive.

They make clean Pharos builds reproducible before Query Table's next publication.
Install them with `npm ci` in `web`; package-lock.json records their npm integrity
and external dependency versions. The upstream README and MIT license are
included in each package. The Go source snapshot is in `third_party/query-table-go`.

To develop against the Miami worktree, run `node tools/link-query-table.mjs`
from the Pharos root. It replaces installed packages with worktree-local symlinks
without editing these archives or the lockfile. `npm ci` restores the archives.
After Query Table publishes the intended release, pin that release in package.json
and the lockfile, remove these archives, then regenerate schemas and rebuild/test.
