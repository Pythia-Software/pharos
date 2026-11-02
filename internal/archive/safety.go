package archive

// Safety turns supported shell operations into questions for the user's agent.
// Selection is deterministic and deliberately makes no authorization or harm
// judgement. Nothing here executes a command or inspects the present filesystem.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const safetyVersion = "safety-v1"
const safetyWindowDays = 28

type safetyRule struct {
	Title, Explanation string
	Questions          []string
	Priority           int
}

var safetyRules = map[string]safetyRule{
	"credentials":     {"Check where credentials were copied", "A command selected credential files or cloud configuration for copying to another location.", []string{"Which sources contain credentials rather than configuration?", "Was the destination uploaded, published, or later cleaned up?"}, 0},
	"database":        {"Review a database change", "A command contains a database mutation. Its environment and affected records need checking.", []string{"Which database and environment were selected?", "What tables and predicates bound the change, and what result or recovery evidence exists?"}, 0},
	"remote-history":  {"Review a change to published Git history", "A command selects a force push or remote ref deletion.", []string{"Which remote and refs were selected?", "What did the lease protect, what did the remote report, and what user request covered the change?"}, 0},
	"boundary-delete": {"Review a deletion outside this checkout", "A recursive deletion selects content outside the recorded checkout, or its target could not be resolved.", []string{"Resolve the target using the archived working directory and command context.", "Was the content created for this task, preserved elsewhere, or potentially pre-existing?", "What establishes execution, affected contents, and recovery?"}, 0},
	"cloud-delete":    {"Review a cloud deletion", "A command selects remote objects for deletion.", []string{"Which account, bucket, and object prefix were selected?", "What establishes deletion, retention, and recovery?"}, 0},
	"publication":     {"Review a merge or publication", "A command selects a merge, release change, or tag publication.", []string{"Which repository, branch, release, or tag was selected?", "What request covered the operation, and what result establishes its outcome and audience?"}, 0},
	"system":          {"Review a system change", "A command selects a system service, installation, or disk change.", []string{"Was this a test resource or a persistent system resource?", "What privileges, persistence, and recovery consequences were involved?"}, 0},
	"local-history":   {"Check a change to saved Git work", "A command selects a reset, history rewrite, branch deletion, or stash discard.", []string{"Which refs or saved work were selected?", "Was the work preserved, and what does the result establish?"}, 1},
	"delete":          {"Check content selected for deletion", "A command selects checkout content for deletion without establishing that it is disposable.", []string{"Which exact paths were selected?", "Was the content created for this task or preserved elsewhere, and what does the result establish?"}, 1},
	"process":         {"Check which processes were selected", "A process signal uses a name or expression that may match more than this task's process.", []string{"What could the selector match?", "Is there launch or ownership evidence, and was unrelated work affected?"}, 1},
	"permissions":     {"Review a permission change", "A command selects permissions outside the recorded checkout or against an unresolved path.", []string{"Which resource and mode or owner were selected?", "Does this broaden access, repair permissions, or protect sensitive content?"}, 1},
	"api":             {"Check what an API request changes", "An HTTP mutation needs its endpoint and payload semantics checked.", []string{"Which endpoint, method, account, and resource were selected?", "Was this a local test or a consequential change, and what does the response establish?"}, 1},
	"disk-image":      {"Check a disk image operation", "A command selects a disk image or mounted volume for a lifecycle operation.", []string{"Was this a newly created test image or an existing volume?", "Which image or mount was selected, and what does the result establish?"}, 1},
}

type safetyCall struct {
	ID, Workspace, Conversation, Message, ResultMessage, At, Status    string
	Repository, RepositoryLabel, Host, HostLabel, Root, Command, Input string
	Exit                                                               sql.NullInt64
}

type safetyEvidence struct {
	ID            string `json:"id"`
	CallID        string `json:"call_id"`
	Workspace     string `json:"workspace_id"`
	Conversation  string `json:"conversation_id"`
	Message       string `json:"message_id"`
	ResultMessage string `json:"result_message_id,omitempty"`
	At            string `json:"at"`
	Command       string `json:"command"`
	CWD           string `json:"cwd"`
	CWDBasis      string `json:"cwd_basis"`
	Status        string `json:"status"`
	Outcome       string `json:"outcome"`
	Conditional   bool   `json:"conditional"`
	Path          string `json:"path"`
}

type safetyCandidate struct {
	ID, Rule, Target, Label, Subject, Repository string
	Evidence                                     []safetyEvidence
	Calls                                        int
	First, Last, LastCall                        string
	Strongest                                    *safetyEvidence
	// Completion is only evidence of a successful *single* shell command,
	// never a claim that every segment of a compound call ran or caused harm.
	Completion bool
}

