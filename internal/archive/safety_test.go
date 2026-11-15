package archive

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

func TestSafetySelection(t *testing.T) {
	base := safetyCall{ID: "call", Workspace: "ws", Conversation: "conv", Message: "input", ResultMessage: "output", Repository: "repo", RepositoryLabel: "alpha", Root: "/Users/a/repo", Status: "ok", Exit: sql.NullInt64{Int64: 0, Valid: true}}
	for _, test := range []struct{ command, rule, subject string }{
		{"rm -rf /Users/a/archive", "boundary-delete", "/Users/a/archive"},
		{"rm -rf .git", "delete", "/Users/a/repo/.git"},
		{"rm -rf \"$DIR/\"", "boundary-delete", ""},
		{"rm -rf /Users/a/repo", "delete", "/Users/a/repo"},
		{"cp -R ~/.config/gcloud .build/gcloud", "credentials", "/Users/a/repo/.build/gcloud"},
		{"cp -t .build/gcloud ~/.config/gcloud/credentials.db", "credentials", "/Users/a/repo/.build/gcloud"},
		{`sqlite3 /Users/a/data.db "DELETE FROM steps WHERE idx=321;"`, "database", "/Users/a/data.db"},
		{`sqlite3 /Users/a/data.db "BEGIN; DELETE FROM steps; COMMIT;"`, "database", "/Users/a/data.db"},
		{`sqlite3 /Users/a/data.db "SELECT 1; DROP TABLE steps;"`, "database", "/Users/a/data.db"},
		{`psql -h db.example.com -c "BEGIN; UPDATE steps SET value=1; COMMIT;"`, "database", ""},
		{"git push --force-with-lease origin main", "remote-history", ""},
		{"git push --force origin HEAD", "remote-history", ""},
		{"git push origin :old-branch", "remote-history", "/Users/a/repo · push origin old-branch"},
		{"git -C /other push origin :refs/heads/old", "remote-history", "/other · push origin refs/heads/old"},
		{"git stash clear", "local-history", ""},
		{"git stash drop", "local-history", ""},
		{"git branch -D main", "local-history", ""},
		{"git clean -fdx", "delete", ""},
		{"gh -Rowner/repo release delete v1", "publication", "owner/repo · release delete v1"},
		{"gh --repo=owner/repo release upload v1 app.zip --clobber", "publication", "owner/repo · release upload v1 app.zip"},
		{"rsync --delete-before source/ /outside/", "boundary-delete", "/outside"},
		{"rsync --delete-after source/ /outside/", "boundary-delete", "/outside"},
		{"rsync --delete-excluded source/ /outside/", "boundary-delete", "/outside"},
		{"rsync --delete-during source/ /outside/", "boundary-delete", "/outside"},
		{"rsync --delete-delay source/ /outside/", "boundary-delete", "/outside"},
		{"rsync --del source/ /outside/", "boundary-delete", "/outside"},
		{"rsync --delete-missing-args source/ /outside/", "boundary-delete", "/outside"},
		{"gsutil -m rm -r gs://example/prefix", "cloud-delete", "gs://example/prefix"},
		{"pkill -f pharos", "process", ""},
		{"pkill -10 worker", "process", ""},
		{"killall -10 worker", "process", ""},
		{"chmod 777 /Users/a/shared", "permissions", "/Users/a/shared"},
		{"chmod -R o+w data", "permissions", "/Users/a/repo/data"},
		{"chown -R nobody /srv/app", "permissions", "/srv/app"},
		{"hdiutil detach /Volumes/Test", "disk-image", ""},
		{"diskutil eraseDisk APFS scratch /dev/disk7", "system", ""},
		{"curl -X DELETE https://example.com/api/object", "api", "https://example.com/api/object"},
		{"curl -XDELETE https://example.com/api/object", "api", "https://example.com/api/object"},
		{"curl -sSfXDELETE https://example.com/api/object", "api", "https://example.com/api/object"},
		{"curl --request=DELETE https://example.com/api/object", "api", "https://example.com/api/object"},
		{`curl --json '{}' https://example.com/api/object`, "api", "https://example.com/api/object"},
		{`curl --json='{}' --url=https://example.com/api/object`, "api", "https://example.com/api/object"},
		{`curl -dfoo https://example.com/api/object`, "api", "https://example.com/api/object"},
		{`curl -Fa=b https://example.com/api/object`, "api", "https://example.com/api/object"},
		{`curl -T file --url https://example.com/api/object`, "api", "https://example.com/api/object"},
		{`curl -XPOST -d https://body.invalid -H https://header.invalid https://example.com/api/object`, "api", "https://example.com/api/object"},
		{`curl -X DELETE -d '--help' https://example.com/api/object`, "api", "https://example.com/api/object"},
		{`curl -XDELETE --data '--dry-run' https://example.com/api/object`, "api", "https://example.com/api/object"},
		{`curl --json '--dryrun' https://example.com/api/object`, "api", "https://example.com/api/object"},
		{`rm -rf -- /outside/--help`, "boundary-delete", "/outside/--help"},
		{`rm -rf -- /outside/--dry-run`, "boundary-delete", "/outside/--dry-run"},
		{`rsync --delete --exclude '--dry-run' source/ /outside/`, "boundary-delete", "/outside"},
		{`rsync --delete -e 'ssh -n' source/ /outside/`, "boundary-delete", "/outside"},
		{`gh release delete v1 --cleanup-tag --repo '--help'`, "publication", ""},
		{`git clean -fdx -e '--help'`, "delete", ""},
		{`git clean -fdx -- --help`, "delete", ""},
		{`git -C --help push --force origin HEAD`, "remote-history", ""},
	} {
		t.Run(test.command, func(t *testing.T) {
			call := base
			call.Command = test.command
			items := selectSafetyCall(call)
			if len(items) != 1 || items[0].Rule != test.rule {
				t.Fatalf("selected %+v", items)
			}
			if test.subject != "" && items[0].Subject != test.subject {
				t.Fatalf("subject %q", items[0].Subject)
			}
			if !items[0].Completion {
				t.Fatal("single call should retain reported completion, without claiming effects")
			}
		})
	}
	// Only an assignment the deletion sees, and only mktemp's output, makes
	// its path temporary.
	for _, command := range []string{`d=/Users/a/archive; rm -rf "$d"; d=$(mktemp -d)`, `d=/Users/a/archive; (d=$(mktemp -d)); rm -rf "$d"`, `d=$(mktemp -d); d=/Users/a/archive; rm -rf "$d"`} {
		call := base
		call.Command = command
		if items := selectSafetyCall(call); len(items) != 1 || items[0].Rule != "boundary-delete" || items[0].Subject != "/Users/a/archive" {
			t.Errorf("%q selected %+v", command, items)
		}
	}
	call := base
	call.Command = `d=/Users/a/mktemp-notes; rm -rf "$d"`
	if items := selectSafetyCall(call); len(items) != 1 || items[0].Subject != "/Users/a/mktemp-notes" {
		t.Errorf("a path naming mktemp is not temporary: %+v", items)
	}
	for _, command := range []string{
		"echo 'rm -rf /Users/a/archive'", "rg 'rm -rf' docs", "cat <<'EOF'\nrm -rf /Users/a/archive\nEOF",
		"gcloud builds submit --help", "git clean -nd", "git push origin HEAD", "git push --dry-run --force origin HEAD", "git push -n --force origin HEAD", "rsync -an --delete source/ /outside", "pkill -0 agent", "curl https://example.com", "chmod 600 local.txt",
		"rsync -d source/ /outside/", "rsync --dirs source/ /outside/", "rsync --exclude --delete-before source/ /outside/", "rsync -an --delete-after source/ /outside/",
		"curl -G -d foo https://example.com", "curl -dfoo --get https://example.com", "curl -X GET --json '{}' https://example.com", "curl --json '{}' --request=GET https://example.com", "curl -I -d foo https://example.com",
		"curl -d-XDELETE -G https://example.com", "git push -o :old-branch origin HEAD", "gh -R owner/repo pr view 17", "gh --repo owner/repo release view v1",
		"curl --help -XDELETE https://example.com", "curl -XDELETE --help https://example.com", "git clean -fd --help", "git clean -fd --dry-run", "gh pr merge 17 --help",
		"curl -h -XDELETE https://example.com", "gh pr merge 17 -h", `sqlite3 -help /Users/a/data.db 'DROP TABLE steps;'`,
		"pkill --signal=0 worker", "pkill --signal 0 worker", "killall --signal=0 worker",
		`sqlite3 /Users/a/data.db "SELECT 'hello; DELETE FROM steps';"`,
		// An agent's own scratch, build output and work in progress.
		"rm -rf build", "rm -rf .build/cache", "rm -rf /tmp/pharos-x", "rm -rf \"$TMPDIR/run\"", "d=$(mktemp -d) && rm -rf \"$d\"", "rm -rf node_modules dist",
		"rm -rf /Users/a/other/.context/wt", "rm -f /Users/a/repo/notes.md", "rm -rf src/old", "rm -rf '$DIR'", "rm -rf -- --help",
		`sqlite3 /tmp/probe.db "DROP TABLE x;"`, `sqlite3 .build/catalog.sqlite3 "DELETE FROM steps;"`, `psql -c "UPDATE steps SET value=1;"`, `psql -h "$PGHOST" -c "DELETE FROM steps;"`, `psql -h localhost -c "DELETE FROM steps;"`,
		"git push --force-with-lease origin HEAD", "git push --force-with-lease origin feature/x", "git push origin refs/tags/v1", "git push --tags origin", "git reset --hard HEAD", "git rebase -i main", "git branch -D tmp-backup", "git stash drop \"$ref\"", "git clean -fd",
		"gh pr merge 17 --squash", "gh release create v1 --notes x", "gh release edit v1 --notes x", "gh release upload v1 app.zip",
		"chmod 600 /Users/a/.config/secret", "chmod +x script.sh", "chmod 755 /usr/local/bin/tool", "chown a data",
		"pkill -f 'archive.test.*TestSafety'", "pkill -f /Users/a/repo/.build/server", "pkill -f 'vite --port 5173'", "pkill -f bkog2am5l", "pkill -f TestToolOutputIndexMigration",
		"hdiutil detach /tmp/test.dmg", "hdiutil attach image.dmg", "diskutil list", "diskutil info disk2",
		"curl -X POST http://127.0.0.1:8765/api/x", "curl -X DELETE http://localhost/api/x", "curl -X POST https://example.com/api/search -d q=x",
	} {
		base.Command = command
		if items := selectSafetyCall(base); len(items) != 0 {
			t.Errorf("read-only/dry-run %q selected %+v", command, items)
		}
	}
	t.Run("SQL statement boundaries", func(t *testing.T) {
		for _, test := range []struct {
			sql      string
			mutation bool
		}{
			{"BEGIN; /* operation */ DELETE/* comment */FROM steps; COMMIT;", true},
			{"SELECT 1; -- note\n DROP TABLE steps;", true},
			{"/* outer /* nested */ comment */ BEGIN; UPDATE steps SET x=1;", true},
			{"SELECT 'not a ; DELETE FROM steps';", false},
			{"SELECT 'it''s a ; DROP TABLE steps';", false},
			{`SELECT "DELETE FROM steps;" FROM [DROP TABLE steps];`, false},
			{"SELECT $quote$; DROP TABLE steps;$quote$;", false},
			{"SELECT $$; DROP TABLE steps;$$; DELETE FROM real_steps;", true},
			{"-- DELETE FROM steps;\nSELECT 1; /* DROP TABLE steps; */", false},
			{"SELECT 1; EXPLAIN DELETE FROM steps;", false},
			{"WITH\nremoved AS (DELETE FROM steps RETURNING *) SELECT * FROM removed;", true},
			{"WITH q AS (SELECT 'DROP TABLE steps') SELECT * FROM q;", false},
		} {
			if got := safetySQLMutates(test.sql, "sqlite3"); got != test.mutation {
				t.Errorf("SQL %q: mutation=%v", test.sql, got)
			}
		}
		if safetySQLMutates("SELECT 1; # DROP TABLE steps;", "mysql") {
			t.Fatal("a MySQL line comment is not a mutation")
		}
		if !safetySQLMutates(`SELECT '\'; DELETE FROM steps;`, "sqlite3") {
			t.Fatal("SQLite backslashes do not escape SQL quotes")
		}
	})
}

