package archive

import (
	"database/sql/driver"
	"encoding/json"
	"strings"

	"modernc.org/sqlite"
)

// These deterministic identity functions are versioned with their indexes.
// Bump the index version if classification semantics change, so recorded keys
// are rebuilt rather than silently retaining an earlier function's results.
var machineCallIndexes = []string{
	`CREATE INDEX IF NOT EXISTS tool_calls_operation_v1_page_idx ON tool_calls(machine_operation_key(tool_name,mcp_server,program,subcommand),started_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS tool_calls_activity_v1_page_idx ON tool_calls(machine_activity(tool_category,command_category,tool_name,mcp_server,program),started_at DESC,id DESC)`,
}

// Families use the shell parser's program and named subcommand, not a regex
// over arbitrary invocation text. Flags, targets, and compound commands remain
// on the source calls; a family is not a claim that its workloads are identical.
func machineOperationKey(tool, server, program, subcommand string) string {
	parts := []string{"tool", server, tool}
	if program != "" {
		parts = []string{"command", program, subcommand}
	}
	encoded, _ := json.Marshal(parts)
	return string(encoded)
}

func machineOperationName(tool, server, program, subcommand string) string {
	if program != "" {
		return strings.TrimSpace(program + " " + subcommand)
	}
	if tool == "" {
		return "(Unidentified tool)"
	}
	if server != "" {
		return server + " / " + tool
	}
	return tool
}

func machineActivity(toolCategory, commandCategory, tool, server, program string) string {
	switch commandCategory {
	case "test":
		return "test"
	case "build":
		return "build"
	case "package":
		return "setup"
	case "vcs":
		return "git"
	case "read", "search":
		return "read"
	case "file-write":
		return "edit"
	case "network":
		return "network"
	case "process":
		return "process"
	}
	switch toolCategory {
	case "read", "search", "web":
		return "read"
	case "edit":
		return "edit"
	case "agent":
		return "delegate"
	case "plan":
		return "plan"
	case "user":
		return "user"
	case "meta":
		return "discovery"
	}
	if program != "" {
		return "command:" + program
	}
	return "tool:" + machineOperationName(tool, server, "", "")
}

func init() {
	text := func(args []driver.Value, index int) string { value, _ := args[index].(string); return value }
	_ = sqlite.RegisterDeterministicScalarFunction("machine_operation_key", 4, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		return machineOperationKey(text(args, 0), text(args, 1), text(args, 2), text(args, 3)), nil
	})
	_ = sqlite.RegisterDeterministicScalarFunction("machine_operation_name", 4, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		return machineOperationName(text(args, 0), text(args, 1), text(args, 2), text(args, 3)), nil
	})
	_ = sqlite.RegisterDeterministicScalarFunction("machine_activity", 5, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		return machineActivity(text(args, 0), text(args, 1), text(args, 2), text(args, 3), text(args, 4)), nil
	})
}