var safetyCredential = regexp.MustCompile(`(?i)(/\.config/gcloud(?:/|$)|credentials\.db|access_tokens\.db|application_default_credentials\.json|/\.aws/credentials|/id_(?:rsa|ed25519)(?:$|\.)|/\.netrc$|/\.npmrc$|(?:^|/)\.env(?:\.|$))`)
var safetySQL = regexp.MustCompile(`(?i)^\s*(?:DELETE\s+FROM|UPDATE\s+|INSERT\s+INTO|DROP\s+|TRUNCATE\s+|ALTER\s+|REPLACE\s+INTO)`)
var safetySQLMutation = regexp.MustCompile(`(?i)\b(?:DELETE\s+FROM|UPDATE\s+|INSERT\s+INTO|DROP\s+|TRUNCATE\s+|ALTER\s+|REPLACE\s+INTO)`)
var safetySQLDollarQuote = regexp.MustCompile(`^\$(?:[A-Za-z_][A-Za-z_0-9]*)?\$`)
var safetyScriptCWD = regexp.MustCompile(`\b(?:workdir|cwd)\s*:\s*("(?:[^"\\]|\\.)*")`)

func safetyRedact(text string) string {
	text = feedbackSecrets.ReplaceAllString(feedbackBearer.ReplaceAllString(text, "Bearer [redacted]"), "${1}[redacted]")
	for _, secret := range labelingSecrets {
		text = secret.pattern.ReplaceAllString(text, secret.replacement)
	}
	return text
}

// argv requires the lexical program to be present as a word. Substitutions,
// variables, script bodies and quoted prose are never evaluated as commands.
func safetyArgv(segment shellSegment) []string {
	words := shellWords(segment.Text)
	for i, word := range words {
		if path.Base(word) == segment.Program {
			return words[i:]
		}
	}
	return nil
}

func safetyOperands(args []string) []string {
	values := []string{}
	options := true
	for _, arg := range args {
		if options && arg == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(arg, "-") {
			continue
		}
		values = append(values, arg)
	}
	return values
}

// Only actual options can suppress discovery or change its breadth. Values
// and operands following -- must not be interpreted as help/dry-run switches.
var safetyValueOptions = map[string][]string{
	"curl":    {"-X", "-d", "-F", "-T", "-H", "-u", "-o", "-b", "-c", "-D", "-x", "-U", "-m", "-E", "-w", "-K", "-e", "-A", "--request", "--url", "--data", "--data-ascii", "--data-binary", "--data-raw", "--data-urlencode", "--json", "--form", "--form-string", "--upload-file", "--header", "--user", "--output", "--dump-header", "--cookie", "--cookie-jar", "--proxy", "--proxy-user", "--connect-timeout", "--max-time", "--retry", "--retry-delay", "--cacert", "--capath", "--cert", "--key", "--resolve", "--connect-to", "--write-out", "--request-target", "--config", "--referer", "--user-agent"},
	"git":     {"-C", "-c", "-o", "-e", "-m", "--git-dir", "--work-tree", "--namespace", "--config-env", "--push-option", "--receive-pack", "--exec", "--repo", "--exclude", "--message"},
	"gh":      {"-R", "-b", "-F", "-t", "--repo", "--hostname", "--body", "--body-file", "--title", "--subject", "--author-email", "--match-head-commit", "--notes", "--notes-file", "--notes-start-tag", "--target", "--discussion-category"},
	"rsync":   {"-e", "-f", "-B", "-T", "--rsh", "--filter", "--exclude", "--include", "--exclude-from", "--include-from", "--rsync-path", "--chown", "--chmod", "--suffix", "--backup-dir", "--files-from", "--max-delete", "--block-size", "--temp-dir"},
	"cp":      {"-t", "-S", "--target-directory", "--suffix"},
	"mv":      {"-t", "-S", "--target-directory", "--suffix"},
	"sqlite3": {"-cmd", "--cmd", "-init", "-separator", "-newline", "-nullvalue", "-vfs"},
	"psql":    {"-c", "-d", "-f", "-h", "-p", "-U", "-v", "-P", "-o", "--command", "--dbname", "--file", "--host", "--port", "--username", "--set", "--pset", "--output"},
	"mysql":   {"-e", "-D", "-h", "-u", "-P", "--execute", "--database", "--host", "--user", "--port", "--password"},
	"chmod":   {"--reference"}, "chown": {"--reference", "--from"},
	"pkill":  {"-F", "-u", "-U", "-P", "-g", "-G", "-s", "-t", "--pidfile", "--signal"},
	"aws":    {"--profile", "--region", "--endpoint-url", "--query", "--output", "--exclude", "--include"},
	"gcloud": {"--project", "--account", "--configuration", "--format", "--filter", "--flags-file"},
}

func safetyParseOptions(program string, args []string) (flags, operands []string) {
	values := safetyValueOptions[program]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			operands = append(operands, args[i+1:]...)
			break
		}
		if program == "sqlite3" && a == "-help" {
			flags = append(flags, "--help")
			continue
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			operands = append(operands, a)
			continue
		}
		name, _, attached := strings.Cut(a, "=")
		if strings.HasPrefix(a, "--") || slices.Contains(values, name) {
			flags = append(flags, a)
			if slices.Contains(values, name) && !attached {
				i++
			}
			continue
		}
		for j := 1; j < len(a); j++ {
			flag := "-" + a[j:j+1]
			flags = append(flags, flag)
			if slices.Contains(values, flag) {
				if j == len(a)-1 {
					i++
				}
				break
			}
		}
	}
	return flags, operands
}

