package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A session's git activity (docs/projects-and-outcomes.md): the git commands
// its shell calls ran that make commits, and the pull requests it opened, read
// from the command line and its output as any harness with a shell tool
// records them. Local Git (outcome_git.go) links commits to the session by
// them: a commit made while the call ran, under a subject the command wrote,
// is the session's whatever tool edited the files. Only facts are kept: the
// kind, the call's times, the repository it ran in, the hashes it printed and
// a fingerprint of each commit subject it wrote, never the message.

// gitOp is one such command.
type gitOp struct {
	Kind        string   `json:"kind"`
	At          string   `json:"at"`
	Until       string   `json:"until,omitempty"`
	Repository  string   `json:"repository,omitempty"`
	Hashes      []string `json:"hashes,omitempty"`
	Subjects    []string `json:"subjects,omitempty"`
	PullRequest int      `json:"pull_request,omitempty"`
	Forge       string   `json:"forge,omitempty"`
	transcriptPlace
}

var (
	// A git invocation that can make commits, after any global options; the
	// subcommand ends at a separator, so merge-base or am-something is not one.
	gitCommitInvocation = regexp.MustCompile(`(?:^|[\s;&|(` + "`" + `])git((?:\s+(?:-C\s+(?:"[^"]*"|'[^']*'|[^\s;&|)]+)|-c\s+\S+|--[a-z][a-z-]*(?:=\S+)?|-[pP]))*)\s+(commit|merge|rebase|cherry-pick|revert|am|pull)(?:[\s;&|)` + "`" + `]|$)`)
	gitDirectoryOption  = regexp.MustCompile(`-C\s+("[^"]*"|'[^']*'|[^\s;&|)]+)`)
	changeDirectory     = regexp.MustCompile(`(?:^|[\s;&|(])cd\s+("[^"]*"|'[^']*'|[^\s;&|)]+)`)
	ghPullRequestCreate = regexp.MustCompile(`(?:^|[\s;&|(])gh\s+pr\s+create\b`)
	pullRequestURL      = regexp.MustCompile(`https://github\.com/([\w.-]+/[\w.-]+)/pull/(\d+)`)
	// -m, --message and combined short flags ending in m (-am, -qm).
	messageFlag  = regexp.MustCompile(`(?:^|\s)(?:-[a-zA-Z]*m|--message)(?:=|\s+)`)
	heredocStart = regexp.MustCompile(`<<-?\s*(?:'(\w+)'|"(\w+)"|\\?(\w+))[^\n]*\n`)
)

// readGitOps reads the git commands one shell call ran. resolve gives the
// repository a directory the command named is in, if it names one. Only a
// git or gh that starts a command counts: one in quoted text, a message or a
// pull request body say, or in a here document runs nothing.
func readGitOps(call outcomeCall, resolve func(directory string) string) []gitOp {
	command := call.Command
	starts := commandStarts(command)
	ops := []gitOp{}
	for _, match := range gitCommitInvocation.FindAllStringSubmatchIndex(command, -1) {
		if !starts[match[0]+strings.Index(command[match[0]:], "git")] {
			continue
		}
		op := gitOp{Kind: command[match[4]:match[5]], At: call.StartedAt, Until: call.EndedAt, transcriptPlace: call.Place}
		directory := ""
		if option := gitDirectoryOption.FindStringSubmatch(command[match[2]:match[3]]); option != nil {
			directory = unquoteWord(option[1])
		} else {
			directory = shellDirectory(command, match[0]+1)
		}
		if directory != "" && resolve != nil {
			op.Repository = resolve(directory)
		}
		// Only git commit takes a message: revert -m names a parent.
		if op.Kind == "commit" {
			op.Subjects = commitSubjects(command, match[5])
		}
		ops = append(ops, op)
	}
	// The output can't say which invocation printed a hash: the last that
	// prints one holds them all.
	for index := len(ops) - 1; index >= 0; index-- {
		if kind := ops[index].Kind; kind == "commit" || kind == "cherry-pick" || kind == "revert" {
			for _, printed := range commitLine.FindAllStringSubmatch(call.Result, -1) {
				if !slices.Contains(ops[index].Hashes, printed[2]) {
					ops[index].Hashes = append(ops[index].Hashes, printed[2])
				}
			}
			break
		}
	}
	if slices.ContainsFunc(ghPullRequestCreate.FindAllStringIndex(command, -1), func(match []int) bool {
		return starts[match[0]+strings.Index(command[match[0]:], "gh")]
	}) {
		for _, found := range pullRequestURL.FindAllStringSubmatch(call.Result, -1) {
			number, _ := strconv.Atoi(found[2])
			ops = append(ops, gitOp{Kind: "pull_request", At: call.StartedAt, Until: call.EndedAt, PullRequest: number, Forge: strings.ToLower(found[1]), transcriptPlace: call.Place})
		}
	}
	return ops
}

