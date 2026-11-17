package archive

// teamsNumericCapabilities is the audited intersection of parser, SQL executor,
// result schema and automatic sharing. These fields are numeric metadata and may
// include explicitly private sources; they do not authorize content access.
var teamsNumericCapabilities = map[string]struct{ MetricID, Unit string }{
	"total_tokens":            {"captured_tokens", "tokens"},
	"output_tokens":           {"output_tokens", "tokens"},
	"reasoning_output_tokens": {"reasoning_output_tokens", "tokens"},
	"unclassified_tokens":     {"unclassified_tokens", "tokens"},
}
