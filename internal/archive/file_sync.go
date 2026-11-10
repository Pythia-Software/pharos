package archive

import "github.com/gbdubs/pharos/internal/platform/filesync"

// syncFileData flushes an open file's buffered writes to its drive with a
// plain fsync, before the file is renamed into place or recorded. It is not
// syncToDrive, which on macOS also empties the drive's own write cache
// (F_FULLFSYNC) and is kept for publishing retained state: substituting either
// for the other would change a write's cost or its durability.
var syncFileData = filesync.Flush
