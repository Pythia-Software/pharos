package teams

import "testing"

func TestSourceTokenVector(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	got, err := SourceToken(key, "codex", "synthetic-account", "synthetic-conversation")
	if err != nil || got != "knhABoFGkiIWS5lojTjS5weYgzs1b4lahJBqOXOWSag" {
		t.Fatalf("%q: %v", got, err)
	}
	if _, err = SourceToken(key, "codex", "", "synthetic-conversation"); err == nil {
		t.Fatal("accepted missing account")
	}
	other, err := SourceToken(key, "codex", "synthetic-account", "synthetic-Conversation")
	if err != nil || got == other {
		t.Fatal("source identity lost case sensitivity")
	}
}
