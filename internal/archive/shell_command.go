package archive

import (
	"path"
	"strconv"
	"strings"
)

// shellSegment is one simple command of a shell command line. Operator is the
// control operator that joined it to the previous segment ("" for the first).
type shellSegment struct {
	Text, Operator                string
	Program, Subcommand, Category string
	Assigned                      bool
	// Scope identifies lexical subshell ancestry; an empty scope is the
	// invoking shell. Consumers must not leak subshell directory changes.
	Scope string
}

// shellCommand summarizes a command line for tool analytics. It is a lexical
// reading, not an evaluation: variables, aliases, and functions are not
// expanded, and a command substitution is part of its enclosing segment.
type shellCommand struct {
	Segments                                         []shellSegment
	Primary                                          int
	HasPipe, HasRedirect, HasHeredoc, IsBackgrounded bool
}

func (c shellCommand) primary() shellSegment {
	if c.Primary < 0 || c.Primary >= len(c.Segments) {
		return shellSegment{}
	}
	return c.Segments[c.Primary]
}

// parseShellCommand splits a command line on unquoted control operators
// (&&, ||, ;, |, &, newline), skips heredoc bodies, and classifies each
// segment by program. The primary segment is the first one that does real
// work rather than set up the shell (cd, export, echo separators, ...).
func parseShellCommand(line string) shellCommand {
	result := shellCommand{Primary: -1}
	var current strings.Builder
	operator := ""
	flush := func(next string) {
		text := strings.TrimSpace(current.String())
		current.Reset()
		if text != "" {
			if strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")") && matchSubstitution(text, 0) == len(text) {
				inner := parseShellCommand(text[1 : len(text)-1])
				scope := strconv.Itoa(len(result.Segments)) + "/"
				for index, part := range inner.Segments {
					part.Scope = scope + part.Scope
					if index == 0 {
						part.Operator = operator
					}
					result.Segments = append(result.Segments, part)
				}
				result.HasPipe = result.HasPipe || inner.HasPipe
				result.HasRedirect = result.HasRedirect || inner.HasRedirect
				result.HasHeredoc = result.HasHeredoc || inner.HasHeredoc
				result.IsBackgrounded = result.IsBackgrounded || inner.IsBackgrounded
				operator = next
				return
			}
			segment := shellSegment{Text: text, Operator: operator}
			segment.Program, segment.Subcommand = shellProgram(text)
			segment.Assigned = assignmentOnly(text)
			segment.Category = shellCategory(segment.Program, segment.Subcommand, text)
			result.Segments = append(result.Segments, segment)
		}
		operator = next
	}
	var quote byte
	depth := 0
	heredocs := []string{}
	for index := 0; index < len(line); index++ {
		char := line[index]
		if quote == '"' && strings.HasPrefix(line[index:], "<<") && !strings.HasPrefix(line[index:], "<<<") {
			// A heredoc inside "$(...)": its body may contain quotes of its own.
			result.HasHeredoc = true
			if delimiter := heredocDelimiter(line[index+2:]); delimiter != "" {
				heredocs = append(heredocs, delimiter)
			}
			current.WriteString("<<")
			index++
			continue
		}
		if quote != 0 && char == '\n' && len(heredocs) > 0 {
			skipHeredocs(line, &index, heredocs)
			heredocs = heredocs[:0]
			continue
		}
		if quote != 0 {
			current.WriteByte(char)
			if char == '\\' && quote == '"' && index+1 < len(line) {
				index++
				current.WriteByte(line[index])
			} else if char == quote {
				quote = 0
			}
			continue
		}
		if char == '\\' && index+1 < len(line) {
			current.WriteByte(char)
			index++
			current.WriteByte(line[index])
			continue
		}
		if char == '\'' || char == '"' || char == '`' {
			quote = char
			current.WriteByte(char)
			continue
		}
		if strings.HasPrefix(line[index:], "<<") && !strings.HasPrefix(line[index:], "<<<") {
			result.HasHeredoc = true
			if delimiter := heredocDelimiter(line[index+2:]); delimiter != "" {
				heredocs = append(heredocs, delimiter)
			}
			current.WriteString("<<")
			index++
			continue
		}
		if char == '\n' && len(heredocs) > 0 {
			// Heredoc bodies are data, not commands: skip them, even inside $(...).
			skipHeredocs(line, &index, heredocs)
			heredocs = heredocs[:0]
			if depth == 0 {
				flush(";")
			}
			continue
		}
		if char == '(' {
			depth++
		} else if char == ')' && depth > 0 {
			depth--
			current.WriteByte(char)
			continue
		}
		if depth > 0 {
			current.WriteByte(char)
			continue
		}
		if char == '#' && (index == 0 || line[index-1] == ' ' || line[index-1] == '\t' || line[index-1] == '\n') {
			// A comment runs to the end of the line.
			for index+1 < len(line) && line[index+1] != '\n' {
				index++
			}
			continue
		}
		if char == '>' && !(index > 0 && line[index-1] == '&') && !strings.HasPrefix(line[index:], ">&") {
			result.HasRedirect = true
		}
		switch {
		case char == '\n':
			flush(";")
			continue
		case strings.HasPrefix(line[index:], "&&"):
			flush("&&")
			index++
			continue
		case strings.HasPrefix(line[index:], "||"):
			flush("||")
			index++
			continue
		case char == '|':
			result.HasPipe = true
			flush("|")
			continue
		case char == ';':
			flush(";")
			continue
		case char == '&' && !(index > 0 && (line[index-1] == '>' || line[index-1] == '<')) && !strings.HasPrefix(line[index:], "&>"):
			result.IsBackgrounded = true
			flush("&")
			continue
		}
		current.WriteByte(char)
	}
	flush("")
	for index, segment := range result.Segments {
		if segment.Program != "" && !segment.Assigned && !shellSetupPrograms[segment.Program] {
			result.Primary = index
			break
		}
	}
	if result.Primary < 0 && len(result.Segments) > 0 {
		result.Primary = 0
	}
	return result
}

