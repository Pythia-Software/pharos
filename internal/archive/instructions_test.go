package archive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures are synthetic, shaped like Claude Code 2.1 session files and
// Codex 0.15x rollouts.

func claudeInstructionLines(cwd string) []string {
	attachment := func(ts string, value map[string]any) string {
		line, _ := json.Marshal(map[string]any{"type": "attachment", "uuid": "u-" + ts, "sessionId": "claude-session", "cwd": cwd, "version": "2.1.284", "timestamp": ts, "attachment": value})
		return string(line)
	}
	files := func(project string) map[string]any {
		return map[string]any{"type": "instructions", "files": []any{
			map[string]any{"path": filepath.Join(cwd, "CLAUDE.md"), "type": "Project", "content": project},
			map[string]any{"path": "/home/me/.claude/projects/p/memory/MEMORY.md", "type": "AutoMem", "content": "- memory"},
		}}
	}
	return []string{
		attachment("2026-09-20T10:00:00.000Z", files("Rules v1")),
		`{"type":"user","uuid":"q","sessionId":"claude-session","cwd":"` + cwd + `","timestamp":"2026-09-20T10:00:01.000Z","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","uuid":"a","sessionId":"claude-session","cwd":"` + cwd + `","timestamp":"2026-09-20T10:00:02.000Z","message":{"id":"req-1","model":"claude-test","content":"hi","usage":{"input_tokens":10,"output_tokens":2}}}`,
		attachment("2026-09-20T10:00:03.000Z", map[string]any{"type": "skill_listing", "names": []any{"alpha", "beta"}, "isInitial": true, "content": "- alpha: Does alpha things\n- beta: Does beta\n  over two lines"}),
		attachment("2026-09-20T10:00:04.000Z", map[string]any{"type": "nested_memory", "path": filepath.Join(cwd, "pkg", "CLAUDE.md"), "displayPath": "pkg/CLAUDE.md",
			"content": map[string]any{"path": filepath.Join(cwd, "pkg", "CLAUDE.md"), "type": "Project", "content": "nested rules", "contentDiffersFromDisk": false}}),
		attachment("2026-09-20T10:00:05.000Z", map[string]any{"type": "invoked_skills", "skills": []any{map[string]any{"name": "dataviz", "path": "bundled:dataviz", "content": "skill body"}}}),
		// After a compaction Claude records the (edited) file again.
		attachment("2026-09-20T11:00:00.000Z", files("Rules v2, longer")),
		`{"type":"assistant","uuid":"b","sessionId":"claude-session","cwd":"` + cwd + `","timestamp":"2026-09-20T11:00:01.000Z","message":{"id":"req-2","model":"claude-test","content":"done","usage":{"input_tokens":12,"output_tokens":2}}}`,
	}
}

