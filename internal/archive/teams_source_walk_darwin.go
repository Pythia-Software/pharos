//go:build darwin

package archive

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Only local APFS is admitted. Other filesystems need their own reviewed
// identity and timestamp guarantees before they can establish census freshness.

type teamsSourceFileStamp struct {
	device, inode, rootDevice, rootInode      uint64
	changeSeconds, changeNanos, size, mtimeNS int64
}

func teamsSourceWalk(ctx context.Context, source SourceConfig) (map[string]teamsSourceFileStamp, error) {
	if !filepath.IsAbs(source.Path) || filepath.Clean(source.Path) != source.Path {
		return nil, errTeamsSourceUnavailable
	}
	fd, err := unix.Open(source.Path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errTeamsSourceUnavailable
	}
	root := &exportDir{fd: fd, path: source.Path}
	defer root.Close()
	var rootStat unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(fd, &rootStat) != nil || unix.Fstatfs(fd, &fs) != nil || strings.TrimRight(string(fs.Fstypename[:]), "\x00") != "apfs" || fs.Flags&unix.MNT_LOCAL == 0 {
		return nil, errTeamsSourceUnavailable
	}
	var named unix.Stat_t
	if unix.Lstat(source.Path, &named) != nil || named.Dev != rootStat.Dev || named.Ino != rootStat.Ino || named.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, errTeamsSourceUnavailable
	}
	files := map[string]teamsSourceFileStamp{}
	adapter := jsonlAdapter{baseAdapter: baseAdapter{config: source}, provider: "codex"}
	entries := 0
	var walk func(*exportDir, int) error
	walk = func(dir *exportDir, depth int) error {
		if depth > 64 {
			return errTeamsSourceUnavailable
		}
		var before unix.Stat_t
		var currentFS unix.Statfs_t
		if unix.Fstat(dir.fd, &before) != nil || before.Dev != rootStat.Dev || unix.Fstatfs(dir.fd, &currentFS) != nil || currentFS.Fsid != fs.Fsid || currentFS.Flags&unix.MNT_LOCAL == 0 || strings.TrimRight(string(currentFS.Fstypename[:]), "\x00") != "apfs" {
			return errTeamsSourceUnavailable
		}
		l, e := dir.list()
		if e != nil {
			return e
		}
		defer l.Close()
		for {
			names, readErr := l.next(256)
			for _, name := range names {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				entries++
				if entries > 100000 || name == "." || name == ".." || strings.Contains(name, "/") {
					return errTeamsSourceUnavailable
				}
				full := filepath.Join(dir.path, name)
				if len(full) > 4096 {
					return errTeamsSourceUnavailable
				}
				var st unix.Stat_t
				if unix.Fstatat(dir.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Dev != rootStat.Dev {
					return errTeamsSourceUnavailable
				}
				switch st.Mode & unix.S_IFMT {
				case unix.S_IFDIR:
					child, e := dir.sub(name)
					if e != nil {
						return e
					}
					var childStat unix.Stat_t
					if unix.Fstat(child.fd, &childStat) != nil || childStat.Dev != st.Dev || childStat.Ino != st.Ino {
						child.Close()
						return errTeamsSourceUnavailable
					}
					e = walk(child, depth+1)
					child.Close()
					if e != nil {
						return e
					}
					var after unix.Stat_t
					if unix.Fstatat(dir.fd, name, &after, unix.AT_SYMLINK_NOFOLLOW) != nil || after.Dev != st.Dev || after.Ino != st.Ino || after.Mode&unix.S_IFMT != unix.S_IFDIR {
						return errTeamsSourceUnavailable
					}
				case unix.S_IFREG:
					if adapter.transcriptName(full) && adapter.accept(full) {
						if !strings.HasSuffix(strings.ToLower(full), ".jsonl") || st.Size <= 0 || st.Size > 4<<20 || len(files) >= 500 {
							return errTeamsSourceUnavailable
						}
						files[full] = teamsSourceFileStamp{device: uint64(uint32(st.Dev)), inode: st.Ino, rootDevice: uint64(uint32(rootStat.Dev)), rootInode: rootStat.Ino, changeSeconds: st.Ctim.Sec, changeNanos: st.Ctim.Nsec, size: st.Size, mtimeNS: st.Mtim.Sec*1e9 + st.Mtim.Nsec}
					}
				default:
					return errTeamsSourceUnavailable
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				return readErr
			}
		}
		var after unix.Stat_t
		if unix.Fstat(dir.fd, &after) != nil || after.Dev != before.Dev || after.Ino != before.Ino || after.Mtim != before.Mtim || after.Ctim != before.Ctim {
			return errTeamsSnapshotChanged
		}
		return nil
	}
	if err = walk(root, 0); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errTeamsSourceUnavailable
	}
	if unix.Lstat(source.Path, &named) != nil || named.Dev != rootStat.Dev || named.Ino != rootStat.Ino || named.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, errTeamsSnapshotChanged
	}
	return files, nil
}