// shellDirectory is the directory the last cd before end moved the shell to.
// A cd in a subshell, ( … ) or $( … ), closed before end moved only that
// subshell. When the quotes before end do not balance, which subshell a cd
// was in is unknown, and it names none.
func shellDirectory(command string, end int) string {
	moves := changeDirectory.FindAllStringSubmatchIndex(command[:end], -1)
	// A subshell opened at an index, and the quote it opened inside.
	type subshell struct {
		at    int
		quote byte
	}
	// scopes[i] are where the subshells open at moves[i] opened.
	scopes := make([][]int, len(moves))
	open, next := []subshell{}, 0
	opened := func() []int {
		at := make([]int, len(open))
		for index, shell := range open {
			at[index] = shell.at
		}
		return at
	}
	quote := byte(0)
	for index := 0; index < end; index++ {
		for next < len(moves) && moves[next][2] == index {
			scopes[next] = opened()
			next++
		}
		switch char := command[index]; {
		case quote == '\'':
			if char == '\'' {
				quote = 0
			}
		case char == '\\':
			index++
		case char == '$' && index+1 < end && command[index+1] == '(':
			open, quote = append(open, subshell{index, quote}), 0
			index++
		case quote == '"':
			if char == '"' {
				quote = 0
			}
		case char == '\'' || char == '"':
			quote = char
		case char == '(':
			open = append(open, subshell{index, 0})
		case char == ')' && len(open) > 0:
			open, quote = open[:len(open)-1], open[len(open)-1].quote
		}
	}
	if quote != 0 {
		return ""
	}
	final := opened()
	for index := len(moves) - 1; index >= 0; index-- {
		if scope := scopes[index]; len(scope) <= len(final) && slices.Equal(scope, final[:len(scope)]) {
			return unquoteWord(command[moves[index][2]:moves[index][3]])
		}
	}
	return ""
}

// commandPrefixes are words after which the next word still starts a
// command: they open a compound command's body or run the command after
// them.
var commandPrefixes = []string{"!", "{", "if", "then", "else", "elif", "while", "until", "do", "time", "env", "command", "exec", "nohup", "sudo"}

var (
	shellAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\+?=`)
	heredocOperator = regexp.MustCompile(`^<<-?[ \t]*(?:'([^'\n]*)'|"([^"\n]*)"|\\?([\w.-]+))`)
)

// commandStarts are the indexes in a shell command line where a simple
// command's name begins: at the start, after a separator (; & | ( or a line
// break) and inside a $( … ) or ` … ` substitution, past any assignments and
// commandPrefixes. Quoted text outside a substitution, comments and here
// documents' bodies start none.
func commandStarts(command string) map[int]bool {
	starts := map[int]bool{}
	// What each open $( … ) returns to: the quote it opened inside, and
	// whether it is an assignment's value.
	type substitution struct {
		quote     byte
		assigning bool
	}
	open := []substitution{}
	quote, backtick := byte(0), false
	// start: the next word is a command's name; assigning: the current word
	// is an assignment, after which one still is.
	start, assigning := true, false
	// The delimiters of the here documents opened on this line.
	delimiters := []string{}
	for index := 0; index < len(command); index++ {
		char := command[index]
		substitutionOpens := char == '$' && index+1 < len(command) && command[index+1] == '('
		switch {
		case quote == '\'':
			if char == '\'' {
				quote = 0
			}
		case quote == '"' && !substitutionOpens:
			if char == '\\' {
				index++
			} else if char == '"' {
				quote = 0
			}
		case substitutionOpens:
			open = append(open, substitution{quote, assigning})
			quote, start, assigning = 0, true, false
			index++
		case char == ')' && len(open) > 0:
			quote, assigning = open[len(open)-1].quote, open[len(open)-1].assigning
			open, start = open[:len(open)-1], false
		case char == '\n':
			if len(delimiters) > 0 {
				index, delimiters = hereDocumentsEnd(command, index+1, delimiters), nil
			}
			start, assigning = true, false
		case char == ' ' || char == '\t':
			if assigning {
				start, assigning = true, false
			}
		case char == '#' && (index == 0 || strings.IndexByte(" \t\n;&|(", command[index-1]) >= 0):
			for index+1 < len(command) && command[index+1] != '\n' {
				index++
			}
		case char == '\\':
			// A line continuation joins the lines; any other escape is a word.
			start = start && index+1 < len(command) && command[index+1] == '\n'
			index++
		case char == '\'' || char == '"':
			quote, start = char, false
		case char == '`':
			backtick = !backtick
			start, assigning = backtick, false
		case strings.HasPrefix(command[index:], "<<<"):
			index += 2
		case char == '<' && heredocOperator.MatchString(command[index:]):
			operator := heredocOperator.FindStringSubmatch(command[index:])
			delimiters = append(delimiters, operator[1]+operator[2]+operator[3])
			index += len(operator[0]) - 1
		case char == '&' && index > 0 && strings.IndexByte("<>", command[index-1]) >= 0:
			// A redirection, as in 2>&1.
		case strings.IndexByte(";&|(", char) >= 0:
			start, assigning = true, false
		case char == ')':
			start = false
		case start:
			rest := command[index:]
			if assignment := shellAssignment.FindString(rest); assignment != "" {
				start, assigning = false, true
				index += len(assignment) - 1
				continue
			}
			word := rest
			if end := strings.IndexAny(rest, " \t\n;&|()<>"); end >= 0 {
				word = rest[:end]
			}
			if slices.Contains(commandPrefixes, word) {
				index += len(word) - 1
				continue
			}
			starts[index], start = true, false
		}
	}
	return starts
}