// skipHeredocs advances index (at a newline) past each pending heredoc body,
// through the line holding its delimiter.
func skipHeredocs(line string, index *int, delimiters []string) {
	for _, delimiter := range delimiters {
		for *index+1 < len(line) {
			end := strings.IndexByte(line[*index+1:], '\n')
			bodyLine := line[*index+1:]
			if end < 0 {
				*index = len(line) - 1
			} else {
				bodyLine = line[*index+1 : *index+1+end]
				*index += end + 1
			}
			if strings.TrimSpace(bodyLine) == delimiter {
				break
			}
		}
	}
}

func heredocDelimiter(rest string) string {
	rest = strings.TrimLeft(strings.TrimPrefix(rest, "-"), " \t")
	end := strings.IndexAny(rest, " \t\n;|&)")
	if end < 0 {
		end = len(rest)
	}
	return strings.Trim(rest[:end], `'"\`)
}

// shellWords splits a simple command into words, removing quotes. It stops at
// a redirection so "cat <<EOF" and "go test > log" read as cat and go test.
func shellWords(text string) []string {
	words, _ := shellWordsRest(text)
	return words
}

// shellWordsRest also returns the text from the first redirection on.
func shellWordsRest(text string) ([]string, string) {
	words := []string{}
	var word strings.Builder
	inWord := false
	var quote byte
	for index := 0; index < len(text); index++ {
		char := text[index]
		if quote != '\'' && (char == '`' || strings.HasPrefix(text[index:], "$(") || strings.HasPrefix(text[index:], "${")) {
			if end := matchSubstitution(text, index); end > index {
				word.WriteString(text[index:end])
				inWord = true
				index = end - 1
				continue
			}
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				word.WriteByte(char)
			}
			continue
		}
		switch char {
		case '\'', '"':
			quote = char
			inWord = true
		case ' ', '\t', '\n':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		case '\\':
			if index+1 < len(text) {
				index++
				word.WriteByte(text[index])
				inWord = true
			}
		case '<', '>':
			start := index
			if inWord && isAllDigits(word.String()) {
				start -= word.Len()
				word.Reset()
				inWord = false
			}
			if inWord {
				words = append(words, word.String())
			}
			return words, text[start:]
		default:
			word.WriteByte(char)
			inWord = true
		}
	}
	if inWord {
		words = append(words, word.String())
	}
	return words, ""
}

