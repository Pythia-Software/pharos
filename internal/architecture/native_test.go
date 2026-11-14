package architecture

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

// Approval is by exact file and purpose, never by a filename suffix or an
// entire directory. New native implementations need a reviewed entry here.
var nativeLeaves = map[string]string{
	"internal/platform/filelock/filelock_unix.go":             "Borrowed-descriptor advisory locking.",
	"internal/platform/filesync/filesync_unix.go":             "Plain buffered-file flush; not stronger publication durability.",
	"internal/platform/fsinfo/fsinfo_unix.go":                 "Filesystem capacity observations.",
	"internal/platform/fsinfo/fsinfo_darwin.go":               "Darwin block size and read-only mount flag.",
	"internal/platform/fsinfo/fsinfo_linux.go":                "Linux block size and read-only mount flag.",
	"internal/platform/fsinfo/fsinfo_windows.go":              "Caller-available Windows capacity, not read-only proof.",
	"internal/platform/localidentity/localidentity_unix.go":   "Identity of the supplied stat observation.",
	"internal/platform/localidentity/localidentity_darwin.go": "Legacy signed Darwin device encoding.",
	"internal/platform/localidentity/localidentity_linux.go":  "Full-width Linux device encoding.",
	"internal/archive/process_isolation_unix.go":              "Distinct Ask group and login-shell session cancellation.",
	"internal/archive/labeling_process_unix.go":               "Labeler group signaling and persisted leader identity.",
	"internal/archive/process_liveness_unix.go":               "Positive-PID presence/absence/unknown observation.",
	"internal/archive/process_replace_unix.go":                "Replace MCP process while preserving stdio and pending input.",
	"internal/archive/process_start_darwin.go":                "Kernel process-start identity.",
	"internal/archive/resources_unix.go":                      "Unix accounting and resident-memory fallback.",
	"internal/archive/resources_linux.go":                     "Linux self-accounting; explicitly approximate runtime memory.",
	"internal/archive/resources_darwin.go":                    "Darwin kernel accounting and process table.",
	"internal/archive/resources_gpu_darwin.go":                "Darwin process GPU observation.",
	"internal/archive/work_load_darwin.go":                    "Optional Darwin machine-load context.",
	"internal/archive/work_load_linux.go":                     "Optional Linux procfs load context.",
	"internal/archive/file_identity_unix.go":                  "Backup/link observations and legacy persisted history-root keys.",
	"internal/archive/file_observation_unix.go":               "V1 library inode and placement-device observations.",
	"internal/archive/service_volume_unix.go":                 "Service volume watchdog identity.",
	"internal/archive/input_open_unix.go":                     "Nonblocking path and rooted input opens.",
	"internal/archive/finder_info_unix.go":                    "Finder metadata observation; no native launch authorization.",
	"internal/archive/browser_open_darwin.go":                 "Opens a validated Claude https link in the default browser or a listed Chromium profile, and lists those profiles, for a person-enabled export download.",
	"internal/archive/speech_darwin.go":                       "Has the system voice read a stored message's text file into an audio file for the conversation player; no output is reported.",
	"internal/archive/drive_darwin.go":                        "Darwin mount observation.",
	"internal/archive/rename_exclusive_darwin.go":             "Atomic no-replace publication.",
	"internal/archive/capture_export_io_unix.go":              "Confined descriptor-relative capture export I/O.",
	"internal/archive/capture_export_io_darwin.go":            "Darwin atomic no-replace export rename.",
	"internal/archive/capture_export_io_linux.go":             "Linux atomic no-replace export rename.",
	"internal/archive/capture_iopolicy_darwin.go":             "Darwin background I/O policy.",
	"internal/archive/wal_pin_darwin.go":                      "Darwin SQLite WAL ownership diagnostics.",
	"cmd/pharos-labeler-ort/stdout_unix.go":                   "Preserve worker protocol stdout across native runtime loading.",
	"cmd/pharos-labeler-ort/ort.go":                           "Existing ONNX C ABI leaf, explicitly cgo-gated.",
	"cmd/pharos-labeler-ort/ort_dlopen.go":                    "Existing Unix ONNX dynamic loading.",
	"cmd/pharos-labeler-ort/mlx_darwin.go":                    "Existing Darwin MLX C ABI leaf.",
}

