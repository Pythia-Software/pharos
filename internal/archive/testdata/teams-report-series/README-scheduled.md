The scheduled wire vectors are copied byte-for-byte to the native repository's `internal/archive/testdata/teams-report-series/`. Assignment cases change one sibling field at a time while holding the persisted authority fixed; config hashing alone does not authenticate those fields. Calendar vectors cover daylight-saving gaps and folds, weekly cadence and monthly clamping. Timing vectors include overlapping periods, final-deadline clamping and the finite schedule limit. Rejections exercise query, schema, ACL and strict JSON boundaries.

SHA-256:

- config: `da44ddded062fd84c3cef80a322802a8ba451a21b3d15bb94e4f649d0213edc5`
- assignment: `903fec474c9bd1d704ac31628229df97a4c3418006b84e43f74e1b2c5f73dd7a`
- calendar: `f937b814e159721e49135f06fa1b905c7a04ec7aaab7680d30d1fe2a4a9562d0`
- timing: `52a6852318ef37678601885a4aad74fd087feb718cd945eba3e8bcb9a141a26f`
- rejection: `ed061cb46678328ae94a7eb30fa089f27fbb5ea54fedd855557328f408d381e1`
- equivalent link encodings: `f10eee4869b69d5dac4f4122efb1d5de3a8c653f9eaecdf68b5512458bd2879e`
- schedule and zone validation: `fb362531c6b2151758d077c681643679be23f8c2c781be82bd2e10a39b39f2c3`
- IANA zone-name inventory: `8725722643bf1f4ff4fc4b22268ade98b6fae047a86897219c3a13d4c4ced93d`

The inventory is the complete list, including legitimate aliases, from Go 1.27.1's `lib/time/zoneinfo.zip`, generated from IANA tzdata 2026c (see Go's `lib/time/update.bash`, `CODE=2026c` and `DATA=2026c`). IANA states its time zone database is public domain; see https://data.iana.org/time-zones/tz-link.html. To update deliberately, use that exact Go release and run `unzip -Z1 "$(go env GOROOT)/lib/time/zoneinfo.zip" | LC_ALL=C sort`, review the name and alias diff, copy identical bytes to `internal/archive/iana_zone_names.txt` in the native repository, update both pinned hashes and run both vector suites. This inventory fixes accepted spellings only; it does not pin transition data. A versioned shared transition-data policy remains required before activation.