func safetyLiteralSelection(value string) bool {
	return value != "" && !strings.ContainsAny(value, "$`*?[]{}\n\r") && !strings.HasPrefix(value, "~")
}

// Mask strings, quoted identifiers and comments before inspecting each SQL
// statement. A semicolon inside quoted data never starts another statement.
func safetySQLMutates(text, program string) bool {
	var clean strings.Builder
	for i := 0; i < len(text); {
		switch {
		case strings.HasPrefix(text[i:], "--") || program == "mysql" && text[i] == '#':
			for i < len(text) && text[i] != '\n' {
				i++
			}
			clean.WriteByte(' ')
		case strings.HasPrefix(text[i:], "/*"):
			i += 2
			for depth := 1; i < len(text) && depth > 0; {
				if strings.HasPrefix(text[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(text[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
			clean.WriteByte(' ')
		case strings.ContainsRune("'\"`[", rune(text[i])):
			escape := program == "mysql" || program == "psql" && i > 0 && (text[i-1] == 'E' || text[i-1] == 'e')
			close := text[i]
			if close == '[' {
				close = ']'
			}
			i++
			for i < len(text) {
				if text[i] == close {
					i++
					if i < len(text) && text[i] == close {
						i++
						continue
					}
					break
				}
				if escape && text[i] == '\\' && i+1 < len(text) {
					i++
				}
				i++
			}
			clean.WriteString(" ? ")
		case text[i] == '$' && safetySQLDollarQuote.FindString(text[i:]) != "":
			quote := safetySQLDollarQuote.FindString(text[i:])
			i += len(quote)
			if end := strings.Index(text[i:], quote); end >= 0 {
				i += end + len(quote)
			} else {
				i = len(text)
			}
			clean.WriteString(" ? ")
		default:
			clean.WriteByte(text[i])
			i++
		}
	}
	for _, statement := range strings.Split(clean.String(), ";") {
		words := strings.Fields(statement)
		if safetySQL.MatchString(statement) || (len(words) > 0 && strings.EqualFold(words[0], "WITH") && safetySQLMutation.MatchString(statement)) {
			return true
		}
	}
	return false
}

func safetyFlag(args []string, letters, long string) bool {
	for _, a := range args {
		if a == "--" {
			break
		}
		if long != "" && (a == long || strings.HasPrefix(a, long+"=")) {
			return true
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a[1:], letters) {
			return true
		}
	}
	return false
}

// curl's request method is independent of option order: an explicit -X wins,
// and -G keeps data on the query string. Consume option values so a URL in a
// header or body is never mistaken for the selected endpoint.
func safetyCurl(args []string) (string, []string) {
	method := ""
	get, head, payload, upload := false, false, false, false
	urls := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func(attached string, present bool) string {
			if present {
				return attached
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		if a == "--" {
			urls = append(urls, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "--") {
			name, attached, present := strings.Cut(a, "=")
			switch name {
			case "--request":
				method = strings.ToUpper(value(attached, present))
			case "--url":
				urls = append(urls, value(attached, present))
			case "--get":
				get = true
			case "--no-get":
				get = false
			case "--head":
				head = true
			case "--no-head":
				head = false
			case "--data", "--data-ascii", "--data-binary", "--data-raw", "--data-urlencode", "--json", "--form", "--form-string":
				payload = true
				value(attached, present)
			case "--upload-file":
				upload = true
				value(attached, present)
			default:
				if slices.Contains(safetyValueOptions["curl"], name) {
					value(attached, present)
				}
			}
		} else if strings.HasPrefix(a, "-") && a != "-" {
			for j := 1; j < len(a); j++ {
				flag := a[j]
				if flag == 'G' {
					get = true
				} else if flag == 'I' {
					head = true
				} else if slices.Contains(safetyValueOptions["curl"], "-"+string(flag)) {
					v := value(a[j+1:], j+1 < len(a))
					switch flag {
					case 'X':
						method = strings.ToUpper(v)
					case 'd', 'F':
						payload = true
					case 'T':
						upload = true
					}
					break
				}
			}
		} else {
			urls = append(urls, a)
		}
	}
	if method == "" {
		switch {
		case head:
			method = "HEAD"
		case upload:
			method = "PUT"
		case get:
			method = "GET"
		case payload:
			method = "POST"
		default:
			method = "GET"
		}
	}
	return method, urls
}

func safetyGH(args []string) ([]string, string) {
	ops, repo := []string{}, ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			ops = append(ops, args[i+1:]...)
			break
		}
		if a == "-R" || a == "--repo" {
			if i+1 < len(args) {
				i++
				repo = args[i]
			}
		} else if strings.HasPrefix(a, "--repo=") {
			repo = strings.TrimPrefix(a, "--repo=")
		} else if strings.HasPrefix(a, "-R") {
			repo = strings.TrimPrefix(a, "-R")
		} else if name, _, attached := strings.Cut(a, "="); slices.Contains(safetyValueOptions["gh"], name) {
			if !attached {
				i++
			}
		} else if len(a) > 2 && slices.Contains(safetyValueOptions["gh"], a[:2]) {
			// Attached short-option value; it is neither a command nor a ref.
		} else if !strings.HasPrefix(a, "-") {
			ops = append(ops, a)
		}
	}
	return ops, repo
}

func safetyPushOperands(args []string) []string {
	// args starts after push; --repo supplies the remote without an operand.
	ops := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(ops, args[i+1:]...)
		}
		if slices.Contains([]string{"-o", "--push-option", "--receive-pack", "--exec"}, a) {
			i++
		} else if a == "--repo" && i+1 < len(args) {
			i++
			ops = append([]string{args[i]}, ops...)
		} else if strings.HasPrefix(a, "--repo=") {
			ops = append([]string{strings.TrimPrefix(a, "--repo=")}, ops...)
		} else if !strings.HasPrefix(a, "-") {
			ops = append(ops, a)
		}
	}
	return ops
}

func safetyGitCommand(args []string, cwd, workspace string) (verb string, rest []string, context string, resolved bool) {
	root, rootOK, _ := safetyPath(".", cwd, workspace)
	if !rootOK {
		root = defaultString(nilIfEmpty(cwd), "Unresolved checkout")
	}
	extra, extraOK := []string{}, true
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		a := args[i]
		name, value, attached := strings.Cut(a, "=")
		if strings.HasPrefix(a, "-C") || strings.HasPrefix(a, "-c") {
			name, value, attached = a[:2], a[2:], len(a) > 2
		}
		if slices.Contains([]string{"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--config-env"}, name) {
			if !attached && i+1 < len(args) {
				i++
				value = args[i]
			}
			if name == "-C" {
				root, rootOK, _ = safetyPath(value, root, workspace)
			} else {
				if name == "--git-dir" || name == "--work-tree" {
					var ok bool
					value, ok, _ = safetyPath(value, root, workspace)
					extraOK = extraOK && ok
				}
				extraOK = extraOK && safetyLiteralSelection(value) && name != "--config-env"
				extra = append(extra, name+"="+value)
			}
		}
		i++
	}
	if i < len(args) {
		verb, rest = args[i], args[i+1:]
	}
	return verb, rest, strings.Join(append([]string{root}, extra...), " · "), rootOK && extraOK
}

func safetyCleanScope(args []string) (string, bool) {
	_, paths := safetyParseOptions("git", args)
	exclusions := []string{}
	for i := 0; i < len(args) && args[i] != "--"; i++ {
		a := args[i]
		if a == "-e" || a == "--exclude" {
			if i+1 < len(args) {
				i++
				exclusions = append(exclusions, args[i])
			}
		} else if strings.HasPrefix(a, "--exclude=") {
			exclusions = append(exclusions, strings.TrimPrefix(a, "--exclude="))
		} else if strings.HasPrefix(a, "-e") {
			exclusions = append(exclusions, a[2:])
		} else if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
			// -e may follow other short flags, e.g. git clean -fdekeep.
			if at := strings.IndexByte(a[1:], 'e'); at >= 0 {
				value := a[at+2:]
				if value == "" && i+1 < len(args) {
					i++
					value = args[i]
				}
				exclusions = append(exclusions, value)
			}
		}
	}
	resolved := true
	for _, value := range append(append([]string{}, paths...), exclusions...) {
		resolved = resolved && safetyLiteralSelection(value)
	}
	sort.Strings(paths)
	paths = slices.Compact(paths)
	sort.Strings(exclusions)
	exclusions = slices.Compact(exclusions)
	selection := "all paths"
	if len(paths) > 0 {
		selection = "pathspecs=" + jsonText(paths)
	}
	if len(exclusions) > 0 {
		selection += " · excludes=" + jsonText(exclusions)
	}
	return selection, resolved
}

func safetyPath(value, cwd, root string) (string, bool, bool) {
	// A shell pattern is a selection, not an expanded list of affected files.
	if strings.ContainsAny(value, "$`*?[]{}\n\r") || strings.HasPrefix(value, "~") || value == "" {
		return safetyRedact(value), false, false
	}
	if strings.Contains(value, ":") && !path.IsAbs(value) {
		return safetyRedact(value), false, false
	}
	resolved := value
	if !path.IsAbs(value) {
		if !path.IsAbs(cwd) {
			return value, false, false
		}
		resolved = path.Join(cwd, value)
	}
	resolved = path.Clean(resolved)
	inside := path.IsAbs(root) && (resolved == path.Clean(root) || strings.HasPrefix(resolved, path.Clean(root)+"/"))
	return safetyRedact(resolved), true, inside
}

type safetyCommand struct{ text, cwd, basis string }

func safetyCommands(call safetyCall) []safetyCommand {
	var envelope map[string]any
	_ = json.Unmarshal([]byte(call.Input), &envelope)
	input := envelope["input"]
	object := mapValueDefault(input)
	cwd, basis := firstString(object["workdir"], object["cwd"], object["Cwd"], object["working_directory"]), "tool input"
	if cwd == "" {
		cwd, basis = call.Root, "workspace metadata fallback; verify in archived context"
	}
	if command := shellCommandText(input); command != "" {
		return []safetyCommand{{command, cwd, basis}}
	}
	if script, ok := input.(string); ok {
		commands := scriptCommands(script)
		if len(commands) > 0 {
			// A single literal directory can be associated; multiple execution
			// objects need investigation rather than borrowing a sibling's cwd.
			cwd, basis = "", "Code Mode working directory unresolved"
			matches := safetyScriptCWD.FindAllStringSubmatch(script, -1)
			if len(commands) == 1 && len(matches) == 1 && json.Unmarshal([]byte(matches[0][1]), &cwd) == nil {
				basis = "literal Code Mode workdir"
			}
			out := []safetyCommand{}
			for _, command := range commands {
				out = append(out, safetyCommand{command, cwd, basis})
			}
			return out
		}
	}
	// Ledger commands are clipped to 2,000 characters. Do not interpret a
	// truncated token as an exact path when the original input is unavailable.
	if call.Command != "" && len(call.Command) < 1990 {
		return []safetyCommand{{call.Command, cwd, basis}}
	}
	return nil
}

func selectSafetyCall(call safetyCall) []safetyCandidate {
	result := []safetyCandidate{}
	commands := safetyCommands(call)
	for _, command := range commands {
		parsed := parseShellCommand(command.text)
		cwd := command.cwd
		cwdConditional := false
		for _, segment := range parsed.Segments {
			argv := safetyArgv(segment)
			if len(argv) == 0 {
				continue
			}
			program, args := segment.Program, argv[1:]
			if program == "cd" {
				ops := safetyOperands(args)
				if len(ops) == 1 {
					cwd, _, _ = safetyPath(ops[0], cwd, call.Root)
				} else {
					cwd = ""
				}
				cwdConditional = true
				continue
			}
			flags, ops := safetyParseOptions(program, args)
			if slices.Contains(flags, "--help") || slices.Contains(flags, "--dry-run") || slices.Contains(flags, "--dryrun") {
				continue
			}
			if (program == "curl" || program == "gh" || program == "git") && safetyFlag(flags, "h", "") {
				continue
			}
			if ((program == "git" && segment.Subcommand == "push") || program == "rsync") && safetyFlag(flags, "n", "--dry-run") {
				continue
			}
			if (program == "pkill" || program == "killall") && slices.Contains(flags, "-0") {
				continue
			}
			emit := func(rule, subject, breadth string, resolved bool) {
				resolved = resolved && !strings.ContainsAny(subject, "$`\n\r")
				subject = safetyRedact(subject)
				if subject == "" {
					subject = "Target needs context"
					resolved = false
				}
				target, label := "repo:"+call.Repository, call.RepositoryLabel
				if call.Repository == "" {
					target, label = "machine:"+call.Host, call.HostLabel
				}
				if call.Repository == "" && call.Host == "" {
					target, label = "workspace:"+call.Workspace, "Work with unknown machine"
				}
				identity := subject
				if !resolved {
					identity += ":unresolved:" + call.ID
				}
				id := stableID("safety", rule, target, defaultString(nilIfEmpty(call.Host), call.Workspace), identity, breadth)
				conditional := len(parsed.Segments) > 1 || len(commands) > 1 || cwdConditional || parsed.IsBackgrounded || segment.Operator != ""
				completed := !conditional && call.Status == "ok" && call.Exit.Valid && call.Exit.Int64 == 0 && call.ResultMessage != ""
				outcome := "Execution and effects need context"
				if completed {
					outcome = "Single command reported exit 0; resource effects still need verification"
				}
				if call.Status == "error" {
					outcome = "Call reported an error; partial effects remain possible"
				}
				params := ""
				if call.Conversation != "" {
					params = "?conversation=" + url.QueryEscape(call.Conversation) + "&message=" + url.QueryEscape(call.Message)
				}
				result = append(result, safetyCandidate{ID: id, Rule: rule, Target: target, Label: defaultString(nilIfEmpty(label), "Repository"), Subject: subject, Repository: call.Repository, Completion: completed,
					Evidence: []safetyEvidence{{ID: stableID("safety-evidence", call.ID, segment.Text, cwd), CallID: call.ID, Workspace: call.Workspace, Conversation: call.Conversation, Message: call.Message, ResultMessage: call.ResultMessage, At: call.At,
						Command: clipText(safetyRedact(segment.Text), 1000), CWD: safetyRedact(cwd), CWDBasis: command.basis, Status: call.Status, Outcome: outcome, Conditional: conditional, Path: "/work/" + call.Workspace + params}}})
			}
			emitPath := func(rule, value, breadth string) {
				subject, resolved, _ := safetyPath(value, cwd, call.Root)
				emit(rule, subject, breadth, resolved)
			}
			switch program {
			case "rm", "rmdir":
				for _, value := range ops {
					subject, resolved, inside := safetyPath(value, cwd, call.Root)
					rule, breadth := "delete", "single-path"
					if safetyFlag(flags, "rR", "--recursive") {
						breadth = "recursive"
						if !resolved || !inside {
							rule = "boundary-delete"
						}
					}
					emit(rule, subject, breadth, resolved)
				}
			case "cp", "mv", "rsync":
				ops = []string{}
				destination := ""
				prunesDestination := false
				for i := 0; i < len(args); i++ {
					a := args[i]
					if a == "--" {
						ops = append(ops, args[i+1:]...)
						break
					}
					if slices.Contains([]string{"-t", "--target-directory", "--exclude", "--include", "--exclude-from", "--include-from", "--filter", "-e", "--rsh", "--rsync-path", "--chown", "--chmod", "--suffix", "--backup-dir", "--files-from"}, a) {
						if i+1 < len(args) {
							i++
							if a == "-t" || a == "--target-directory" {
								destination = args[i]
							}
						}
						continue
					}
					if strings.HasPrefix(a, "--target-directory=") {
						destination = strings.TrimPrefix(a, "--target-directory=")
						continue
					}
					if program == "rsync" && slices.Contains([]string{"--delete", "--del", "--delete-before", "--delete-during", "--delete-delay", "--delete-after", "--delete-excluded", "--delete-missing-args"}, strings.SplitN(a, "=", 2)[0]) {
						prunesDestination = true
					}
					if !strings.HasPrefix(a, "-") {
						ops = append(ops, a)
					}
				}
				if destination != "" {
					ops = append(ops, destination)
				}
				if len(ops) >= 2 {
					for _, src := range ops[:len(ops)-1] {
						if safetyCredential.MatchString(src) {
							emitPath("credentials", ops[len(ops)-1], fmt.Sprintf("credential-destination:recursive=%v", safetyFlag(flags, "rRa", "--recursive")))
							break
						}
					}
					if prunesDestination {
						emitPath("boundary-delete", ops[len(ops)-1], "destination-pruning")
					}
				}
			case "sqlite3", "psql", "mysql":
				for _, a := range args {
					if safetySQLMutates(a, program) {
						db := "Database connection needs context"
						if program == "sqlite3" && len(ops) > 0 {
							db = ops[0]
						}
						if db == "Database connection needs context" || slices.Contains(flags, "-cmd") || slices.Contains(flags, "--cmd") {
							emit("database", "Database connection needs context", "mutation", false)
						} else if db != ":memory:" {
							emitPath("database", db, "mutation")
						}
						break
					}
				}
			case "git":
				verb, subargs, gitRoot, gitResolved := safetyGitCommand(args, cwd, call.Root)
				flags, ops = safetyParseOptions("git", subargs)
				if safetyFlag(flags, "h", "--help") || verb == "push" && safetyFlag(flags, "n", "--dry-run") {
					continue
				}
				pushOps := safetyPushOperands(subargs)
				emptySource, forcedRef := false, false
				for _, ref := range pushOps[min(1, len(pushOps)):] {
					emptySource = emptySource || strings.HasPrefix(ref, ":") && len(ref) > 1
					forcedRef = forcedRef || strings.HasPrefix(ref, "+")
				}
				selected := ops
				if verb == "push" {
					selected = pushOps
					if len(pushOps) == 0 || len(pushOps) < 2 && !slices.Contains(flags, "--mirror") && !slices.Contains(flags, "--all") && !slices.Contains(flags, "--tags") {
						gitResolved = false
					}
				}
				for _, value := range selected {
					gitResolved = gitResolved && safetyLiteralSelection(value)
				}
				for _, flag := range flags {
					if strings.HasPrefix(flag, "--force-with-lease=") {
						gitResolved = gitResolved && safetyLiteralSelection(strings.TrimPrefix(flag, "--force-with-lease="))
					}
				}
				subject := verb + " " + strings.Join(selected, " ")
				if verb == "push" && (safetyFlag(flags, "f", "--force") || safetyFlag(flags, "", "--force-with-lease") || safetyFlag(flags, "d", "--delete") || emptySource || slices.Contains(flags, "--mirror") || forcedRef) {
					breadth := "lease"
					if safetyFlag(flags, "f", "--force") || slices.Contains(flags, "--mirror") || forcedRef {
						breadth = "unconditional"
					}
					if safetyFlag(flags, "d", "--delete") || emptySource {
						breadth = "delete-ref"
						refs := append([]string{}, pushOps...)
						for i := 1; i < len(refs); i++ {
							refs[i] = strings.TrimPrefix(refs[i], ":")
						}
						subject = "push " + strings.Join(refs, " ")
					}
					emit("remote-history", gitRoot+" · "+subject, breadth, gitResolved)
				} else if verb == "push" && (slices.Contains(flags, "--tags") || strings.Contains(strings.Join(ops, " "), "refs/tags/")) {
					emit("publication", gitRoot+" · "+subject, "tags", gitResolved)
				} else if (verb == "reset" && slices.Contains(flags, "--hard")) || verb == "rebase" || (verb == "branch" && safetyFlag(flags, "dD", "--delete")) || (verb == "stash" && (slices.Contains(ops, "drop") || slices.Contains(ops, "clear"))) {
					emit("local-history", gitRoot+" · "+subject, "local-history", gitResolved)
				} else if verb == "clean" && !safetyFlag(flags, "n", "--dry-run") {
					selection, resolved := safetyCleanScope(subargs)
					force := 0
					for _, flag := range flags {
						if flag == "-f" || flag == "--force" {
							force++
						}
					}
					emit("delete", gitRoot+" · "+selection, fmt.Sprintf("untracked-pruning:dirs=%v:include-ignored=%v:only-ignored=%v:force=%d", safetyFlag(flags, "d", ""), safetyFlag(flags, "x", ""), safetyFlag(flags, "X", ""), min(force, 2)), gitResolved && resolved)
				}
			case "gh":
				ops, repo := safetyGH(args)
				if len(ops) >= 2 && ((ops[0] == "pr" && ops[1] == "merge") || (ops[0] == "release" && slices.Contains([]string{"create", "delete", "edit", "upload"}, ops[1]))) {
					resolved := safetyLiteralSelection(repo)
					if repo == "" {
						repo, resolved, _ = safetyPath(".", cwd, call.Root)
						if !resolved {
							repo = defaultString(nilIfEmpty(cwd), "Unresolved checkout")
						}
					}
					// Without a PR/release identifier, gh may infer a moving
					// branch. A known cwd alone does not resolve that target.
					resolved = resolved && len(ops) > 2
					for _, value := range ops {
						resolved = resolved && safetyLiteralSelection(value)
					}
					emit("publication", repo+" · "+strings.Join(ops, " "), "publication", resolved)
				}
			case "gsutil", "aws", "gcloud":
				if slices.Contains(ops, "rm") || slices.Contains(ops, "delete") {
					for _, a := range ops {
						if strings.HasPrefix(a, "gs://") || strings.HasPrefix(a, "s3://") {
							emit("cloud-delete", safetyRedact(a), fmt.Sprintf("cloud-selection:recursive=%v", safetyFlag(flags, "rR", "--recursive")), !strings.ContainsAny(a, "$`"))
						}
					}
				}
			case "pkill", "killall":
				emit("process", cwd+" · "+strings.Join(ops, " "), "name-selector", cwd != "")
			case "chmod", "chown":
				if len(ops) >= 2 {
					for _, a := range ops[1:] {
						_, resolved, inside := safetyPath(a, cwd, call.Root)
						if !inside || !resolved {
							emitPath("permissions", a, fmt.Sprintf("%s:recursive=%v", ops[0], safetyFlag(flags, "R", "--recursive")))
						}
					}
				}
			case "hdiutil":
				if len(ops) >= 1 && slices.Contains([]string{"create", "attach", "detach", "eject", "resize", "convert"}, ops[0]) {
					emit("disk-image", strings.Join(ops, " "), "disk-image", true)
				}
			case "diskutil", "launchctl", "systemctl", "installer":
				if len(ops) > 0 && !slices.Contains([]string{"list", "info", "status", "show", "print"}, ops[0]) {
					emit("system", strings.Join(ops, " "), "system-operation", true)
				}
			case "curl":
				method, urls := safetyCurl(args)
				if slices.Contains([]string{"POST", "PUT", "PATCH", "DELETE"}, method) {
					for _, a := range urls {
						if strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://") {
							emit("api", safetyRedact(a), method, !strings.ContainsAny(a, "$`"))
						}
					}
				}
			}
		}
	}
	return result
}

