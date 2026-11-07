# Native compact fingerprint renderer

The service's compact fingerprint renderer is entirely Go. It replaces the
Goja-backed layout introduced in 0.8.9 while keeping the existing rasterizer,
PNG encoder, bounded image cache, durable work queue and publication rules.

## Scope and parity

The native domain is exactly the background worker's two production profiles:
`wide` (1024×128) and `card` (1024×256), computed on a 320-unit layout, in both
themes and with agent events either shown or hidden. Selection retains the
500-event ceiling, four/six-arc profile budgets, tool dots/tails, label palette,
recorded parallelism, collision rules, and deterministic ties. It is not a
JavaScript engine or an arbitrary SVG renderer.

`fingerprint_prepare.go` projects the source evidence, merges overlapping event
spans, allocates label slices and positions tools. `fingerprint_layout.go` selects
and places events, arcs and tools. `fingerprint_svg.go` serializes the drawing.
The existing small SVG rasterizer and PNG/cache code stay in
`fingerprint_render.go`. SVG paths are serialized only for accepted final arcs,
rather than during each tentative placement.

Matching the old result requires more than equivalent formulas. The port retains
UTF-16 identity ordering/hashing, root-locale collation, timestamp millisecond
precision, operation order, JavaScript decimal rounding and stable sorting.
Explicit floating-point rounding boundaries prevent fused multiply-add from
changing the curve coordinates. No epsilon or image-difference threshold is used
for parity assertions.

The original 0.8.9 Goja renderer was frozen before editing and used to capture
reference SVG/PNG SHA-256 hashes. All **2,224 SVG strings and PNG byte streams**
matched exactly across **278 deterministic synthetic inputs** (eight appearances
per input). This includes dense 620-message/1,476-event/300-tool input, a
2,501-message history, merged events, missing targets/times, tied timestamps,
Unicode identities, fractional timestamps, tool-only histories, overlapping
sessions, every palette entry and 256 seeded variations. These are synthetic
inputs, not photographs or samples from a private library.

A representative 54-input corpus (432 appearances) is retained in
`testdata/fingerprint-rendering.json`. The existing Go renderer tests verify both
SVG and PNG hashes. The existing TypeScript compact-model tests independently
verify the same SVG hashes against the current frontend model and check that the
stored inputs match the generator in `web/tests/fixtures/compact-fingerprints.mjs`.
This catches drift on either side without requiring Node or Goja during Go tests.

Parity and performance were executed on Apple silicon. The Intel service was
cross-compiled successfully; this Mac has no Rosetta installation, so Intel
execution parity was not measured.

To check parity:

```sh
go test -vet=off -run '^TestFingerprintRendererParity$' ./internal/archive
npm --prefix web test -- compact-fingerprint
```

For an intentional reference change, regenerate the SVG hashes from TypeScript,
then generate PNG hashes only after the native SVG hashes agree. Review the
fixture diff; do not update it to suppress an unexplained mismatch.

```sh
npm --prefix web run generate-fingerprint-fixtures
PHAROS_UPDATE_FINGERPRINTS=1 go test -run '^TestFingerprintRendererParity$' ./internal/archive
```

## Runtime measurements

| Synthetic input | PNG cache | Goja ms/batch | Native Go ms/batch | Speedup |
| --- | --- | ---: | ---: | ---: |
| small | uncached | 14.156 | 11.705 | 1.21× |
| small | warm | 1.959 | 0.108 | 18.07× |
| mixed | uncached | 797.541 | 166.282 | 4.80× |
| mixed | warm | 694.077 | 13.111 | 52.94× |
| dense | uncached | 6,261.409 | 428.434 | 14.61× |
| dense | warm | 6,206.831 | 401.773 | 15.45× |

Input sizes: `small` has 12 messages and no events/tools; `mixed` has 100
messages, 100 events and 60 tools; `dense` has 620 messages, 1,476 events and
300 tools. All three are synthetic.

| Synthetic input, uncached | Goja allocated MB/batch | Native allocated MB/batch |
| --- | ---: | ---: |
| small | 1.89 | 0.25 |
| mixed | 325.73 | 13.11 |
| dense | 2,252.20 | 78.46 |

Allocated bytes are cumulative Go allocations per render, not peak RSS or
retained cache size. MB are decimal. In the uncached mixed/dense workloads,
allocation volume fell by roughly 96%.

The raw three-run ranges reflect load variation on this development Mac:

| Case | Goja ms range | Native ms range |
| --- | ---: | ---: |
| small/uncached | 13.867–15.002 | 11.682–15.771 |
| small/warm | 1.936–2.221 | 0.100–0.119 |
| mixed/uncached | 766.092–859.958 | 165.927–177.034 |
| mixed/warm | 635.899–1,176.874 | 12.748–14.187 |
| dense/uncached | 6,163.882–7,247.443 | 402.693–433.722 |
| dense/warm | 5,865.172–6,782.665 | 322.987–504.648 |

The benchmark times `fingerprintRenderer.Render` on already loaded inputs with
agent events enabled: one batch produces four PNGs, both profiles and both
themes. Renderer construction/initial compilation and one warm-up render are
outside the timed interval. It excludes catalog input reads, publication and
filesystem flushes. `uncached` sets the image cache limit to zero, so every PNG
is rasterized and encoded; `warm` reuses PNGs but still performs input projection,
selection, layout and SVG generation on every call.

Both versions used Go 1.27.3 on an Apple M3 Ultra, `GOMAXPROCS=1`, three
repetitions with `-benchtime=1s`. The Goja and native measurements ran sequentially
on the same synthetic inputs. Results are medians from this shared development
Mac; individual samples vary with other work. They are rendering costs, not a
whole-library backfill forecast. The earlier
[100-conversation backfill study](fingerprint-backfill-profile.md) measured a
different workload and includes storage/publication costs.

The current benchmark is reproducible with:

```sh
GOMAXPROCS=1 go test -vet=off -run '^$' -bench '^BenchmarkFingerprintRender$' \
  -benchtime=1s -count=3 ./internal/archive
```

For the before measurement, the original renderer and its locked frontend were
built from `289ad3aa1c1f40dfc0c215dcf77c61eff0241beb` in a disposable archive,
with the same benchmark body and input corpus copied into that checkout. No
private catalog, migrations, inference or live-library processing ran.

## Executable size

Release-style builds used Go 1.27.3, `CGO_ENABLED=1`, `-trimpath`, `-ldflags='-s -w'`
and the same local SDK/linker for both versions.

| Executable | Goja bytes | Native Go bytes | Reduction |
| --- | ---: | ---: | ---: |
| arm64 slice | 50,777,522 | 40,347,234 | 10,430,288 |
| x86_64 slice | 55,185,424 | 43,676,064 | 11,509,360 |
| Universal executable, including alignment | 105,991,602 | 84,043,362 | **21,948,240 (20.7%)** |

These are local unsigned/ad hoc build outputs, not republished release DMGs.
The shipped app's other executables and runtime libraries were not rebuilt.
Goja, its regexp engine, source-map dependency and external pprof dependency have
been removed from the module. The native release executable has no Goja or
regexp2 dependency. `x/text` collation remains to preserve the reference's ordering.
The retired JS bundle is removed by the frontend build before the assets are
embedded, so an older checkout's generated output cannot remain in the service.

Native source hashes version the image cache automatically. The first run of the
new version requeues existing images; it retains the last good PNGs while the
background worker recomputes them.
