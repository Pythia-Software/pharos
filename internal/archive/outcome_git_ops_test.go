package archive

import (
	"fmt"
	"testing"
)

func TestReadGitOps(t *testing.T) {
	resolve := func(directory string) string {
		return map[string]string{"/src/app": "repo-app", "/src/lib": "repo-lib"}[directory]
	}
	read := func(command, result string) string {
		t.Helper()
		ops := readGitOps(outcomeCall{Command: command, Result: result, StartedAt: "2026-09-01T00:00:00Z", EndedAt: "2026-09-01T00:00:03Z"}, resolve)
		text := ""
		for _, op := range ops {
			text += fmt.Sprintf("%s %s %v %v %d %s; ", op.Kind, op.Repository, op.Subjects, op.Hashes, op.PullRequest, op.Forge)
		}
		return text
	}
	subject := subjectFingerprint
	for _, test := range []struct{ command, result, want string }{
		// The first -m is the subject; a later -m, or another program's, is not.
		{`cd /src/app && git add -A && git commit -m "Fix: a thing" -m "More" && python -m pytest`, "",
			"commit repo-app [" + subject("Fix: a thing") + "] [] 0 ; "},
		// A here document the command reads, with its subject over two lines,
		// and the directory -C names, quoted.
		{"git -C '/src/lib' commit -q -F - <<'EOF'\nTwo line\nsubject\n\nThe body.\nEOF\ngit push", "",
			"commit repo-lib [" + subject("Two line subject") + "] [] 0 ; "},
		{"git commit -m \"$(cat <<'EOF'\nFrom cat\n\nThe body.\nEOF\n)\"", "",
			"commit  [" + subject("From cat") + "] [] 0 ; "},
		// A script's here document is not the message, nor is a variable.
		{"python3 - <<'EOF'\nprint('edit')\nEOF\ngit commit -qa -F .git/MESSAGE", "", "commit  [] [] 0 ; "},
		{`git commit -qm "$MESSAGE"`, "", "commit  [] [] 0 ; "},
		// Only subcommands that make commits, after git's own options.
		{"git merge-base a b; git -c core.editor=true --no-pager rebase main; git log --oneline", "", "rebase  [] [] 0 ; "},
		// The last command that can print a hash holds what was printed.
		{"git commit -qm one && git cherry-pick abc", "[main 1234567] one\n[main 89abcde] two\n",
			"commit  [" + subject("one") + "] [] 0 ; cherry-pick  [] [1234567 89abcde] 0 ; "},
		// Only git commit takes a message: revert -m names a parent.
		{"git revert --no-edit -m 1 abc", "", "revert  [] [] 0 ; "},
		{"gh pr create --fill", "https://github.com/Acme/App/pull/7\n", "pull_request  [] [] 7 acme/app; "},
		{`echo "git commit -m x"`, "", ""},
	} {
		if got := read(test.command, test.result); got != test.want {
			t.Errorf("%q:\n got %s\nwant %s", test.command, got, test.want)
		}
	}
}

func TestReadShellWord(t *testing.T) {
	for text, want := range map[string]string{
		`"a \"b\" \$c" rest`:  `a "b" $c`,
		`'it'"'"'s' rest`:     `it's`,
		`$'line\nnext';`:      "line\nnext",
		`bare;next`:           "bare",
		`mixed"quo ted"'x' y`: "mixedquo tedx",
	} {
		if got, ok := readShellWord(text); !ok || got != want {
			t.Errorf("%q: %q %v, want %q", text, got, ok, want)
		}
	}
	if _, ok := readShellWord(`"unterminated`); ok {
		t.Error("an unterminated quote read as a word")
	}
}
