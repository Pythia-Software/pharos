package archive

import (
	"os"
	"sort"
)

// Match the original inputs, not the now-removed captured payload. This small
// checkpoint lets a pending library stay empty after delivery until a source
// actually changes. A partial discovery can never suppress capture.
func captureDeliveryFingerprint(source SourceConfig, plan capturePlan) string {
	if !plan.complete || len(plan.warnings) > 0 {
		return ""
	}
	states := []string{jsonText(source)}
	for _, item := range plan.files {
		info, err := os.Stat(item.path)
		if err != nil {
			return ""
		}
		digest, _, err := fileDigest(item.path)
		if err != nil {
			return ""
		}
		states = append(states, jsonText([]any{item.path, info.Size(), info.ModTime().UnixNano(), digest}))
	}
	for _, item := range plan.databases {
		state, err := sqliteDependencyState(item.path)
		if err != nil {
			return ""
		}
		states = append(states, item.path+jsonText(state))
	}
	sort.Strings(states)
	return hashBytes([]byte(jsonText(states)))
}