func TestSafetyIdentityAndExecution(t *testing.T) {
	call := safetyCall{ID: "a", Repository: "repo", Root: "/repo", Command: "rm -rf /outside", ResultMessage: "result", Exit: sql.NullInt64{Int64: 0, Valid: true}}
	first := selectSafetyCall(call)[0]
	call.ID = "b"
	call.Conversation = "later"
	if got := selectSafetyCall(call)[0]; got.ID != first.ID {
		t.Fatal("a repeat in another conversation should accumulate")
	}
	call.Command = "rm -rf /different"
	if got := selectSafetyCall(call)[0]; got.ID == first.ID {
		t.Fatal("a new target should need a separate review")
	}
	call.Command = "rm -rf $DIR/"
	a := selectSafetyCall(call)[0]
	call.ID = "c"
	if selectSafetyCall(call)[0].ID == a.ID {
		t.Fatal("unresolved variables must not group unrelated resources")
	}
	call.Command = "cd /outside && rm -rf data"
	item := selectSafetyCall(call)[0]
	if item.Subject != "/outside/data" || item.Completion || !item.Evidence[0].Conditional {
		t.Fatalf("compound call %+v", item)
	}
	call.Command = "rm -rf /outside &"
	if selectSafetyCall(call)[0].Completion {
		t.Fatal("background launch is not command completion")
	}
	call.Command = "(rm -rf /outside &)"
	if selectSafetyCall(call)[0].Completion {
		t.Fatal("a subshell must not turn a background launch into completion")
	}
	call.Command = "rm -rf data"
	call.Input = `{"input":{"cmd":"rm -rf data","workdir":"/another/checkout"}}`
	item = selectSafetyCall(call)[0]
	if item.Subject != "/another/checkout/data" || item.Rule != "boundary-delete" {
		t.Fatalf("tool workdir must override workspace %+v", item)
	}
	call.Input = ""
	for _, commands := range [][]string{
		{"git push origin :old-branch", "git push --delete origin old-branch", "git push -d origin old-branch"},
		{"curl -X DELETE https://example.com/object", "curl -XDELETE https://example.com/object", "curl --request=DELETE https://example.com/object"},
		{"gh -R owner/repo release delete v1", "gh release delete v1 --repo=owner/repo", "gh -Rowner/repo release delete v1"},
		{"git clean -fdx --exclude=keep", "git clean -fdxekeep", "git clean -fdx -e keep"},
		{"cd /a && gh -R owner/repo release delete v1", "cd /b && gh --repo owner/repo release delete v1"},
		{"pkill -u501 -x worker", "pkill --euid=501 --exact worker"},
		{"pkill -TERM -x worker", "pkill --signal=TERM --exact worker"},
		{"rsync --delete --exclude keep source/ /outside/", "rsync --delete --exclude=keep source/ /outside/"},
		{"rsync --delete -f '- keep' source/ /outside/", "rsync --delete --filter='- keep' source/ /outside/"},
		{"rsync --del source/ /outside/", "rsync --delete-during source/ /outside/"},
		{"git push -f --tags origin", "git push --force --tags origin"},
	} {
		identity := ""
		for _, command := range commands {
			call.Command = command
			items := selectSafetyCall(call)
			if len(items) != 1 {
				t.Fatalf("%s: %+v", command, items)
			}
			if identity != "" && items[0].ID != identity {
				t.Fatalf("equivalent option forms must share a review: %s", command)
			}
			identity = items[0].ID
		}
	}
	for _, command := range []string{
		`git -C "$CHECKOUT" push --force origin HEAD`,
		`git -C "$CHECKOUT" -C child push --force origin HEAD`,
		`git --git-dir="$GIT_DIR" push --force origin HEAD`,
		`git push --force origin "$REF"`,
		`git push --force-with-lease="$LEASE" origin main`,
		`git push --force origin`,
		`git clean -fdx -- "$PATHS"`,
		`gh -R "$REPO" release delete v1`,
		`gh release delete`,
		`pkill -F /tmp/workers.pid worker`,
		`pkill -u "$UID" worker`,
		`rsync --delete --exclude-from filters source/ /outside/`,
		`rsync --delete --include-from filters source/ /outside/`,
		`rsync --delete --files-from paths source/ /outside/`,
		`rsync --delete --filter='. filters' source/ /outside/`,
		`rsync --delete --filter='merge,r filters' source/ /outside/`,
		`rsync --delete -F source/ /outside/`,
		`rsync --delete --max-delete="$LIMIT" source/ /outside/`,
		`rm -rf "$DIR/"`,
		`cd /outside && echo ok; rm -rf "$OUT"/*`,
	} {
		call.Command, call.ID = command, "first"
		first := selectSafetyCall(call)
		call.ID = "second"
		second := selectSafetyCall(call)
		if len(first) != 1 || len(second) != 1 || first[0].ID == second[0].ID {
			t.Errorf("unresolved selection must stay scoped to its call: %s", command)
		}
	}
	for _, commands := range [][2]string{
		{`git clean -fdx -- data`, `git clean -fdx`},
		{`git clean -fdx -- data`, `git clean -fdx -- other`},
		{`git clean -fdx --exclude=keep`, `git clean -fdx`},
		{`git clean -fdX`, `git clean -fdx`},
		{`git clean -fdx`, `git clean -ffdx`},
		{`cd /other/a && gh release delete v1`, `cd /other/b && gh release delete v1`},
		{`git -C /other/a push --force origin HEAD`, `git -C /other/b push --force origin HEAD`},
		{`git push --force --tags origin`, `git push --mirror origin`},
		{`git push --force --tags origin`, `git push --force --all origin`},
		{`git push --force origin HEAD`, `git push --force --follow-tags origin HEAD`},
		{`git push --force origin HEAD`, `git push --force --prune origin HEAD`},
		{`git push --force --tags --no-tags origin`, `git push --force --no-tags --tags origin`},
		{`pkill -u 501 -x worker`, `pkill -f worker`},
		{`pkill -u 501 worker`, `pkill worker`},
		{`pkill -x worker`, `pkill worker`},
		{`pkill -TERM worker`, `pkill -KILL worker`},
		{`killall -u 501 worker`, `killall worker`},
		{`killall -TERM worker`, `killall -KILL worker`},
		{`rsync --delete --exclude keep source/ /outside/`, `rsync --delete source/ /outside/`},
		{`rsync --delete --exclude keep source/ /outside/`, `rsync --delete-excluded source/ /outside/`},
		{`rsync --delete source/ /outside/`, `rsync --delete other/ /outside/`},
		{`rsync --delete source/ /outside/`, `rsync --delete source /outside/`},
		{`rsync --delete -f '- keep' -f '+ keep' source/ /outside/`, `rsync --delete -f '+ keep' -f '- keep' source/ /outside/`},
		{`rsync --delete --recursive --no-recursive source/ /outside/`, `rsync --delete --no-recursive --recursive source/ /outside/`},
	} {
		call.ID, call.Command = "first", commands[0]
		first := selectSafetyCall(call)
		call.ID, call.Command = "second", commands[1]
		second := selectSafetyCall(call)
		if len(first) != 1 || len(second) != 1 || first[0].ID == second[0].ID {
			t.Errorf("different resources must not share a handoff: %q / %q", commands[0], commands[1])
		}
	}
	t.Run("directory control flow", func(t *testing.T) {
		for _, test := range []struct{ command, subject, rule, cwd string }{
			{`cd /outside || rm -rf .git`, "/repo/.git", "delete", "/repo"},
			{`cd /outside && rm -rf .git`, "/outside/.git", "boundary-delete", "/outside"},
			{`cd /outside; rm -rf .git`, "", "", ""},
			{`cd /outside && echo ok; rm -rf .git`, "", "", ""},
			{`cd /outside && echo ok || rm -rf .git`, "", "", ""},
			{`(cd /outside && echo ok); rm -rf .git`, "/repo/.git", "delete", "/repo"},
			{`(cd /outside && rm -rf .git)`, "/outside/.git", "boundary-delete", "/outside"},
			{`((cd /outside && echo ok)); rm -rf .git`, "/repo/.git", "delete", "/repo"},
			{`(cd /outside && echo ok); (rm -rf .git)`, "/repo/.git", "delete", "/repo"},
			{`cd /outside && (cd child && echo ok) && rm -rf .git`, "/outside/.git", "boundary-delete", "/outside"},
			{`cd /outside & rm -rf .git`, "/repo/.git", "delete", "/repo"},
			{`cd /outside | rm -rf .git`, "/repo/.git", "delete", "/repo"},
			{`echo ok | cd /outside; rm -rf .git`, "", "", ""},
			{`cd "$CHECKOUT" && rm -rf .git`, "", "", ""},
			{`if test -d /outside; then cd /outside; fi; rm -rf .git`, "", "", ""},
			{`! cd /outside || rm -rf .git`, "", "", ""},
			{`cd /outside; rm -rf /repo/.git`, "/repo/.git", "delete", ""},
		} {
			call := safetyCall{ID: "directory", Root: "/repo", Command: test.command}
			items := selectSafetyCall(call)
			if test.rule == "" {
				// A relative path from an unknown directory names nothing to check.
				if len(items) != 0 {
					t.Errorf("%s: %+v", test.command, items)
				}
				continue
			}
			if len(items) != 1 || items[0].Subject != test.subject || items[0].Rule != test.rule || items[0].Evidence[0].CWD != test.cwd {
				t.Errorf("%s: %+v", test.command, items)
			}
		}
	})
}

