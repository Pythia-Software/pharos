package archive

import "errors"

// errProcessIsolationUnsupported refuses to run a command whose cancellation
// would reach only the direct child. Ask and login-shell discovery need their
// whole process group stopped, and no substitute (parent-only Kill, taskkill,
// a new console process group or a Job Object) has been validated for them.
var errProcessIsolationUnsupported = errors.New("this platform cannot stop the command's process group")

// Callers configure through these seams so tests can refuse configuration
// without launching a child. Each runs before the command starts.
var (
	askProcessConfig        = configureAskProcess
	loginShellProcessConfig = configureLoginShellProcess
)
