//go:build !darwin

package archive

import "context"

type teamsSourceFileStamp struct {
	device, inode, rootDevice, rootInode      uint64
	changeSeconds, changeNanos, size, mtimeNS int64
}

func teamsSourceWalk(context.Context, SourceConfig) (map[string]teamsSourceFileStamp, error) {
	return nil, errTeamsSourceUnavailable
}
