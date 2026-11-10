package archive

import (
	"os"
	"sync"
	"testing"
)

// foreignMetadataInfo is a real FileInfo whose system metadata is not the
// native stat result an observation needs.
type foreignMetadataInfo struct{ os.FileInfo }

func (foreignMetadataInfo) Sys() any { return struct{}{} }

// deviceTable makes deviceOf, and volumeIdentity's fallback, observe the
// metadata set for a path. A path without an entry is stat'ed for real but
// reports foreign metadata, so its device cannot be observed.
type deviceTable struct {
	mu    sync.Mutex
	paths map[string]os.FileInfo
}

func stubDevices(t *testing.T) *deviceTable {
	t.Helper()
	table := &deviceTable{paths: map[string]os.FileInfo{}}
	override(t, &statDevice, func(path string) (os.FileInfo, error) {
		table.mu.Lock()
		info, ok := table.paths[path]
		table.mu.Unlock()
		if ok {
			return info, nil
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		return foreignMetadataInfo{info}, nil
	})
	return table
}

// set gives path the metadata info, or makes its device unobservable for nil.
func (d *deviceTable) set(path string, info os.FileInfo) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if info == nil {
		delete(d.paths, path)
	} else {
		d.paths[path] = info
	}
}