func matchSubstitution(text string, start int) int {
	if start >= len(text) {
		return 0
	}
	if text[start] == '`' {
		for i := start + 1; i < len(text); i++ {
			if text[i] == '\\' {
				i++
				continue
			}
			if text[i] == '`' {
				return i + 1
			}
		}
		return 0
	}
	open := byte('(')
	i := start
	if strings.HasPrefix(text[start:], "${") {
		open = '{'
		i++
	} else if strings.HasPrefix(text[start:], "$(") {
		i++
	} else if text[start] != '(' {
		return 0
	}
	stack := []byte{open}
	var quote byte
	for i++; i < len(text); i++ {
		ch := text[i]
		if ch == '\\' {
			i++
			continue
		}
		if quote == '\'' {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if quote == '"' && ch == '"' {
			quote = 0
			continue
		}
		if ch == '\'' && quote == 0 {
			quote = ch
			continue
		}
		if ch == '"' && quote == 0 {
			quote = ch
			continue
		}
		if ch == '`' {
			if end := matchSubstitution(text, i); end > i {
				i = end - 1
				continue
			}
		}
		if strings.HasPrefix(text[i:], "$(") || strings.HasPrefix(text[i:], "${") {
			if end := matchSubstitution(text, i); end > i {
				i = end - 1
				continue
			}
		}
		if quote == '"' {
			continue
		}
		if ch == '(' || ch == '{' {
			stack = append(stack, ch)
		}
		if ch == ')' || ch == '}' {
			if len(stack) == 0 || (ch == ')' && stack[len(stack)-1] != '(') || (ch == '}' && stack[len(stack)-1] != '{') {
				return 0
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return i + 1
			}
		}
	}
	return 0
}

func assignmentOnly(text string) bool {
	words := shellWords(text)
	if len(words) == 0 {
		return false
	}
	for _, word := range words {
		eq := strings.IndexByte(word, '=')
		if eq <= 0 || !isShellName(word[:eq]) {
			return false
		}
	}
	return true
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// shellWrappers run the command that follows them; their own options are
// skipped. The value is how many non-flag arguments the wrapper consumes.
var shellWrappers = map[string]int{"sudo": 0, "time": 0, "nohup": 0, "command": 0, "exec": 0, "env": 0, "timeout": 1, "gtimeout": 1, "nice": 0, "caffeinate": 0, "stdbuf": 0, "xcrun": 0}

// shellSubcommandPrograms are programs whose first operand names the action.
var shellSubcommandPrograms = map[string]bool{
	"git": true, "go": true, "npm": true, "pnpm": true, "yarn": true, "bun": true, "cargo": true, "docker": true,
	"kubectl": true, "gh": true, "brew": true, "pip": true, "pip3": true, "uv": true, "poetry": true, "make": true,
	"gcloud": true, "aws": true, "terraform": true, "swift": true, "deno": true, "npx": true, "bunx": true, "pnpx": true,
	"rustup": true, "conda": true, "apt": true, "apt-get": true, "systemctl": true, "launchctl": true, "pharos": true,
	"tl1": true, "claude": true, "codex": true, "gradle": true, "mvn": true, "dotnet": true, "helm": true, "firebase": true,
	"vercel": true, "supabase": true, "prisma": true, "turbo": true, "nx": true, "just": true, "task": true, "xcodebuild": false,
	"sqlite3": false, "defaults": true, "security": true, "diskutil": true, "hdiutil": true, "codesign": false, "open": false,
}

// shellOptionArguments lists global options that take a separate value, so
// "git -C repo status" reads as git status.
var shellOptionArguments = map[string]map[string]bool{
	"git":    {"-C": true, "-c": true, "--git-dir": true, "--work-tree": true},
	"go":     {},
	"npm":    {"--prefix": true, "-w": true, "--workspace": true},
	"pnpm":   {"-C": true, "--dir": true, "--filter": true, "-F": true},
	"yarn":   {"--cwd": true},
	"make":   {"-C": true, "-f": true, "-j": true},
	"docker": {"-H": true, "--context": true},
	"cargo":  {"--manifest-path": true},
	"gh":     {"-R": true, "--repo": true},
}

// shellSetupPrograms prepare the shell rather than do the work.
var shellSetupPrograms = map[string]bool{"mktemp": true, "mkdir": true, "for": true, "while": true, "until": true, "if": true, "case": true, "select": true, "[": true, "[[": true, "test": true, "cd": true, "pwd": true, "pushd": true, "popd": true, "export": true, "set": true, "unset": true, "source": true, ".": true, "true": true, ":": true, "echo": true, "printf": true, "sleep": true, "trap": true, "shopt": true, "local": true, "declare": true, "alias": true, "nvm": true}

// shellKeywords introduce the command that follows them in a compound
// command ("then cat x", "do go test"); they are not programs.
var shellKeywords = map[string]bool{"then": true, "do": true, "else": true, "elif": true, "!": true, "{": true, "(": true, "done": true, "fi": true, "esac": true, "}": true}

func shellProgram(text string) (string, string) {
	words := shellWords(strings.TrimLeft(text, "({ "))
	index := 0
	for index < len(words) {
		word := words[index]
		if shellKeywords[word] {
			index++
			continue
		}
		if equals := strings.IndexByte(word, '='); equals > 0 && !strings.HasPrefix(word, "-") && isShellName(word[:equals]) {
			index++
			continue
		}
		consumes, wrapper := shellWrappers[path.Base(word)]
		if word == "command" && index+1 < len(words) && (words[index+1] == "-v" || words[index+1] == "-V") {
			break // "command -v go" looks go up rather than running it.
		}
		if !wrapper {
			break
		}
		if index == len(words)-1 {
			// A bare wrapper ("env", "time") runs on its own.
			break
		}
		index++
		for index < len(words) && (strings.HasPrefix(words[index], "-") || (path.Base(word) == "env" && strings.Contains(words[index], "="))) {
			index++
		}
		index += consumes
	}
	if index >= len(words) {
		for i := len(words) - 1; i >= 0; i-- {
			if eq := strings.IndexByte(words[i], '='); eq >= 0 {
				value := words[i][eq+1:]
				if strings.HasPrefix(value, "$((") {
					continue
				}
				if strings.HasPrefix(value, "$(") && matchSubstitution(value, 0) == len(value) {
					return shellProgram(value[2 : len(value)-1])
				}
				if strings.HasPrefix(value, "`") && matchSubstitution(value, 0) == len(value) {
					return shellProgram(value[1 : len(value)-1])
				}
			}
		}
		return "", ""
	}
	program := strings.ToLower(path.Base(words[index]))
	if program == "" {
		return "", ""
	}
	rest := words[index+1:]
	switch program {
	case "python", "python3", "python3.11", "python3.12", "python3.13", "python3.14":
		for position, word := range rest {
			if word == "-m" && position+1 < len(rest) {
				return program, "-m " + rest[position+1]
			}
			if word == "-c" {
				return program, "-c"
			}
			if word == "-" {
				return program, "stdin"
			}
			if !strings.HasPrefix(word, "-") {
				return program, path.Base(word)
			}
		}
		return program, ""
	case "node", "ruby", "perl", "bash", "sh", "zsh", "tsx", "ts-node":
		for _, word := range rest {
			if word == "-e" || word == "-c" {
				return program, word
			}
			if !strings.HasPrefix(word, "-") {
				return program, path.Base(word)
			}
		}
		return program, ""
	}
	if !shellSubcommandPrograms[program] {
		return program, ""
	}
	options := shellOptionArguments[program]
	for position := 0; position < len(rest); position++ {
		word := rest[position]
		if strings.HasPrefix(word, "-") || (program == "cargo" && strings.HasPrefix(word, "+")) {
			if options[word] {
				position++
			}
			continue
		}
		if (word == "run" || word == "exec" || word == "x" || word == "dlx") && (program == "npm" || program == "pnpm" || program == "yarn" || program == "bun") {
			target := position + 1
			if target < len(rest) && rest[target] == "--" {
				target++
			}
			if target < len(rest) && !strings.HasPrefix(rest[target], "-") {
				return program, word + " " + rest[target]
			}
		}
		return program, word
	}
	return program, ""
}

func isShellName(value string) bool {
	for index, char := range value {
		if char == '_' || (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return value != ""
}

var shellProgramCategories = map[string]string{
	"cat": "read", "head": "read", "tail": "read", "less": "read", "more": "read", "nl": "read", "bat": "read", "wc": "read",
	"file": "read", "stat": "read", "xxd": "read", "hexdump": "read", "od": "read", "strings": "read", "jq": "read", "yq": "read",
	"awk": "read", "cut": "read", "sort": "read", "uniq": "read", "column": "read", "tr": "read", "diff": "read", "cmp": "read",
	"md5": "read", "shasum": "read", "sha256sum": "read", "base64": "read", "plutil": "read", "readlink": "read", "realpath": "read",
	"grep": "search", "rg": "search", "ag": "search", "ack": "search", "find": "search", "fd": "search", "locate": "search", "mdfind": "search", "egrep": "search", "fgrep": "search",
	"ls": "list", "tree": "list", "du": "list", "df": "list", "pwd": "list", "eza": "list", "exa": "list",
	"git": "vcs", "gh": "vcs",
	"make": "build", "tsc": "build", "esbuild": "build", "swiftc": "build", "xcodebuild": "build", "gradle": "build", "mvn": "build", "cmake": "build", "ninja": "build", "vite": "build", "webpack": "build", "rollup": "build",
	"pytest": "test", "jest": "test", "eslint": "test", "zcat": "read", "gzcat": "read", "nvm": "shell", "apply_patch": "file-write", "vitest": "test", "playwright": "test", "mocha": "test",
	"brew": "package", "pip": "package", "pip3": "package", "uv": "package", "poetry": "package", "conda": "package", "apt": "package", "apt-get": "package", "rustup": "package",
	"mktemp": "shell", "mv": "file-write", "cp": "file-write", "rm": "file-write", "mkdir": "file-write", "touch": "file-write", "chmod": "file-write", "ln": "file-write",
	"tee": "file-write", "patch": "file-write", "rsync": "file-write", "unzip": "file-write", "zip": "file-write", "tar": "file-write", "rmdir": "file-write", "install": "file-write",
	"curl": "network", "wget": "network", "ssh": "network", "scp": "network", "nc": "network", "ping": "network", "http": "network", "dig": "network", "nslookup": "network",
	"ps": "process", "kill": "process", "pkill": "process", "pgrep": "process", "lsof": "process", "top": "process", "open": "process", "osascript": "process",
	"launchctl": "process", "systemctl": "process", "killall": "process", "wait": "process", "jobs": "process", "screencapture": "process",
	"python": "run", "python3": "run", "node": "run", "deno": "run", "ruby": "run", "perl": "run", "bash": "run", "sh": "run", "zsh": "run", "tsx": "run", "ts-node": "run", "swift": "run",
	"java": "run", "npx": "run", "bunx": "run", "pnpx": "run",
	"sqlite3": "database", "psql": "database", "mysql": "database", "redis-cli": "database", "duckdb": "database",
	"docker": "container", "kubectl": "container", "helm": "container", "podman": "container",
	"gcloud": "cloud", "aws": "cloud", "terraform": "cloud", "vercel": "cloud", "firebase": "cloud", "supabase": "cloud",
	"cd": "shell", "pushd": "shell", "popd": "shell", "export": "shell", "set": "shell", "unset": "shell", "source": "shell", ".": "shell", "true": "shell", ":": "shell",
	"echo": "shell", "printf": "shell", "sleep": "shell", "which": "shell", "type": "shell", "env": "shell", "date": "shell", "whoami": "shell", "test": "shell", "[": "shell",
	"[[": "shell", "command": "shell", "trap": "shell", "exit": "shell", "hostname": "shell", "uname": "shell", "sw_vers": "shell", "id": "shell", "tput": "shell",
}

func shellCategory(program, subcommand, text string) string {
	switch program {
	case "":
		return "other"
	case "git":
		if subcommand == "grep" {
			return "search"
		}
		return "vcs"
	case "cat":
		// "cat > file <<EOF" and "cat a b > c" write a file.
		if authorshipOutputFile(text, authorshipShellSyntax(text)) {
			return "file-write"
		}
		return "read"
	case "gofmt":
		switch {
		case shellHasFlag(text, "-w"):
			return "file-write"
		case shellHasFlag(text, "-l", "-d"):
			return "test"
		}
		return "read"
	case "rustfmt":
		if shellHasFlag(text, "--check") {
			return "test"
		}
		return "file-write"
	case "prettier":
		switch {
		case shellHasFlag(text, "--write", "-w"):
			return "file-write"
		case shellHasFlag(text, "--check", "-c", "--list-different", "-l"):
			return "test"
		}
		return "read"
	case "unzip":
		if shellHasFlag(text, "-l", "-p", "-t", "-Z", "-v") {
			return "read"
		}
		return "file-write"
	case "gzip", "gunzip", "zstd", "xz", "unxz", "bzip2", "bunzip2":
		// Without -c these replace the file they compress or expand.
		if shellHasFlag(text, "-c", "--stdout", "-l", "--list", "-t", "--test") {
			return "read"
		}
		return "file-write"
	case "npx", "bunx", "pnpx", "uvx":
		return shellRunnerCategory(text, program)
	case "uv", "poetry":
		if subcommand == "run" {
			return shellRunnerCategory(text, "run")
		}
	case "sed":
		words := shellWords(text)
		for _, word := range words[1:] {
			if word == "-i" || strings.HasPrefix(word, "-i") || word == "--in-place" {
				return "file-write"
			}
		}
		return "read"
	case "perl":
		if strings.Contains(text, " -pi") || strings.Contains(text, " -i") {
			return "file-write"
		}
	case "go", "cargo", "npm", "pnpm", "yarn", "bun", "deno", "swift", "dotnet":
		verb := strings.Fields(subcommand)
		first := ""
		if len(verb) > 0 {
			first = verb[0]
		}
		script := ""
		if len(verb) > 1 {
			script = verb[1]
		}
		if first == "exec" || first == "x" || first == "dlx" {
			return shellRunnerCategory(text, first)
		}
		// pnpm, yarn and bun run a package script named directly ("pnpm typecheck").
		task := first
		if first == "run" && script != "" {
			task = script
		}
		switch {
		case strings.HasPrefix(task, "test") || task == "vet" || strings.HasPrefix(task, "lint") || strings.HasPrefix(task, "typecheck") ||
			task == "check" || strings.HasPrefix(task, "check:") || task == "clippy":
			return "test"
		case strings.HasPrefix(task, "build") || task == "compile" || task == "generate":
			return "build"
		case first == "install" || first == "i" || first == "ci" || first == "add" || first == "get" || first == "mod" || first == "remove" || first == "update" || first == "upgrade" || first == "uninstall":
			return "package"
		case strings.HasPrefix(task, "fmt") || strings.HasPrefix(task, "format"):
			return "file-write"
		case first == "list" || first == "ls" || first == "version" || first == "env" || first == "doc" || first == "view" || first == "info" || first == "outdated" || first == "why" || first == "tree":
			return "read"
		}
		return "run"
	case "python", "python3", "python3.11", "python3.12", "python3.13", "python3.14":
		if subcommand == "-m pytest" || subcommand == "-m unittest" {
			return "test"
		}
		if subcommand == "-m pip" {
			return "package"
		}
		return "run"
	}
	if category, ok := shellProgramCategories[program]; ok {
		return category
	}
	if strings.HasPrefix(program, "python") {
		return "run"
	}
	if strings.Contains(text, "/") && (strings.HasPrefix(strings.TrimSpace(text), "./") || strings.HasPrefix(strings.TrimSpace(text), "/")) {
		return "run"
	}
	return "other"
}

// shellRunnerOptions take a separate value in runners such as "npx -p pkg"
// and "uv run --with pkg".
var shellRunnerOptions = map[string]bool{"-p": true, "--package": true, "--with": true, "--python": true, "--project": true, "--directory": true,
	"--from": true, "-C": true, "--dir": true, "--filter": true, "-F": true, "--env-file": true, "--group": true, "--extra": true}

// shellRunnerCategory classifies the command a runner starts: the words after
// the marker ("npx", "exec", "run") and the runner's own options. A runner
// whose target is unknown, or absent, is a run.
func shellRunnerCategory(text, marker string) string {
	words, redirect := shellWordsRest(text)
	start := -1
	for index, word := range words {
		if strings.ToLower(path.Base(word)) == marker {
			start = index + 1
			break
		}
	}
	for start >= 0 && start < len(words) && strings.HasPrefix(words[start], "-") {
		if words[start] == "--" {
			start++
			break
		}
		if shellRunnerOptions[words[start]] {
			start++
		}
		start++
	}
	if start < 0 || start >= len(words) {
		return "run"
	}
	// The runner's redirections apply to its target: "pnpm exec cat > f" writes f.
	target := strings.TrimSpace(strings.Join(words[start:], " ") + " " + redirect)
	program, subcommand := shellProgram(target)
	if category := shellCategory(program, subcommand, target); category != "other" {
		return category
	}
	return "run"
}

// shellHasFlag reports whether a simple command passes one of the flags,
// alone or, for single-letter flags, combined ("-lw").
func shellHasFlag(text string, flags ...string) bool {
	for _, word := range shellWords(text) {
		if word == "--" {
			return false
		}
		for _, flag := range flags {
			if word == flag || strings.HasPrefix(word, flag+"=") ||
				(len(flag) == 2 && flag[0] == '-' && len(word) > 2 && word[0] == '-' && word[1] != '-' && strings.ContainsRune(word[1:], rune(flag[1]))) {
				return true
			}
		}
	}
	return false
}

// shellScript returns the script of a command line that only hands it to a
// shell, as in `/bin/zsh -lc "sed -n '1,9p' f"`, so the script's own commands
// are classified rather than the shell.
func shellScript(line string) (string, bool) {
	words := []string{}
	var word strings.Builder
	inWord := false
	var quote byte
	for index := 0; index < len(line); index++ {
		char := line[index]
		switch {
		case quote == '\'':
			if char == '\'' {
				quote = 0
			} else {
				word.WriteByte(char)
			}
		case quote == '"':
			if char == '"' {
				quote = 0
			} else if char == '\\' && index+1 < len(line) && strings.IndexByte("\\\"$`\n", line[index+1]) >= 0 {
				index++
				if line[index] != '\n' {
					word.WriteByte(line[index])
				}
			} else {
				word.WriteByte(char)
			}
		case char == '\'' || char == '"':
			quote = char
			inWord = true
		case char == '\\' && index+1 < len(line):
			index++
			word.WriteByte(line[index])
			inWord = true
		case char == ' ' || char == '\t':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		case strings.IndexByte(";&|<>()`$\n#", char) >= 0:
			// Anything the invoking shell would interpret first.
			return "", false
		default:
			word.WriteByte(char)
			inWord = true
		}
	}
	if quote != 0 {
		return "", false
	}
	if inWord {
		words = append(words, word.String())
	}
	if len(words) != 3 || (words[1] != "-c" && words[1] != "-lc") {
		return "", false
	}
	switch path.Base(words[0]) {
	case "sh", "bash", "zsh":
		return words[2], strings.TrimSpace(words[2]) != ""
	}
	return "", false
}