// Legacy shared plumbing is grandfathered per enclosing function and operation,
// not per file. This is a ratchet, not permission to add another native call.
// Keep reasons with the exception and remove entries when a seam is extracted.
var sharedNative = map[string]string{
	"internal/archive/util.go|volumeIdentity|command:diskutil":                                                                                  "Legacy volume UUID discovery; native device fallback is already mediated.",
	"internal/archive/adapter_antigravity_version.go|installedAntigravityVersion|native-path:/Applications/Antigravity.app/Contents/Info.plist": "Existing Mac provider discovery, not a cross-host source identity.",
	"internal/archive/adapter_antigravity_version.go|installedAntigravityVersion|command:plutil":                                                "Existing Mac provider version discovery.",
	"internal/archive/application.go|applicationFolderPicker|command:osascript":                                                                 "Existing Mac folder-picker implementation; shared UI uses host services.",
	"internal/archive/application.go|api|native-path:/Volumes/":                                                                                 "Existing disconnected Mac-volume check.",
	"internal/archive/application.go|runApplicationMCPCLI|native-path:/Volumes/":                                                                "Existing disconnected Mac-volume check.",
	"internal/archive/authorship_sources.go|authorshipFileDestination|native-path:/proc/self/fd/":                                               "Reject recorded process-descriptor destinations; no procfs I/O.",
	"internal/archive/cli.go|Run|command:open":                                                                                                  "Existing Mac launch command.",
	"internal/archive/devui.go|runDevUICLI|command:open":                                                                                        "Developer-only browser launch.",
	"internal/archive/drive.go|inspectVolume|command:diskutil":                                                                                  "Existing Mac volume inspection.",
	"internal/archive/drive.go|diskImage|command:hdiutil":                                                                                       "Existing Mac disk-image inspection.",
	"internal/archive/drive.go|spotlightStatus|command:mdutil":                                                                                  "Existing Mac Spotlight inspection.",
	"internal/archive/drive.go|lookupVolumeIdentity|command:diskutil":                                                                           "Existing bounded Mac volume UUID lookup.",
	"internal/archive/drive.go|backupCheck|native-path:/Volumes/BACKUP-DRIVE/Pharos Backup":                                                     "Display-only example destination in backup advice.",
	"internal/archive/hosts.go|detectHost|command:scutil":                                                                                       "Existing Mac host display-name discovery.",
	"internal/archive/hosts.go|platformUUID|command:ioreg":                                                                                      "Existing Mac host identity discovery.",
	"internal/archive/library_registry.go|resolve|native-path:/Volumes":                                                                         "Existing Mac remounted-volume resolution.",
	"internal/archive/library_status.go|libraryDriveOf|native-path:/Volumes/":                                                                   "Existing Mac library drive label.",
	"internal/archive/markdown_links.go|markdownOpenFile|command:open":                                                                          "Existing runtime-gated Mac file opener; no new launch capability.",
	"internal/archive/mcp_library.go|libraryNotConnected|native-path:/Volumes/":                                                                 "Existing disconnected Mac-volume advice.",
	"internal/archive/operation_broker.go|libraryDriveName|native-path:/Volumes/":                                                               "Existing Mac drive label.",
	"internal/archive/power.go|readPower|command:pmset":                                                                                         "Existing optional Mac power observation.",
	"internal/archive/probe.go|probeApplicationDirs|native-path:/Applications":                                                                  "Existing Mac application discovery.",
	"internal/archive/probe.go|protectedLocation|native-path:/Volumes/":                                                                         "Existing Mac protected-location safety check.",
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func checkBoundaries(root fs.FS, leaves, exceptions map[string]string) ([]string, error) {
	names, err := goSources(root)
	if err != nil {
		return nil, err
	}
	var failures []string
	seenFiles, used := map[string]bool{}, map[string]bool{}
	for _, name := range names {
		seenFiles[name] = true
		data, err := fs.ReadFile(root, name)
		if err != nil {
			return nil, err
		}
		found, gated, err := inspectGo(name, data)
		if err != nil {
			return nil, err
		}
		if reason, approved := leaves[name]; approved {
			if strings.TrimSpace(reason) == "" || !gated {
				failures = append(failures, name+": native approval needs a reason and an OS/cgo build constraint")
			}
			for _, f := range found {
				if strings.HasPrefix(f.operation, "dot-import:") {
					failures = append(failures, f.String())
				}
			}
			continue
		}
		for _, f := range found {
			key := name + "|" + f.owner + "|" + f.operation
			if reason := strings.TrimSpace(exceptions[key]); reason != "" {
				used[key] = true
			} else {
				failures = append(failures, f.String())
			}
		}
	}
	for name := range leaves {
		if !seenFiles[name] {
			failures = append(failures, "remove stale native approval: "+name)
		}
	}
	for key := range exceptions {
		if !used[key] {
			failures = append(failures, "remove stale shared exception: "+key)
		}
	}
	return failures, nil
}

func TestNativeBoundaries(t *testing.T) {
	failures, err := checkBoundaries(os.DirFS(repoRoot(t)), nativeLeaves, sharedNative)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range failures {
		t.Error(failure)
	}
}

func TestNativeBoundaryPatterns(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		invalid    bool
	}{
		{"import alias and function value", `import u "syscall"; var kill = u.Kill`, true},
		{"native type", `import s "syscall"; var info s.Stat_t`, true},
		{"native dot import", `import . "syscall"; var kill = Kill`, true},
		{"process attributes", `func f(cmd *Command) { cmd.SysProcAttr = nil }`, true},
		{"windows native import", `import w "golang.org/x/sys/windows"; var x = w.GetDiskFreeSpaceEx`, true},
		{"command constructor alias", `import e "os/exec"; var run = e.Command; func f(){ const tool="disk"+"util"; run(tool, "info") }`, true},
		{"short command alias", `import e "os/exec"; func f(){ run := e.Command; tool := "diskutil"; run(tool) }`, true},
		{"context command", `import e "os/exec"; func f(){e.CommandContext(ctx,"/usr/bin/osascript")}`, true},
		{"existing wrapper", `func f(){driveTool(ctx,"/usr/sbin/diskutil")}`, true},
		{"proc path", `import "os"; func f(){os.ReadFile("/pro"+"c/loadavg")}`, true},
		{"ordinary file and command", `import("os"; "os/exec"); func f(){os.Stat(name); os.ReadFile(name); exec.Command("git","status")}`, false},
		{"portable errno and signal", `import "syscall"; var a=syscall.EINTR; var b=syscall.SIGTERM`, false},
		{"shadowed package", `import "syscall"; var code=syscall.EINTR; func f(){syscall := value; syscall.Kill()}`, false},
		{"comments and ordinary text", `// syscall.Kill is native\n` + "\n" + `var text = "syscall.Kill"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found, _, err := inspectGo("internal/example/new_darwin.go", []byte("package example\n"+tc.code))
			if err != nil {
				t.Fatal(err)
			}
			if (len(found) != 0) != tc.invalid {
				t.Fatalf("findings=%v; want invalid=%v", found, tc.invalid)
			}
		})
	}
}

func TestNativeApprovalsAreExplicit(t *testing.T) {
	code := []byte("//go:build darwin\n\npackage example\nimport s \"syscall\"\nvar kill=s.Kill\n")
	name := "internal/example/new_darwin.go"
	files := fstest.MapFS{name: {Data: code}, "web/node_modules/vendor.go": {Data: []byte("not Go")}}
	failures, err := checkBoundaries(files, nil, nil)
	if err != nil || len(failures) == 0 {
		t.Fatalf("a platform filename must not exempt new native code: %v, %v", failures, err)
	}
	failures, err = checkBoundaries(files, map[string]string{name: "Deliberate native mechanism."}, nil)
	if err != nil || len(failures) != 0 {
		t.Fatalf("reviewed constrained leaf: %v, %v", failures, err)
	}
	delete(files, name)
	name = "internal/example/shared.go"
	files[name] = &fstest.MapFile{Data: []byte("package example\nimport s \"syscall\"\nvar kill=s.Kill\n")}
	failures, err = checkBoundaries(files, map[string]string{name: "Missing platform constraint."}, nil)
	if err != nil || len(failures) == 0 {
		t.Fatalf("native approval without constraint: %v, %v", failures, err)
	}
	for _, tc := range []struct {
		tag   string
		gated bool
	}{
		{"arm64", false},
		{"darwin || amd64", false},
		{"darwin || 386", false},
		{"darwin || integration", false},
		{"cgo || integration", false},
		{"darwin || (linux && integration) || custom", false},
		{"!windows", false},
		{"!(integration && !darwin)", false},
		{"darwin", true},
		{"darwin && arm64", true},
		{"(darwin || linux) && !native_unsupported", true},
		{"cgo && unix", true},
		{"!(!darwin && !linux)", true},
		{"!(!darwin || integration)", true},
		{"(darwin && !cgo) || (linux && integration)", true},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			files[name] = &fstest.MapFile{Data: []byte("//go:build " + tc.tag + "\n\npackage example\nimport s \"syscall\"\nvar kill=s.Kill\n")}
			failures, err = checkBoundaries(files, map[string]string{name: "Reviewed native mechanism."}, nil)
			if err != nil || (len(failures) == 0) != tc.gated {
				t.Fatalf("native approval with constraint %q: %v, %v; want gated=%v", tc.tag, failures, err, tc.gated)
			}
		})
	}
}

func TestSharedNativeExceptionsStayScoped(t *testing.T) {
	name := "internal/example/shared.go"
	files := fstest.MapFS{name: {Data: []byte("package example\nimport \"os/exec\"\nfunc legacy(){exec.Command(\"diskutil\")}\n")}}
	exceptions := map[string]string{name + "|legacy|command:diskutil": "Existing Mac volume query."}
	if failures, err := checkBoundaries(files, nil, exceptions); err != nil || len(failures) != 0 {
		t.Fatalf("reviewed legacy operation: %v, %v", failures, err)
	}
	files[name].Data = append(files[name].Data, []byte("func newFeature(){exec.Command(\"diskutil\")}\n")...)
	if failures, err := checkBoundaries(files, nil, exceptions); err != nil || len(failures) == 0 {
		t.Fatalf("another function must not inherit a legacy exception: %v, %v", failures, err)
	}
	files[name].Data = []byte("package example\n")
	if failures, err := checkBoundaries(files, nil, exceptions); err != nil || len(failures) == 0 {
		t.Fatalf("a removed operation must retire its exception: %v, %v", failures, err)
	}
}