// hereDocumentsEnd is where the bodies of here documents beginning at at
// end: the line break after the last one's delimiter line, else the end.
func hereDocumentsEnd(command string, at int, delimiters []string) int {
	for number, delimiter := range delimiters {
		for {
			end := strings.IndexByte(command[at:], '\n')
			if end < 0 {
				return len(command)
			}
			line := command[at : at+end]
			at += end + 1
			if strings.TrimSpace(line) == delimiter {
				if number == len(delimiters)-1 {
					return at - 1
				}
				break
			}
		}
	}
	return len(command)
}

// commitSubjects fingerprints the subject a git invocation writes, from its
// arguments, which begin at start: its first -m (git makes later ones
// paragraphs of the body), and the first paragraph of a here document the
// arguments open, which is how -F - <<EOF and -m "$(cat <<EOF …)" pass one. A
// here document elsewhere in the command, a script say, is not the message.
func commitSubjects(command string, start int) []string {
	subjects := []string{}
	add := func(message string) {
		if subject := messageSubject(message); subject != "" {
			if fingerprint := subjectFingerprint(subject); !slices.Contains(subjects, fingerprint) {
				subjects = append(subjects, fingerprint)
			}
		}
	}
	end := start + commandEnd(command[start:])
	arguments := command[start:end]
	// A message from a variable or a substitution is not the text the commit
	// holds; one from a here document is read below.
	if flag := messageFlag.FindStringIndex(arguments); flag != nil {
		if value, ok := readShellWord(arguments[flag[1]:]); ok && !strings.Contains(value, "$") {
			add(value)
		}
	}
	for _, opened := range heredocStart.FindAllStringSubmatchIndex(command, -1) {
		if opened[0] < start || opened[0] >= end {
			continue
		}
		delimiter := ""
		for group := 2; group < len(opened); group += 2 {
			if opened[group] >= 0 {
				delimiter = command[opened[group]:opened[group+1]]
			}
		}
		lines := strings.Split(command[opened[1]:], "\n")
		for index, line := range lines {
			if strings.TrimSpace(line) == delimiter {
				add(strings.Join(lines[:index], "\n"))
				break
			}
		}
	}
	return subjects
}

// commandEnd is where the command at the start of text ends: at the first
// ;, &, |, ) or line break outside quotes.
func commandEnd(text string) int {
	quote := byte(0)
	for index := 0; index < len(text); index++ {
		switch char := text[index]; {
		case quote == '\'':
			if char == '\'' {
				quote = 0
			}
		case quote == '"':
			if char == '\\' {
				index++
			} else if char == '"' {
				quote = 0
			}
		case char == '\\':
			index++
		case char == '\'' || char == '"':
			quote = char
		case strings.IndexByte(";&|)\n", char) >= 0:
			return index
		}
	}
	return len(text)
}

// messageSubject is a message's subject as git reads it: its first
// paragraph, joined into one line.
func messageSubject(message string) string {
	lines := []string{}
	for _, line := range strings.Split(message, "\n") {
		if strings.TrimSpace(line) == "" {
			if len(lines) > 0 {
				break
			}
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
}

// subjectFingerprint is the first 6 bytes of the SHA-256 of a subject, as hex:
// enough to tell a session's commits apart, and not the subject.
func subjectFingerprint(subject string) string {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(subject), " ")))
	return hex.EncodeToString(sum[:6])
}

// readShellWord reads the shell word at the start of text: double-quoted (with
// backslash escapes), single-quoted, $'…' or bare.
func readShellWord(text string) (string, bool) {
	var word strings.Builder
	quote := byte(0)
	for index := 0; index < len(text); index++ {
		char := text[index]
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
			} else if char == '\\' && index+1 < len(text) && strings.IndexByte("\"\\$`\n", text[index+1]) >= 0 {
				index++
				if text[index] != '\n' {
					word.WriteByte(text[index])
				}
			} else {
				word.WriteByte(char)
			}
		case quote == '$':
			if char == '\'' {
				quote = 0
			} else if char == '\\' && index+1 < len(text) {
				index++
				switch text[index] {
				case 'n':
					word.WriteByte('\n')
				case 't':
					word.WriteByte('\t')
				default:
					word.WriteByte(text[index])
				}
			} else {
				word.WriteByte(char)
			}
		case char == '\'' || char == '"':
			quote = char
		case char == '$' && index+1 < len(text) && text[index+1] == '\'':
			quote, index = '$', index+1
		case char == '\\' && index+1 < len(text):
			index++
			word.WriteByte(text[index])
		case strings.IndexByte(" \t\n;&|)", char) >= 0:
			return word.String(), word.Len() > 0
		default:
			word.WriteByte(char)
		}
	}
	return word.String(), word.Len() > 0 && quote == 0
}

func unquoteWord(text string) string {
	word, _ := readShellWord(text)
	return word
}
