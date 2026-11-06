package archive

// Risky actions look for high-signal operations, not every operation that
// could be risky. These helpers recognize what an agent routinely creates and
// throws away (scratch files, temporary and build directories, values a
// command assigned itself) so that only selections a person should look at
// raise a notice.

import (
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Directories whose contents are disposable by convention: temporary space,
// build output, dependency and tool caches, and agent scratch areas.
var safetyScratchDirs = map[string]bool{
	"tmp": true, ".tmp": true, "temp": true, ".build": true, "build": true, "dist": true, "out": true, "target": true,
	"node_modules": true, ".venv": true, "venv": true, "__pycache__": true, ".pytest_cache": true, ".mypy_cache": true,
	".ruff_cache": true, ".cache": true, ".context": true, "coverage": true, "DerivedData": true, ".next": true,
	".turbo": true, ".gradle": true, ".parcel-cache": true, ".task-worktrees": true, ".candidate-worktrees": true,
	".test-build": true, "test-results": true, "playwright-report": true,
}

// File names agents create to throw away: probes, scratch tests, temporary
// copies, logs, profiles and compiled test binaries.
var safetyScratchFile = regexp.MustCompile(`(?i)(^zz|^tmp[._-]|[._-]tmp([._-]|$)|\.tmp\b|\.bak$|\.orig$|\.log$|\.out$|\.prof$|\.pprof$|\.test$|\.pyc$|^\.ds_store$|probe|scratch|^t\.|\.err$)`)

var safetyScratchDir = regexp.MustCompile(`(?i)scratch|^te?mp[._-]|[._-]te?mp$`)

var safetyTempRoots = []string{"/tmp", "/private/tmp", "/var/folders", "/private/var/folders", "/var/tmp"}

// safetyScratch reports whether a resolved path is disposable by convention.
func safetyScratch(value string) bool {
	clean := path.Clean(value)
	for _, root := range safetyTempRoots {
		if clean == root || strings.HasPrefix(clean, root+"/") {
			return true
		}
	}
	for _, part := range strings.Split(clean, "/") {
		if safetyScratchDirs[part] || safetyScratchDir.MatchString(part) {
			return true
		}
	}
	return safetyScratchFile.MatchString(path.Base(clean))
}

var safetyAssignment = regexp.MustCompile(`(?:^|[\s;&|(])(?:export\s+|local\s+|readonly\s+|declare\s+(?:-\w+\s+)?)?([A-Za-z_][A-Za-z0-9_]*)=("(?:[^"\\]|\\.)*"|'[^']*'|\$\((?:[^()]|\([^()]*\))*\)|[^\s;&|()]+)`)
var safetyVariable = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// Well-known variables naming temporary space.
var safetyTempVariables = map[string]bool{"TMPDIR": true, "TMP": true, "TEMP": true, "TEMPDIR": true, "RUNNER_TEMP": true}

const safetyTempMarker = "/tmp/pharos-safety-temporary"

// A value that is exactly the output of mktemp, quoted or not.
var safetyMktemp = regexp.MustCompile(`^"?\$\(\s*mktemp(?:\s[^()]*)?\)"?$`)

// safetyAssignments reads, for each segment, the variables the command
// assigned before it: in earlier segments of the same shell or an enclosing
// one, never in a subshell that has exited, nor later in the command. A value
// from mktemp is temporary space; any other value is used only when it is
// literal after expanding earlier assignments, $PWD and $HOME. An assignment
// that cannot be read forgets the variable's earlier value.
func safetyAssignments(parsed shellCommand, cwd string) []map[string]string {
	visible := make([]map[string]string, len(parsed.Segments))
	for index, segment := range parsed.Segments {
		values := map[string]string{}
		for _, earlier := range parsed.Segments[:index] {
			if !strings.HasPrefix(segment.Scope, earlier.Scope) {
				continue
			}
			for _, match := range safetyAssignment.FindAllStringSubmatch(earlier.Text, -1) {
				name, value := match[1], match[2]
				delete(values, name)
				if safetyMktemp.MatchString(value) {
					values[name] = safetyTempMarker
					continue
				}
				if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') {
					value = value[1 : len(value)-1]
				}
				if expanded, ok := safetyExpand(value, values, cwd); ok {
					values[name] = expanded
				}
			}
		}
		visible[index] = values
	}
	return visible
}

// safetyExpand substitutes known variables. It reports false when any
// variable, substitution or pattern remains unknown.
func safetyExpand(value string, values map[string]string, cwd string) (string, bool) {
	known := true
	expanded := safetyVariable.ReplaceAllStringFunc(value, func(token string) string {
		name := strings.Trim(token, "${}")
		switch {
		case values[name] != "":
			return values[name]
		case safetyTempVariables[name]:
			return safetyTempMarker
		case name == "PWD" && path.IsAbs(cwd):
			return cwd
		}
		known = false
		return token
	})
	if strings.ContainsAny(expanded, "`") || strings.Contains(expanded, "$(") {
		known = false
	}
	return expanded, known
}

