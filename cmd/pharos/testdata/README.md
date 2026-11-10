# Isolated named-timezone check

After `tools/setup.sh` (Node 24), run from the repository root:

```sh
go test ./cmd/pharos -run '^TestNamedTimezones$' -v
cmd/pharos/testdata/run-timezones.sh
```

The first command checks ordinary named-timezone behavior. It cannot prove the
embedded fallback is used: macOS can load system or GOROOT data even when
`ZONEINFO` points to a nonexistent directory.

The runner cross-builds this same executable-package test binary with
`CGO_ENABLED=0 GOOS=linux`, including the `time/tzdata` import from production
`main.go`. It creates a fresh `FROM scratch` image containing only that binary
and runs only `TestNamedTimezones`, without network or host filesystem mounts.
The image has no Go installation or system timezone database. Temporary build
files and the test image are removed on exit.

Docker must have a running Linux daemon that can execute the target architecture.
The default target is `linux/amd64`; use
`GOARCH=arm64 cmd/pharos/testdata/run-timezones.sh` for an ARM64 runner. No
prepared image or network download is needed. If the daemon is unavailable, the script reports
isolated verification as pending and exits unsuccessfully after cross-building;
compilation alone does not prove the fallback works at runtime.