func safetyTestCall(t *testing.T, c *Catalog, id, command, at string, completed bool) {
	t.Helper()
	tx := seedTx(t, c.DB)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES(?,'conv-1',?,'assistant','tool_call',?,'hash',?)`, id+"-input", id+"-input", jsonText(map[string]any{"input": map[string]any{"cmd": command, "workdir": "/repo"}}), at)
	var exit any
	result := ""
	status := "no_result"
	if completed {
		exit = 0
		result = id + "-result"
		status = "ok"
	}
	exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,command,started_at,status,call_message_id,result_message_id,exit_code)
 VALUES(?,'ws-1','conv-1',1,'claude','tool_call','Bash','command',?,?,?,?,?,?)`, id, command, at, status, id+"-input", result, exit)
	commitSeed(t, tx)
}

func TestSafetyHandoffLifecycle(t *testing.T) {
	// The command/option matrix belongs to TestSafetyIdentityAndExecution.
	// Exercise persistence for a resolved identity and a call-scoped one.
	for _, commands := range [][2]string{
		{`git clean -fdx -- data`, `git clean -fdx`},
		{`git -C "$CHECKOUT" push --force origin HEAD`, `git -C "$CHECKOUT" push --force origin HEAD`},
	} {
		t.Run("separate handoff for "+commands[0], func(t *testing.T) {
			now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
			fixture := newFindingFixture(t, now)
			fixture.conversation(now, false)
			c := fixture.catalog
			safetyTestCall(t, c, "first", commands[0], formatTime(now), true)
			if err := c.refreshSafety(t.Context()); err != nil {
				t.Fatal(err)
			}
			items, err := loadSafetyCases(t.Context(), c.DB)
			if err != nil || len(items) != 1 {
				t.Fatalf("initial selection: %v %+v", err, items)
			}
			id, target := items[0].ID, items[0].Target
			if err := c.SafetyAction(t.Context(), id, map[string]any{"action": "cart_add"}); err != nil {
				t.Fatal(err)
			}
			items, err = loadSafetyCases(t.Context(), c.DB)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := safetyPreview(items, target, []string{id})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.SafetyCopy(t.Context(), target, []string{id}, firstString(preview["preview_token"])); err != nil {
				t.Fatal(err)
			}
			safetyTestCall(t, c, "second", commands[1], formatTime(now.Add(time.Minute)), true)
			if err := c.refreshSafety(t.Context()); err != nil {
				t.Fatal(err)
			}
			items, err = loadSafetyCases(t.Context(), c.DB)
			if err != nil || len(items) != 2 {
				t.Fatalf("a broader or unresolved selection must need a new review: %v %+v", err, items)
			}
			for _, item := range items {
				if item.ID == id && item.State != "handed_off" || item.ID != id && item.State != "open" {
					t.Fatalf("handoff leaked across resources: %+v", items)
				}
			}
		})
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	fixture := newFindingFixture(t, now)
	fixture.conversation(now, false)
	c := fixture.catalog
	safetyTestCall(t, c, "first", "rm -rf /outside", formatTime(now), false)
	if err := c.refreshSafety(t.Context()); err != nil {
		t.Fatal(err)
	}
	items, err := loadSafetyCases(t.Context(), c.DB)
	if err != nil || len(items) != 1 {
		t.Fatalf("one-off case: %v %+v", err, items)
	}
	id, target := items[0].ID, items[0].Target
	if items[0].State != "open" {
		t.Fatal(items[0].State)
	}
	if err := c.SafetyAction(t.Context(), id, map[string]any{"action": "cart_add"}); err != nil {
		t.Fatal(err)
	}
	items, _ = loadSafetyCases(t.Context(), c.DB)
	preview, err := safetyPreview(items, target, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	prompt := firstString(preview["prompt"])
	if strings.Contains(prompt, "/outside") || !strings.Contains(prompt, "get_safety_case") || !strings.Contains(prompt, "change nothing") || !strings.Contains(prompt, "recommend fixes") || !strings.Contains(prompt, id) {
		t.Fatal("prompt should give investigation instructions, ask for fixes and name its notices, without transcript payload")
	}
	if _, err := c.SafetyCopy(t.Context(), target, []string{id}, "stale"); err == nil {
		t.Fatal("copy must match reviewed snapshot")
	}
	if _, err := c.SafetyCopy(t.Context(), target, []string{id}, firstString(preview["preview_token"])); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SafetyCopy(t.Context(), target, []string{id}, firstString(preview["preview_token"])); err != nil {
		t.Fatal("successful retry should be idempotent", err)
	}
	items, _ = loadSafetyCases(t.Context(), c.DB)
	if items[0].State != "handed_off" || items[0].Cart != nil {
		t.Fatalf("copy %+v", items[0])
	}
	if n := countRows(t, c, "SELECT COUNT(*) FROM finding_interventions"); n != 0 {
		t.Fatal("safety handoff must not start efficiency measurement")
	}
	safetyTestCall(t, c, "repeat", "rm -rf /outside", formatTime(now.Add(time.Hour)), false)
	if err := c.refreshSafety(t.Context()); err != nil {
		t.Fatal(err)
	}
	items, _ = loadSafetyCases(t.Context(), c.DB)
	if len(items) != 1 || items[0].State != "handed_off" || items[0].Calls != 2 {
		t.Fatalf("quiet accumulation %+v", items)
	}
	safetyTestCall(t, c, "stronger", "rm -rf /outside", formatTime(now.Add(2*time.Hour)), true)
	if err := c.refreshSafety(t.Context()); err != nil {
		t.Fatal(err)
	}
	items, _ = loadSafetyCases(t.Context(), c.DB)
	if items[0].State != "open" || !items[0].Material {
		t.Fatal("stronger execution evidence should reopen the review")
	}
	if err := c.SafetyAction(t.Context(), id, map[string]any{"action": "dismiss"}); err != nil {
		t.Fatal(err)
	}
	if err := c.refreshSafety(t.Context()); err != nil {
		t.Fatal(err)
	}
	items, _ = loadSafetyCases(t.Context(), c.DB)
	if items[0].State != "dismissed" {
		t.Fatal("discovery must preserve dismissal")
	}
	if err := c.SafetyAction(t.Context(), id, map[string]any{"action": "restore"}); err != nil {
		t.Fatal(err)
	}
	items, _ = loadSafetyCases(t.Context(), c.DB)
	if items[0].State != "open" {
		t.Fatal("bring back must reopen")
	}
	if err := c.SafetyAction(t.Context(), id, map[string]any{"action": "cart_add"}); err != nil {
		t.Fatal(err)
	}
	items, _ = loadSafetyCases(t.Context(), c.DB)
	next, err := safetyPreview(items, target, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	if next["preview_token"] == preview["preview_token"] {
		t.Fatal("a later handoff needs a fresh basket revision")
	}
	if _, err := c.SafetyCopy(t.Context(), target, []string{id}, firstString(next["preview_token"])); err != nil {
		t.Fatal(err)
	}
	items, _ = loadSafetyCases(t.Context(), c.DB)
	if items[0].State != "handed_off" {
		t.Fatal("a brought-back case should be handable off again")
	}
}

func TestSafetyBasketBoundaries(t *testing.T) {
	cart := &safetyCart{Target: "repo:a", Label: "alpha", Ticked: true}
	items := []safetyCase{{ID: "safety_a", State: "open", Cart: cart, Title: "Review", Questions: []string{"Why?"}}}
	for _, ids := range [][]string{{"safety_a", "efficiency_a"}, {"safety_a", "safety_a"}, {"missing"}, nil} {
		if _, err := safetySelection(items, "repo:a", ids); err == nil {
			t.Fatalf("invalid selection accepted %v", ids)
		}
	}
	if _, err := safetySelection(items, "repo:b", []string{"safety_a"}); err == nil {
		t.Fatal("wrong-target copy accepted")
	}
	c, _ := testCatalog(t)
	if _, err := c.CartPrompt(t.Context(), "repo:a", []string{"efficiency_a", "safety_a"}, "diff"); err == nil {
		t.Fatal("efficiency prompt must reject safety IDs")
	}
}

func TestSafetyAPIAndMCP(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	fixture := newFindingFixture(t, now)
	fixture.conversation(now, false)
	c := fixture.catalog
	safetyTestCall(t, c, "selected", "rm -rf /outside", formatTime(now), false)
	if err := c.RefreshFindings(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	view, err := c.SafetyOverview(t.Context())
	if err != nil || len(view["cases"].([]map[string]any)) != 1 {
		t.Fatalf("daily integration: %v %+v", err, view)
	}
	id := firstString(view["cases"].([]map[string]any)[0]["id"])
	for _, name := range []string{"list_safety_cases", "get_safety_case"} {
		if _, ok, err := callSafetyMCP(c, name, map[string]any{"id": id}); !ok || err != nil {
			t.Fatal(name, err)
		}
	}
	for rule, want := range map[string]int{"boundary-delete": 1, "permissions": 0} {
		value, _, err := callSafetyMCP(c, "list_safety_cases", map[string]any{"rule": rule, "state": "all"})
		items := value.(map[string]any)["items"].([]map[string]any)
		if err != nil || len(items) != want || want == 1 && items[0]["subject"] != "/outside" {
			t.Fatal("a check's reviews should list their selected paths", rule, err, items)
		}
	}
	if _, _, err := callSafetyMCP(c, "list_safety_cases", map[string]any{"rule": "unknown"}); err == nil {
		t.Fatal("unknown check accepted")
	}
	s := testServer(t, Config{}, c)
	for _, endpoint := range []string{"/api/findings/safety", "/api/findings/safety/" + id, "/api/findings/safety/settings"} {
		response := httptest.NewRecorder()
		s.getFindings(response, httptest.NewRequest(http.MethodGet, endpoint, nil))
		if response.Code != 200 {
			t.Fatal(endpoint, response.Code, response.Body.String())
		}
	}
	for _, endpoint := range []string{"cart/prompt", "cart/copy"} {
		response := httptest.NewRecorder()
		s.postFindings(response, queryFixtureHTTPRequest(http.MethodPost, "/api/findings/safety/"+endpoint, nil), map[string]any{"target": "repo:repo-1", "ids": []any{id, "efficiency"}})
		if response.Code < 400 {
			t.Fatal(fmt.Sprint("mixed request accepted: ", endpoint))
		}
	}
}

func TestSafetyDiscoverySettings(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	fixture := newFindingFixture(t, now)
	fixture.conversation(now, false)
	c := fixture.catalog
	settings, err := c.SafetySettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rules := settings["rules"].([]map[string]any)
	if len(rules) == 0 || len(rules) != len(safetyRules) {
		t.Fatal("settings must expose the full discovery catalog, even before any reviews exist", rules)
	}
	seen := map[string]bool{}
	for _, rule := range rules {
		id := firstString(rule["id"])
		if _, ok := safetyRules[id]; !ok || seen[id] || rule["enabled"] != true || firstString(rule["title"]) == "" || firstString(rule["explanation"]) == "" {
			t.Fatal("each check needs a unique ID, description, and default enabled state", rule)
		}
		seen[id] = true
	}
	safetyTestCall(t, c, "existing", "rm -rf /outside", formatTime(now), false)
	if err := c.refreshSafety(t.Context()); err != nil {
		t.Fatal(err)
	}
	items, _ := loadSafetyCases(t.Context(), c.DB)
	if len(items) != 1 {
		t.Fatal(items)
	}
	if err := c.SafetyAction(t.Context(), items[0].ID, map[string]any{"action": "cart_add"}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSafetySettings(t.Context(), map[string]any{"rule_enabled": map[string]any{"boundary-delete": false}}); err != nil {
		t.Fatal(err)
	}
	safetyTestCall(t, c, "new", "rm -rf /another", formatTime(now.Add(time.Minute)), false)
	if err := c.refreshSafety(t.Context()); err != nil {
		t.Fatal(err)
	}
	items, _ = loadSafetyCases(t.Context(), c.DB)
	if len(items) != 1 || items[0].Cart == nil {
		t.Fatal("disabling discovery must preserve existing reviews and basket drafts", items)
	}
	// A disabled check is not run, so an upgrade cannot tell its notices are
	// noise: they stay even after leaving the basket.
	if err := c.SafetyAction(t.Context(), items[0].ID, map[string]any{"action": "cart_remove"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`UPDATE safety_cases SET version='safety-v0'`); err != nil {
		t.Fatal(err)
	}
	if err := c.refreshSafety(t.Context()); err != nil {
		t.Fatal(err)
	}
	if items, _ = loadSafetyCases(t.Context(), c.DB); len(items) != 1 {
		t.Fatal("an upgrade must keep a disabled check's notices", items)
	}
	if detail, err := c.SafetyDetail(t.Context(), items[0].ID); err != nil || detail.Danger != safetyDangerLabels[safetyRules["boundary-delete"].Danger] {
		t.Fatal("Details names the check's danger", detail, err)
	}
	if err := c.SetSafetySettings(t.Context(), map[string]any{"rule_enabled": map[string]any{"credentials": false, "unknown": true}}); err == nil {
		t.Fatal("unknown check accepted")
	}
	disabled, err := safetyDisabledRules(t.Context(), c.DB)
	if err != nil || disabled["credentials"] || !disabled["boundary-delete"] {
		t.Fatalf("settings write must be atomic: %v %v", disabled, err)
	}
	if err := c.SetSafetySettings(t.Context(), map[string]any{"rule_enabled": map[string]any{"boundary-delete": true}}); err != nil {
		t.Fatal(err)
	}
	if err := c.refreshSafety(t.Context()); err != nil {
		t.Fatal(err)
	}
	items, _ = loadSafetyCases(t.Context(), c.DB)
	if len(items) != 2 {
		t.Fatal("reenabled checks should discover retained evidence", items)
	}
}

func TestSafetyQueryTableMatchesReviews(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	fixture := newFindingFixture(t, now)
	fixture.conversation(now, false)
	c := fixture.catalog
	safetyTestCall(t, c, "first", "rm -rf /outside", formatTime(now), false)
	safetyTestCall(t, c, "second", "rm -rf /other", formatTime(now), true)
	safetyTestCall(t, c, "mode", "chmod 777 /shared", formatTime(now), false)
	if err := c.refreshSafety(t.Context()); err != nil {
		t.Fatal(err)
	}
	overview, err := c.FindingsOverview(t.Context(), "summary")
	if err != nil {
		t.Fatal(err)
	}
	if open := overview["summary"].(map[string]any)["safety_open"]; open != 2 {
		t.Fatalf("the Optimize badge counts each check once, not each notice: %v", open)
	}
	config := testConfig(t)
	server := testServer(t, config, c)
	query := func(where ...qt.WhereTerm) []map[string]any {
		t.Helper()
		var value struct {
			Rows []map[string]any `json:"rows"`
		}
		response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/safety/rows-v2", queryFixtureRows{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"id", "state", "rule", "subject", "command", "calls", "target_label"}, Where: where, OrderBy: []qt.OrderBy{{Field: "subject", Dir: "asc"}}, Limit: 50}})
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil {
			t.Fatal(response.Code, response.Body.String())
		}
		return value.Rows
	}
	rows := func() []map[string]any {
		return query(qt.WhereTerm{Field: "state", Op: "=", Value: "open"}, qt.WhereTerm{Field: "rule", Op: "=", Value: "boundary-delete"})
	}
	initial := rows()
	if len(initial) != 2 || initial[0]["subject"] != "/other" || initial[1]["subject"] != "/outside" {
		t.Fatal("one check's reviews should list their paths in order", initial)
	}
	for _, row := range initial {
		if row["target_label"] != "alpha" || integer(row["calls"]) != 1 || row["rule"] != "boundary-delete" || row["command"] != "rm -rf "+firstString(row["subject"]) {
			t.Fatal(row)
		}
	}
	if permissions := query(qt.WhereTerm{Field: "rule", Op: "=", Value: "permissions"}); len(permissions) != 1 || permissions[0]["subject"] != "/shared" || permissions[0]["command"] != "chmod 777 /shared" {
		t.Fatal(permissions)
	}
	id := firstString(initial[0]["id"])
	if err := c.SafetyAction(t.Context(), id, map[string]any{"action": "dismiss"}); err != nil {
		t.Fatal(err)
	}
	if got := rows(); len(got) != 1 {
		t.Fatal("table should reflect review state changes immediately", got)
	}
	response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/safety/metrics", metricRequest(qt.AggSpec{ID: "reviews", Expression: "SUM([review_count])"}))
	var metrics queryFixtureMetricsResult
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &metrics) != nil || len(metrics.Metrics) != 1 || len(metrics.Metrics[0].Buckets) != 1 || floatOr(metrics.Metrics[0].Buckets[0].Value) != 3 {
		t.Fatal(response.Code, response.Body.String())
	}
	bulk := func(action string, ids ...string) int {
		t.Helper()
		return apiCall(t, server, config.APIToken, http.MethodPost, "/api/findings/safety/bulk", map[string]any{"action": action, "ids": ids}).Code
	}
	remaining := firstString(rows()[0]["id"])
	if code := bulk("dismiss", remaining, "unknown"); code != 400 || len(rows()) != 1 {
		t.Fatal("a selection with an unavailable review must change nothing", code)
	}
	if code := bulk("cart_add", remaining, id); code != 400 {
		t.Fatal("a dismissed review cannot join a prompt", code)
	}
	if code := bulk("restore", id); code != 200 || len(rows()) != 2 {
		t.Fatal("bulk restore", code)
	}
	if code := bulk("cart_add", remaining, id); code != 200 {
		t.Fatal("bulk add", code)
	}
	if err := c.SafetyAction(t.Context(), id, map[string]any{"action": "cart_tick", "ticked": false}); err != nil {
		t.Fatal(err)
	}
	if code := bulk("cart_add", id); code != 200 {
		t.Fatal(code)
	}
	items, _ := loadSafetyCases(t.Context(), c.DB)
	for _, item := range items {
		if item.Rule == "boundary-delete" && (item.Cart == nil || !item.Cart.Ticked) {
			t.Fatal("adding a selection to a prompt ticks every review in it", item)
		}
	}
	// A dashboard prompt ticks, unticks and removes a whole check at once.
	tick := apiCall(t, server, config.APIToken, http.MethodPost, "/api/findings/safety/bulk", map[string]any{"action": "cart_tick", "ticked": false, "ids": []string{remaining, id}})
	items, _ = loadSafetyCases(t.Context(), c.DB)
	for _, item := range items {
		if item.Rule == "boundary-delete" && (tick.Code != 200 || item.Cart == nil || item.Cart.Ticked) {
			t.Fatal("unticking a check unticks each of its notices", tick.Code, item)
		}
	}
	if code := bulk("cart_remove", remaining); code != 200 {
		t.Fatal("bulk remove", code)
	}
	if code := bulk("dismiss", remaining, id); code != 200 || len(rows()) != 0 {
		t.Fatal("bulk dismiss", code)
	}
	if code := bulk("cart_reorder", id); code != 400 {
		t.Fatal("only selection actions apply in bulk", code)
	}
}

func TestSafetyPromptStatesEachCheckOnce(t *testing.T) {
	notice := func(id, rule string) safetyCase {
		return safetyCase{ID: id, Rule: rule, Title: safetyRules[rule].Title, Questions: safetyRules[rule].Questions}
	}
	credentials := safetyRules["credentials"]
	same := safetyPrompt([]safetyCase{notice("safety_a", "credentials"), notice("safety_b", "credentials"), notice("safety_c", "credentials")}, "repo:x", "explo")
	if strings.Count(same, credentials.Questions[0]) != 1 || strings.Count(same, credentials.Fixes) != 1 || !strings.Contains(same, "safety_a, safety_b, safety_c") {
		t.Fatalf("one check's questions and fixes appear once, with its notices listed:\n%s", same)
	}
	if !strings.Contains(same, "3 risky action notices") || !strings.Contains(same, credentials.Title) || strings.Contains(same, "## ") {
		t.Fatalf("a single-check prompt names the check up front, without sections:\n%s", same)
	}
	mixed := safetyPrompt([]safetyCase{notice("safety_p", "process"), notice("safety_a", "credentials"), notice("safety_q", "process")}, "repo:x", "explo")
	high, low := strings.Index(mixed, "## "+credentials.Title), strings.Index(mixed, "## "+safetyRules["process"].Title)
	if high < 0 || low < high || !strings.Contains(mixed, "Notices: safety_p, safety_q") || strings.Count(mixed, safetyRules["process"].Questions[0]) != 1 {
		t.Fatalf("mixed prompts have one section per check, most dangerous first:\n%s", mixed)
	}
}