// safetyEmptyExpansion reports an unresolved target that selects the
// filesystem root or a home directory when its variable is empty or unset:
// rm -rf "$DIR/" or "$DIR/*". Other unresolved targets are low signal.
var safetyEmptyExpansion = regexp.MustCompile(`^(?:\$\{?[A-Za-z_][A-Za-z0-9_]*\}?)+/\*?$|^~/?\*?$|^/\*?$|^\$\{?HOME\}?/?\*?$`)

// Broadening permission modes: world-writable, setuid or setgid, or every
// permission for everyone. Adding execute, or restricting access, is not.
func safetyBroadensMode(mode string) bool {
	if n, err := strconv.ParseUint(mode, 8, 32); err == nil && len(mode) >= 3 {
		return n&0o002 != 0 || n&0o6000 != 0
	}
	for _, clause := range strings.Split(mode, ",") {
		at := strings.IndexAny(clause, "+-=")
		if at < 0 || clause[at] == '-' {
			continue
		}
		targets, permissions := clause[:at], clause[at+1:]
		// Without a who, the umask normally keeps others from writing.
		if strings.Contains(permissions, "s") || strings.Contains(permissions, "w") && strings.ContainsAny(targets, "oa") {
			return true
		}
	}
	return false
}

// Search and lookup endpoints use POST for their query; they change nothing.
var safetyReadEndpoint = regexp.MustCompile(`(?i)/(?:search|query|lookup|fetch)(?:[/?#]|$)|graphql\?query=`)

// Loopback and link-local services are local development servers.
func safetyLocalURL(raw string) bool {
	host := raw
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	host, _, _ = strings.Cut(host, "/")
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	if strings.HasPrefix(host, "[") {
		host, _, _ = strings.Cut(strings.TrimPrefix(host, "["), "]")
	} else {
		host, _, _ = strings.Cut(host, ":")
	}
	host = strings.ToLower(host)
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasPrefix(host, "127.") || host == "::1" || host == "0.0.0.0" || strings.HasSuffix(host, ".local")
}

// Branches whose history others build on.
var safetySharedBranch = regexp.MustCompile(`^(?:refs/heads/)?(?:main|master|trunk|develop|development|production|prod|staging|release(?:[/-].*)?|gh-pages)$`)

// A process selector is broad when it names a program rather than one task.
// A short word matches every instance of a program (vite, node, Chrome); a
// path, script, port, test name or generated ID names the task's own process.
func safetyBroadSelector(pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	return pattern == "" || pattern == ".*" || pattern == "." || safetyProgramName.MatchString(pattern) && !safetyTestName.MatchString(pattern)
}

var safetyProgramName = regexp.MustCompile(`^[A-Za-z][A-Za-z_-]{0,15}$`)
var safetyTestName = regexp.MustCompile(`^Test[A-Z]`)

var safetyConnInfoHost = regexp.MustCompile(`(?:^|\s)host=([^\s]+)`)

// safetyDatabaseHost names the server a psql or mysql command connects to when
// it is on another machine. A local socket, loopback or default host is
// development data and returns "".
func safetyDatabaseHost(args []string) string {
	host := ""
	for i, arg := range args {
		switch {
		case arg == "-h" || arg == "--host":
			if i+1 < len(args) {
				host = args[i+1]
			}
		case strings.HasPrefix(arg, "--host="):
			host = strings.TrimPrefix(arg, "--host=")
		case strings.HasPrefix(arg, "-h") && len(arg) > 2 && !strings.HasPrefix(arg, "-help"):
			host = arg[2:]
		case strings.Contains(arg, "://"):
			host = arg
		case safetyConnInfoHost.MatchString(arg):
			host = safetyConnInfoHost.FindStringSubmatch(arg)[1]
		}
	}
	// A host held in a variable is unknown, usually a test server's socket.
	if host == "" || strings.HasPrefix(host, "/") || strings.Contains(host, "$") || safetyLocalURL(host) {
		return ""
	}
	if strings.Contains(host, "://") {
		// Name the server only; credentials and query stay out of the subject.
		rest := host[strings.Index(host, "://")+3:]
		rest, _, _ = strings.Cut(rest, "/")
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		if rest == "" {
			return ""
		}
		host = rest
	}
	return host
}

// System tools' read-only verbs, including diskutil's apfs listings.
var safetySystemReads = []string{"list", "info", "status", "show", "print", "activity", "verifyVolume", "verifyDisk", "listFilesystems", "cat", "is-active", "is-enabled", "is-failed", "blame", "print-disabled", "dumpstate", "list-units", "list-unit-files", "list-timers", "help"}

func safetySystemRead(ops []string) bool {
	if slices.Contains(safetySystemReads, ops[0]) {
		return true
	}
	return ops[0] == "apfs" && len(ops) > 1 && (strings.HasPrefix(ops[1], "list") || ops[1] == "info")
}

// safetyStashDiscard reports a stash discard that can reach another task's
// entry: clearing the stash, or dropping the top or a numbered position. An
// entry found by its own message (a variable or substitution) is the
// careful form.
func safetyStashDiscard(ops []string) bool {
	if slices.Contains(ops, "clear") {
		return true
	}
	at := slices.Index(ops, "drop")
	if at < 0 {
		return false
	}
	return at == len(ops)-1 || !strings.ContainsAny(ops[at+1], "$`")
}