func codexInstructionLines(cwd string) []string {
	skills := "<skills_instructions>\n## Skills\nA skill is a set of local instructions.\n### Skill roots\n- `r0` = `/home/me/.codex/skills/.system`\n### Available skills\n" +
		"- imagegen: Generate images (bitmaps). (file: r0/imagegen/SKILL.md)\n- repo-skill: A repository skill. (file: " + cwd + "/.agents/skills/repo-skill/SKILL.md)\n" +
		"### How to use skills\n- Discovery: The list above is the skills available.\n</skills_instructions>"
	agents := "# AGENTS.md instructions for " + cwd + "\n\n<INSTRUCTIONS>\nUse go test.\n</INSTRUCTIONS>"
	line := func(value map[string]any) string {
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	return []string{
		line(map[string]any{"timestamp": "2026-09-21T09:00:00.000Z", "type": "session_meta", "payload": map[string]any{"id": "codex-session", "cwd": cwd, "originator": "codex_exec", "cli_version": "0.155.0"}}),
		line(map[string]any{"timestamp": "2026-09-21T09:00:01.000Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "developer",
			"content": []any{map[string]any{"type": "input_text", "text": skills}, map[string]any{"type": "input_text", "text": "<permissions instructions>...</permissions instructions>"}}}}),
		line(map[string]any{"timestamp": "2026-09-21T09:00:02.000Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": agents}}}}),
		line(map[string]any{"timestamp": "2026-09-21T09:00:02.000Z", "type": "world_state", "payload": map[string]any{"full": true, "state": map[string]any{"agents_md": map[string]any{"local": map[string]any{"directory": cwd, "instructions": "abc123"}}}}}),
		line(map[string]any{"timestamp": "2026-09-21T09:00:03.000Z", "type": "world_state", "payload": map[string]any{"full": true, "state": map[string]any{"agents_md": map[string]any{}}}}),
		line(map[string]any{"timestamp": "2026-09-21T09:00:04.000Z", "type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "fix it"}}),
		line(map[string]any{"timestamp": "2026-09-21T09:00:05.000Z", "type": "event_msg", "payload": map[string]any{"type": "agent_message", "message": "fixed"}}),
	}
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func instructionsByKey(records []InstructionRecord) map[string]InstructionRecord {
	out := map[string]InstructionRecord{}
	for _, record := range records {
		out[record.Kind+" "+record.Path] = record
	}
	return out
}

func recordBytes(record InstructionRecord) int64 {
	if record.Bytes == nil {
		return -1
	}
	return *record.Bytes
}

func TestClaudeInstructionsFromTranscript(t *testing.T) {
	cwd := "/work/repo"
	path := filepath.Join(t.TempDir(), "claude-session.jsonl")
	writeLines(t, path, claudeInstructionLines(cwd))
	adapter := jsonlAdapter{baseAdapter: baseAdapter{config: SourceConfig{Path: filepath.Dir(path), Account: "local"}}, provider: "claude"}
	record, ok, err := adapter.parse(path)
	if err != nil || !ok {
		t.Fatalf("parse: %v %v", ok, err)
	}
	got := instructionsByKey(record.Conversations[0].Instructions)
	project := got["project /work/repo/CLAUDE.md"]
	if project.Harness != "claude-code" || recordBytes(project) != int64(len("Rules v2, longer")) || project.Hash != hashBytes([]byte("Rules v2, longer")) ||
		project.FirstSeenAt != "2026-09-20T10:00:00.000Z" || project.LastSeenAt != "2026-09-20T11:00:00.000Z" {
		t.Fatalf("project file = %#v", project)
	}
	if memory := got["automem /home/me/.claude/projects/p/memory/MEMORY.md"]; recordBytes(memory) != int64(len("- memory")) {
		t.Fatalf("auto memory = %#v", memory)
	}
	if nested := got["nested /work/repo/pkg/CLAUDE.md"]; recordBytes(nested) != int64(len("nested rules")) {
		t.Fatalf("nested memory = %#v", nested)
	}
	if skill := got["skill bundled:dataviz"]; recordBytes(skill) != int64(len("skill body")) {
		t.Fatalf("invoked skill = %#v", skill)
	}
	if alpha, beta := got["skill_listing alpha"], got["skill_listing beta"]; recordBytes(alpha) != int64(len("- alpha: Does alpha things")) || recordBytes(beta) != int64(len("- beta: Does beta\n  over two lines")) {
		t.Fatalf("skill listing = %#v %#v", alpha, beta)
	}
	if len(got) != 6 {
		t.Fatalf("records = %#v", got)
	}
	// The backfill reads the same record from the file.
	read, err := readInstructionsFile(path, "claude")
	if err != nil || len(read) != 6 || instructionsByKey(read)["project /work/repo/CLAUDE.md"].Hash != project.Hash {
		t.Fatalf("backfill read = %#v, %v", read, err)
	}
}

func TestCodexInstructionsFromRollout(t *testing.T) {
	cwd := "/work/repo"
	path := filepath.Join(t.TempDir(), "sessions", "rollout-2026-09-21T09-00-00-codex-session.jsonl")
	writeLines(t, path, codexInstructionLines(cwd))
	adapter := jsonlAdapter{baseAdapter: baseAdapter{config: SourceConfig{Path: filepath.Dir(filepath.Dir(path)), Account: "local"}}, provider: "codex"}
	record, ok, err := adapter.parse(path)
	if err != nil || !ok {
		t.Fatalf("parse: %v %v", ok, err)
	}
	got := instructionsByKey(record.Conversations[0].Instructions)
	agents := got["agents_md /work/repo/AGENTS.md"]
	if agents.Harness != "codex" || recordBytes(agents) != int64(len("Use go test.")) || agents.Hash != hashBytes([]byte("Use go test.")) || agents.FirstSeenAt != "2026-09-21T09:00:02.000Z" {
		t.Fatalf("AGENTS.md = %#v", agents)
	}
	image := got["skill_listing /home/me/.codex/skills/.system/imagegen/SKILL.md"]
	if recordBytes(image) != int64(len("- imagegen: Generate images (bitmaps). (file: r0/imagegen/SKILL.md)")) {
		t.Fatalf("root-relative skill = %#v", image)
	}
	if _, ok := got["skill_listing /work/repo/.agents/skills/repo-skill/SKILL.md"]; !ok || len(got) != 3 {
		t.Fatalf("records = %#v", got)
	}
	read, err := readInstructionsFile(path, "codex")
	if err != nil || len(read) != 3 {
		t.Fatalf("backfill read = %#v, %v", read, err)
	}
	// A directory named only in world_state has no content to measure.
	only := codexInstructions([]map[string]any{{"type": "world_state", "timestamp": "2026-09-21T09:00:00Z", "payload": map[string]any{"state": map[string]any{"agents_md": map[string]any{"directory": "/other"}}}}})
	if len(only) != 1 || only[0].Path != "/other/AGENTS.md" || only[0].Bytes != nil {
		t.Fatalf("world_state only = %#v", only)
	}
}

func TestInstructionsIngestRoundTrip(t *testing.T) {
	catalog, _ := testCatalog(t)
	root := t.TempDir()
	// A Conductor workspace names its repository without asking Git.
	cwd := filepath.Join(root, "conductor", "workspaces", "repo", "tokyo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeRoot := filepath.Join(root, "claude")
	session := filepath.Join(claudeRoot, "project", "claude-session.jsonl")
	lines := claudeInstructionLines(cwd)
	writeLines(t, session, lines[:3])
	adapter, err := MakeAdapter(SourceConfig{Name: "claude", Kind: "claude", Path: claudeRoot, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if result := catalog.Ingest(adapter, nil); result.Error != nil || result.Conversations != 1 {
		t.Fatalf("ingest: %#v", result)
	}
	rows, err := queryMaps(catalog.DB, `SELECT kind,path,bytes,repo_path,harness FROM conversation_instructions ORDER BY kind`)
	if err != nil || len(rows) != 2 || firstString(rows[0]["kind"]) != "automem" || rows[0]["repo_path"] != nil ||
		firstString(rows[1]["kind"]) != "project" || firstString(rows[1]["repo_path"]) != "CLAUDE.md" || integer(rows[1]["bytes"]) != int64(len("Rules v1")) || firstString(rows[1]["harness"]) != "claude-code" {
		t.Fatalf("first ingest rows = %#v, %v", rows, err)
	}
	// The session grows: the conversation's rows are replaced with the new read.
	writeLines(t, session, lines)
	if result := catalog.Ingest(adapter, nil); result.Error != nil || result.Conversations != 1 {
		t.Fatalf("second ingest: %#v", result)
	}
	var count, size int64
	if err := catalog.DB.QueryRow(`SELECT COUNT(*),(SELECT bytes FROM conversation_instructions WHERE kind='project') FROM conversation_instructions`).Scan(&count, &size); err != nil || count != 6 || size != int64(len("Rules v2, longer")) {
		t.Fatalf("after growth: %d rows, project %d bytes, %v", count, size, err)
	}
	if pending, err := catalog.pendingInstructions(context.Background()); err != nil || pending != 0 {
		t.Fatalf("indexed conversation pending: %d %v", pending, err)
	}
	var repositoryID string
	if err := catalog.DB.QueryRow(`SELECT repository_id FROM workspaces WHERE source_kind='claude'`).Scan(&repositoryID); err != nil || repositoryID == "" {
		t.Fatalf("repository: %q %v", repositoryID, err)
	}
	coverage, err := catalog.instructionCoverage(context.Background(), nil, repositoryID, "2026-09-01T00:00:00.000Z")
	if err != nil || len(coverage) != 1 || coverage[0].Provider != "claude" || coverage[0].Conversations != 1 || coverage[0].Recorded != 1 || coverage[0].ProjectLoaded != 1 {
		t.Fatalf("coverage = %#v, %v", coverage, err)
	}
	files := map[string]instructionFileUse{}
	for _, file := range coverage[0].Files {
		files[file.Kind+" "+file.RepoPath] = file
	}
	if file := files["project CLAUDE.md"]; file.Bytes == nil || *file.Bytes != int64(len("Rules v2, longer")) || len(files) != 2 || files["nested pkg/CLAUDE.md"].Conversations != 1 {
		t.Fatalf("coverage files = %#v", coverage[0].Files)
	}
	if later, err := catalog.instructionCoverage(context.Background(), nil, repositoryID, "2026-10-01T00:00:00.000Z"); err != nil || len(later) != 0 {
		t.Fatalf("coverage after since = %#v, %v", later, err)
	}
	stats, err := catalog.instructionLoad(context.Background(), nil, repositoryID, "CLAUDE.md", "2026-09-20T00:00:00.000Z", "2026-09-21T00:00:00.000Z")
	var requests int64
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM model_requests`).Scan(&requests); err != nil || requests == 0 {
		t.Fatalf("model requests = %d, %v", requests, err)
	}
	if err != nil || stats.Sessions != 1 || stats.Requests != requests || stats.BytesFirst != int64(len("Rules v2, longer")) || stats.ByteRequests != requests*stats.BytesLast {
		t.Fatalf("load = %#v (requests %d), %v", stats, requests, err)
	}
	if none, err := catalog.instructionLoad(context.Background(), nil, repositoryID, "CLAUDE.md", "2026-09-21T00:00:00.000Z", ""); err != nil || none.Sessions != 0 {
		t.Fatalf("load outside range = %#v, %v", none, err)
	}
}

func TestInstructionsBackfill(t *testing.T) {
	c, _ := testCatalog(t)
	root := t.TempDir()
	claudeFile := filepath.Join(root, "claude-session.jsonl")
	writeLines(t, claudeFile, claudeInstructionLines("/work/repo"))
	before, err := os.ReadFile(claudeFile)
	if err != nil {
		t.Fatal(err)
	}
	// A Codex rollout from another Mac, present only as a capture.
	origin := "/other-mac/rollout.jsonl"
	dir := filepath.Join(c.captureRootPath(), "other-host", "codex-source")
	writeLines(t, filepath.Join(dir, "files", "rollout.jsonl"), codexInstructionLines("/work/repo"))
	manifest, err := json.Marshal(captureManifest{Version: captureManifestVersion, Files: []*capturedFile{{Path: origin, Captured: "files/rollout.jsonl"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, captureManifestName), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	host := currentHost().ID
	for _, statement := range []string{
		`INSERT INTO repositories(id,display_name,local_locations_json,created_at,updated_at) VALUES('repo','repo','["/work/repo"]','2026-01-01','2026-01-01')`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,location,title,indexed_at) VALUES
			('wc','claude','local','c','repo','/work/repo','Claude','2026-09-01'),('wx','codex','local','x','repo','/work/repo','Codex','2026-09-01'),
			('wg','claude','local','g','repo','/work/repo','Gone','2026-09-01'),('wd','conductor','local','d','repo','/work/repo','Conductor','2026-09-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,origin,origin_host_id,started_at) VALUES
			('c','wc','claude','local','c','` + claudeFile + `','` + host + `','2026-09-20T10:00:00.000Z'),
			('x','wx','codex','local','x','` + origin + `','merged','2026-09-21T09:00:00.000Z'),
			('g','wg','claude','local','g','` + filepath.Join(root, "gone.jsonl") + `','` + host + `','2026-09-20T10:00:00.000Z'),
			('d','wd','claude','local','conductor:d',NULL,'` + host + `','2026-09-20T10:00:00.000Z')`,
	} {
		if _, err := c.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if pending := upgradePending(t, c); pending["instructions"] != 3 {
		t.Fatalf("pending = %v", pending)
	}
	calls := 0
	done, err := c.BackfillInstructions(context.Background(), func(done, total int) { calls++ })
	if err != nil || done != 3 || calls != 1 {
		t.Fatalf("backfill = %d (%d progress calls), %v", done, calls, err)
	}
	rows, err := queryMaps(c.DB, `SELECT conversation_id,COUNT(*) n,SUM(repo_path IS NOT NULL) in_repo FROM conversation_instructions GROUP BY conversation_id ORDER BY conversation_id`)
	if err != nil || len(rows) != 2 || firstString(rows[0]["conversation_id"]) != "c" || integer(rows[0]["n"]) != 6 || integer(rows[0]["in_repo"]) != 2 ||
		firstString(rows[1]["conversation_id"]) != "x" || integer(rows[1]["n"]) != 3 || integer(rows[1]["in_repo"]) != 2 {
		t.Fatalf("backfilled rows = %#v, %v", rows, err)
	}
	states, err := queryMaps(c.DB, `SELECT conversation_id,status FROM conversation_instructions_state ORDER BY conversation_id`)
	if err != nil || len(states) != 3 || firstString(states[0]["status"]) != "transcript" || firstString(states[1]["conversation_id"]) != "g" || firstString(states[1]["status"]) != "unavailable" {
		t.Fatalf("states = %#v, %v", states, err)
	}
	if after, err := os.ReadFile(claudeFile); err != nil || string(after) != string(before) {
		t.Fatalf("source changed: %v", err)
	}
	// Nothing is read twice.
	if done, err := c.BackfillInstructions(context.Background(), nil); err != nil || done != 0 {
		t.Fatalf("second backfill = %d, %v", done, err)
	}
	coverage, err := c.instructionCoverage(context.Background(), c.DB, "repo", "")
	if err != nil || len(coverage) != 2 || coverage[0].Provider != "claude" || coverage[0].Conversations != 2 || coverage[0].Recorded != 1 || coverage[0].Unavailable != 1 ||
		coverage[1].Provider != "codex" || coverage[1].ProjectLoaded != 1 {
		t.Fatalf("coverage = %#v, %v", coverage, err)
	}
	if agents, err := c.instructionLoad(context.Background(), c.DB, "repo", "AGENTS.md", "", ""); err != nil || agents.Sessions != 1 || agents.BytesLast != int64(len("Use go test.")) {
		t.Fatalf("AGENTS.md load = %#v, %v", agents, err)
	}
}
