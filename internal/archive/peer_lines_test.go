package archive

import "testing"

func TestNormalizePeerLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"    return fmt.Errorf(\"x\")  ", "return fmt.Errorf(\"x\")"},
		{"+\treturn value", "return value"},
		{"-\tremoved", "removed"},
		{"+++ b/file.go", "+++ b/file.go"},
		{"    42→\tconst answer = 42", "const answer = 42"},
		{"17\tfunc main() {", "func main() {"},
		{"12:  matched text", "matched text"},
		{"2026 is a year", "2026 is a year"},
	} {
		if got := normalizePeerLine(tc.in); got != tc.want {
			t.Errorf("normalizePeerLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPeerLinesMatchAcrossPresentations(t *testing.T) {
	const line = `	if err := c.derivePeerLinks(ctx, since); err != nil { return err }`
	cases := map[string]string{
		"edit":  `{"input":{"file_path":"/x/a.go","old_string":"keep this quoted old text that is long enough to count","new_string":"` + jsonEscape(line) + `\n}"}}`,
		"patch": `{"input":"*** Begin Patch\n*** Update File: a.go\n@@\n-old line that was removed from the file entirely ok\n+` + jsonEscape(line) + `\n*** End Patch"}`,
	}
	for name, call := range cases {
		written := map[uint64]bool{}
		writtenPeerLines(call, written)
		if !written[peerLineHash(normalizePeerLine(line))] {
			t.Fatalf("%s: written line not found in %v", name, written)
		}
		if written[peerLineHash("keep this quoted old text that is long enough to count")] {
			t.Fatalf("%s: old_string counted as written", name)
		}
		if written[peerLineHash("old line that was removed from the file entirely ok")] {
			t.Fatalf("%s: removed patch line counted as written", name)
		}
	}
	diff := `{"content":"diff --git a/a.go b/a.go\n@@ -1 +1 @@\n+` + jsonEscape(line) + `\n context line that is unchanged but long enough to count"}`
	all, added := map[uint64]bool{}, map[uint64]bool{}
	readPeerLines(diff, all, added)
	hash := peerLineHash(normalizePeerLine(line))
	if !all[hash] || !added[hash] {
		t.Fatalf("diff line not read: all=%v added=%v", all[hash], added[hash])
	}
	context := peerLineHash("context line that is unchanged but long enough to count")
	if !all[context] || added[context] {
		t.Fatalf("context line: all=%v added=%v", all[context], added[context])
	}
	numbered := `{"content":"    41→\n    42→` + jsonEscape(line) + `"}`
	all = map[uint64]bool{}
	readPeerLines(numbered, all, nil)
	if !all[hash] {
		t.Fatal("numbered file read did not match the written line")
	}
}

func TestPeerLinesIgnoreCommandsAndShortLines(t *testing.T) {
	written := map[uint64]bool{}
	writtenPeerLines(`{"input":{"command":"git commit -m \"a long commit message that should not count\"\nshort"}}`, written)
	if len(written) != 0 {
		t.Fatalf("commands or short lines counted: %d", len(written))
	}
}

func jsonEscape(s string) string {
	out := []rune{}
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\t':
			out = append(out, '\\', 't')
		case '\n':
			out = append(out, '\\', 'n')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