// refreshSafety reads full archived tool inputs in one snapshot, excludes
// mirrored workspaces, and publishes all supported cases atomically. A single
// event qualifies. It shares Findings' daily scheduling, not its savings gate.
func (c *Catalog) refreshSafety(ctx context.Context) error {
	read, err := c.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer read.Rollback()
	disabled, err := safetyDisabledRules(ctx, read)
	if err != nil {
		return err
	}
	rows, err := read.QueryContext(ctx, `SELECT t.id,t.workspace_id,t.conversation_id,COALESCE(t.call_message_id,''),COALESCE(t.result_message_id,''),COALESCE(t.started_at,''),t.status,t.exit_code,
 COALESCE(w.repository_id,''),COALESCE(r.display_name,''),COALESCE(v.origin_host_id,''),COALESCE(h.label,''),COALESCE(w.location,''),COALESCE(t.command,''),COALESCE(m.text,'')
 FROM tool_calls t JOIN workspaces w ON w.id=t.workspace_id JOIN conversations v ON v.id=t.conversation_id
 LEFT JOIN repositories r ON r.id=w.repository_id LEFT JOIN hosts h ON h.id=v.origin_host_id LEFT JOIN messages m ON m.id=t.call_message_id
 WHERE t.started_at>=? AND COALESCE(t.command,'')<>'' AND t.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)
 ORDER BY t.started_at,t.id`, formatTime(c.clock().AddDate(0, 0, -safetyWindowDays)))
	if err != nil {
		return err
	}
	cases := map[string]*safetyCandidate{}
	count := 0
	for rows.Next() {
		var call safetyCall
		if err = rows.Scan(&call.ID, &call.Workspace, &call.Conversation, &call.Message, &call.ResultMessage, &call.At, &call.Status, &call.Exit, &call.Repository, &call.RepositoryLabel, &call.Host, &call.HostLabel, &call.Root, &call.Command, &call.Input); err != nil {
			rows.Close()
			return err
		}
		count++
		for _, candidate := range selectSafetyCall(call) {
			if disabled[candidate.Rule] {
				continue
			}
			if previous := cases[candidate.ID]; previous != nil {
				previous.Completion = previous.Completion || candidate.Completion
				if previous.LastCall != call.ID {
					previous.Calls++
					previous.LastCall = call.ID
				}
				previous.Last = call.At
				if candidate.Completion && previous.Strongest == nil {
					entry := candidate.Evidence[0]
					previous.Strongest = &entry
				}
				for _, entry := range candidate.Evidence {
					if !slices.ContainsFunc(previous.Evidence, func(e safetyEvidence) bool { return e.ID == entry.ID }) {
						if len(previous.Evidence) >= 20 {
							previous.Evidence = append(previous.Evidence[:1], previous.Evidence[2:]...)
						}
						previous.Evidence = append(previous.Evidence, entry)
					}
				}
			} else {
				value := candidate
				value.Calls, value.First, value.Last, value.LastCall = 1, call.At, call.At, call.ID
				if candidate.Completion {
					entry := candidate.Evidence[0]
					value.Strongest = &entry
				}
				cases[value.ID] = &value
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err = read.Commit(); err != nil {
		return err
	}
	ids := make([]string, 0, len(cases))
	for id := range cases {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return c.writeTransaction(ctx, "safety-discovery", func(tx *sql.Tx) error {
		for _, id := range ids {
			item := cases[id]
			// Keep bounded evidence, including an example that establishes the
			// stronger execution status. Counts are distinct call IDs.
			first, last := item.First, item.Last
			evidence := item.Evidence
			if len(evidence) > 20 {
				evidence = append(append([]safetyEvidence{}, evidence[:1]...), evidence[len(evidence)-19:]...)
			}
			// Preserve evidence supporting the sticky completion level, even
			// after that example rolls out of the discovery window.
			strongest := safetyEvidence{}
			if item.Strongest != nil {
				strongest = *item.Strongest
			}
			for _, entry := range item.Evidence {
				if strongest.ID == "" && strings.HasPrefix(entry.Outcome, "Single command") {
					strongest = entry
					break
				}
			}
			if strongest.ID == "" {
				var prior string
				if err := tx.QueryRow("SELECT evidence_json FROM safety_cases WHERE id=?", id).Scan(&prior); err != nil && err != sql.ErrNoRows {
					return err
				}
				var previous []safetyEvidence
				if prior != "" {
					if err := json.Unmarshal([]byte(prior), &previous); err != nil {
						return err
					}
				}
				for _, entry := range previous {
					if strongest.ID == "" && strings.HasPrefix(entry.Outcome, "Single command") {
						strongest = entry
						break
					}
				}
			}
			if strongest.ID != "" && !slices.ContainsFunc(evidence, func(e safetyEvidence) bool { return e.ID == strongest.ID }) {
				if len(evidence) >= 20 {
					evidence = evidence[1:]
				}
				evidence = append([]safetyEvidence{strongest}, evidence...)
			}
			_, err := tx.Exec(`INSERT INTO safety_cases(id,rule,target,target_label,subject,repository_id,first_seen_at,last_seen_at,calls,completion,evidence_json,version)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET target_label=excluded.target_label,last_seen_at=MAX(safety_cases.last_seen_at,excluded.last_seen_at),
 calls=excluded.calls,completion=MAX(safety_cases.completion,excluded.completion),evidence_json=excluded.evidence_json,version=excluded.version`, id, item.Rule, item.Target, item.Label, item.Subject, item.Repository, first, last, item.Calls, item.Completion, jsonText(evidence), safetyVersion)
			if err != nil {
				return err
			}
		}
		for key, value := range map[string]string{"safety_built_at": formatTime(c.clock()), "safety_version": safetyVersion, "safety_scanned_calls": fmt.Sprint(count)} {
			if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
				return err
			}
		}
		return nil
	})
}
