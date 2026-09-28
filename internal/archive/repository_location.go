package archive

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var repositoryRoots sync.Map

func repositoryRootCommit(location string) string {
	if location == "" {
		return ""
	}
	if value, ok := repositoryRoots.Load(location); ok {
		return value.(string)
	}
	if info, err := os.Stat(location); err != nil || !info.IsDir() {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "git", "-C", location, "rev-list", "--reverse", "--max-parents=0", "HEAD").Output()
	if err != nil {
		return ""
	}
	roots := strings.Fields(string(output))
	if len(roots) == 0 {
		return ""
	}
	repositoryRoots.Store(location, roots[0])
	return roots[0]
}

// repositoryFromLocation avoids confusing a worktree or branch directory with a repository.
func repositoryFromLocation(location, remote string) map[string]any {
	return repositoryAt(location, remote, true)
}

// repositoryFromName names the repository without asking Git, for a location
// recorded on another Mac.
func repositoryFromName(location, remote string) map[string]any {
	return repositoryAt(location, remote, false)
}

func repositoryAt(location, remote string, lookup bool) map[string]any {
	root := ""
	if info, err := os.Stat(location); lookup && location != "" && err == nil && info.IsDir() {
		git := func(args ...string) string {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, "git", append([]string{"-C", location}, args...)...).Output()
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(output))
		}
		if remote == "" {
			remote = git("config", "--get", "remote.origin.url")
		}
		if common := git("rev-parse", "--path-format=absolute", "--git-common-dir"); common != "" {
			root = filepath.Dir(common)
		}
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(location)), "/")
	layout := ""
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "conductor" && parts[i+1] == "workspaces" {
			layout = parts[i+2]
		}
	}
	if filepath.Base(filepath.Dir(location)) == ".conductor" {
		layout = filepath.Base(filepath.Dir(filepath.Dir(location)))
	}
	name := ""
	if remote != "" {
		name = strings.TrimSuffix(strings.TrimRight(remote, "/"), ".git")
		if i := strings.LastIndexAny(name, "/:"); i >= 0 {
			name = name[i+1:]
		}
	}
	if name == "" && root != "" {
		name = filepath.Base(root)
	}
	if name == "" {
		name = layout
	}
	if name == "" {
		return nil
	}
	locations := uniqueStrings([]string{root, location})
	aliases := []string{}
	if remote != "" {
		aliases = append(aliases, remote)
	}
	commit := ""
	if lookup {
		commit = repositoryRootCommit(location)
	}
	return map[string]any{"display_name": name, "canonical_remote": nilIfEmpty(remote), "normalized_remote": normalizeRepositoryRemote(remote), "root_commit": commit, "aliases": aliases, "local_locations": locations}
}
