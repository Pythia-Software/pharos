package archive

import "reflect"

// Re-read both the library and host source definitions immediately after the
// sharing lease is acquired. Cooperating Pharos source mutations share the
// lease; direct external file edits cannot participate in that ordering.
var teamsLoadSourceConfig = LoadConfig

func teamsSourceConfigCurrent(c Config) bool {
	if c.Path == "" {
		return false
	}
	current, err := teamsLoadSourceConfig(c.Path)
	return err == nil && reflect.DeepEqual(current.Sources, c.Sources)
}
