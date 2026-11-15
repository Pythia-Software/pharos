package sourcepath

import "testing"

func TestCutLegacyPOSIX(t *testing.T) {
	for _, tc := range []struct {
		name, root, suffix string
		ok                 bool
	}{
		{"/Users/alice/logs", "/Users/alice/logs", "", true},
		{"", "", "", true},
		{"logs/a", "", "", false},
		{"/logs/a", "/logs", "a", true},
		{"/logs-other/a", "/logs", "", false},
		{"/logs/", "/logs/", "", true},
		{"/logs", "/logs/", "", false},
		{"/logs/a", "/logs/", "a", true},
		{"/logs//a", "/logs", "/a", true},
		{"/logs//a", "/logs//", "a", true},
		{"/logs/a", "/logs//", "", false},
		{"/logs//", "/logs//", "", true},
		{"/logs/./a", "/logs", "./a", true},
		{"/logs/a", "/logs/.", "", false},
		{"/logs/../outside", "/logs", "../outside", true},
		{"/a", "/", "a", true},
		{"//a", "/", "/a", true},
		{"relative/logs/a", "relative/logs", "a", true},
		{"~/logs/a", "~/logs", "a", true},
		{"/Users/Alice/a", "/Users/alice", "", false},
		{"/é/a", "/é", "a", true},
		{"/e\u0301/a", "/é", "", false},
		{`C:\Users\alice/logs/a`, `C:\Users\alice`, "logs/a", true},
		{`C:\Users\alice\logs`, `C:\Users\alice`, "", false},
		{"C:/logs/a", "C:/logs", "a", true},
		{`\\server\share/a`, `\\server\share`, "a", true},
		{"/logs/a\x00b", "/logs", "a\x00b", true},
	} {
		t.Run(tc.name+" under "+tc.root, func(t *testing.T) {
			suffix, ok := CutLegacyPOSIX(tc.name, tc.root)
			if suffix != tc.suffix || ok != tc.ok {
				t.Fatalf("CutLegacyPOSIX(%q, %q) = (%q, %v), want (%q, %v)", tc.name, tc.root, suffix, ok, tc.suffix, tc.ok)
			}
		})
	}
}

func TestJoinLegacyPOSIX(t *testing.T) {
	for _, tc := range []struct{ root, suffix, want string }{
		{"/Users/alice/logs", "p/a.jsonl", "/Users/alice/logs/p/a.jsonl"},
		{"/logs//", "", "/logs"},
		{"relative/logs", "a/../b", "relative/logs/b"},
		{"~/logs", "a", "~/logs/a"},
		{"", "", ""},
		{"/", "a", "/a"},
		{`C:\Users\alice`, "logs/a", `C:\Users\alice/logs/a`},
		{"C:/logs", "a", "C:/logs/a"},
		{`\\server\share`, "a", `\\server\share/a`},
		{"/É", "e\u0301:A", "/É/e\u0301:A"},
	} {
		if got := JoinLegacyPOSIX(tc.root, tc.suffix); got != tc.want {
			t.Errorf("JoinLegacyPOSIX(%q, %q) = %q, want %q", tc.root, tc.suffix, got, tc.want)
		}
	}
}
