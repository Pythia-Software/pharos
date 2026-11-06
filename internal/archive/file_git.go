package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The Files page's Git views (docs/projects-and-outcomes.md, "The file
// viewer's checkout"): a file's contents and blame, the history of a file,
// directory or repository, a commit's message and diffs, and a directory's
// entries' last commits, read from the repository's chosen checkout
// (repository_checkout.go) when the view is opened. Every command is
// read-only, runs at low priority with a time limit and an output cap, a
// few at once; nothing it reads is stored. The person chose the checkout, so
// these are the only requests that run Git; every other page reads stored
// facts.

var (
	fileGitTimeout      = 15 * time.Second
	fileGitMaxBytes     = 1 << 20
	fileGitBlameMaxRows = 20000
	// fileGitSlots bounds the Git commands the viewer runs at once.
	fileGitSlots = make(chan struct{}, 2)
)

var fileGitHash = regexp.MustCompile(`^[0-9a-f]{4,64}$`)

// fileGitCommand runs git read-only in dir, as outcomeGitCommand does, and
// keeps at most limit bytes of its output: past it, the command is stopped
// and the output is truncated.
var fileGitCommand = func(ctx context.Context, dir string, limit int, args ...string) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, fileGitTimeout)
	defer cancel()
	select {
	case fileGitSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, false, errGitTimeout
	}
	defer func() { <-fileGitSlots }()
	name, full := "git", append([]string{"-C", dir, "-c", "core.fsmonitor=false"}, args...)
	if nice, err := exec.LookPath("nice"); err == nil {
		name, full = nice, append([]string{"-n", "10", "git"}, full...)
	}
	command := exec.CommandContext(ctx, name, full...)
	command.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat")
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := command.Start(); err != nil {
		return nil, false, err
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, int64(limit)+1))
	truncated := len(output) > limit
	if truncated {
		output = output[:limit]
		cancel()
	}
	waitErr := command.Wait()
	if truncated {
		return output, true, nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return nil, false, errGitTimeout
	}
	return output, false, errors.Join(readErr, waitErr)
}

// fileGitTarget is what a Git view reads: a repository's chosen checkout,
// a commit and a path in it.
type fileGitTarget struct {
	Project fileProject
	Dir     string
	Commit  string
	Path    string
}

type FileGitOptions struct {
	Project string
	// Path is a repository-relative path.
	Path string
	// Commit is a commit hash or its start; "" is the tree the scan read.
	Commit string
}

// fileGitTarget resolves a view's project, checkout, commit and path. A
// result with no Dir says why (choices, or the checkout's state).
func (c *Catalog) fileGitTarget(ctx context.Context, options FileGitOptions, needPath bool) (fileGitTarget, map[string]any, error) {
	projects, err := c.fileProjects(ctx)
	if err != nil {
		return fileGitTarget{}, nil, err
	}
	project, choices, err := projects.choose(ctx, options.Project)
	if err != nil || choices != nil {
		return fileGitTarget{}, map[string]any{"choices": choices}, err
	}
	if project.Kind != "repository" {
		return fileGitTarget{}, nil, fmt.Errorf("%s is a folder, not a Git repository", project.Name)
	}
	target := fileGitTarget{Project: project}
	if needPath || strings.TrimSpace(options.Path) != "" {
		relative, ok := repositoryRelative(options.Path)
		if !ok || relative == "" {
			return fileGitTarget{}, nil, fmt.Errorf("path must be a repository-relative path")
		}
		target.Path = relative
	}
	result := map[string]any{"project": project.value()}
	dir, reason, err := c.viewerCheckout(ctx, project.RepositoryID)
	if err != nil {
		return fileGitTarget{}, nil, err
	}
	result["checkout"] = map[string]any{"path": nilIfEmpty(dir), "status": map[bool]string{true: "set", false: "unset"}[dir != ""], "reason": nilIfEmpty(reason)}
	if dir == "" {
		return target, result, nil
	}
	commit := strings.ToLower(strings.TrimSpace(options.Commit))
	if commit != "" && !fileGitHash.MatchString(commit) {
		return fileGitTarget{}, nil, fmt.Errorf("commit must be a hash of at least 4 hex digits")
	}
	if commit == "" {
		// The tree the scan read, else the checkout's own HEAD: another
		// clone may not have fetched the scanned tip.
		var tip string
		_ = c.reader().QueryRowContext(ctx, `SELECT COALESCE(tree_commit,'') FROM git_repository_scans WHERE repository_id=? AND host_id=?`, project.RepositoryID, currentHost().ID).Scan(&tip)
		if full := resolveCommit(ctx, dir, tip); full != "" {
			target.Dir, target.Commit = dir, full
		} else if full := resolveCommit(ctx, dir, "HEAD"); full != "" {
			target.Dir, target.Commit = dir, full
			result["ref"] = "HEAD"
		}
	} else if full := resolveCommit(ctx, dir, commit); full != "" {
		target.Dir, target.Commit = dir, full
	}
	if target.Dir == "" {
		result["note"] = "The checkout does not hold commit " + defaultString(commit, "HEAD") + ": it may be on another branch or Mac, or rewritten since."
		return target, result, nil
	}
	result["commit"] = target.Commit
	return target, result, nil
}

// resolveCommit is the full hash of a commit in the checkout: a hash or its
// start, or HEAD; "" when the checkout does not hold it.
func resolveCommit(ctx context.Context, dir, commit string) string {
	if commit != "HEAD" && !fileGitHash.MatchString(commit) {
		return ""
	}
	output, _, err := fileGitCommand(ctx, dir, 200, "rev-parse", "--verify", "-q", commit+"^{commit}")
	if full := strings.TrimSpace(string(output)); err == nil && gitCommitID.MatchString(full) {
		return full
	}
	return ""
}

// repositoryRelative is a clean repository-relative path, "" for the root;
// a path that leaves the repository, or holds a NUL or line break, is not.
func repositoryRelative(value string) (string, bool) {
	relative := strings.Trim(strings.TrimSpace(value), "/")
	if relative == "" {
		return "", true
	}
	if strings.ContainsAny(relative, "\x00\n\r") || path.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", false
	}
	return relative, true
}

// FileContent is a file's text at a commit, the tree the scan read by
// default, read from the chosen checkout.
func (c *Catalog) FileContent(ctx context.Context, options FileGitOptions) (map[string]any, error) {
	target, result, err := c.fileGitTarget(ctx, options, true)
	if err != nil || target.Dir == "" {
		return result, err
	}
	result["path"] = target.Path
	size, reason := fileGitSize(ctx, target)
	if reason != "" {
		result["note"] = reason
		return result, nil
	}
	result["size"] = size
	if size > int64(fileGitMaxBytes) {
		result["note"] = fmt.Sprintf("This file is %s, more than the viewer reads (%s).", formatSize(size), formatSize(int64(fileGitMaxBytes)))
		return result, nil
	}
	content, truncated, err := fileGitCommand(ctx, target.Dir, fileGitMaxBytes, "cat-file", "blob", target.Commit+":"+target.Path)
	if err != nil {
		return nil, fileGitError(err)
	}
	if bytes.IndexByte(content[:min(len(content), 8000)], 0) >= 0 {
		result["binary"] = true
		return result, nil
	}
	result["text"], result["truncated"] = string(content), truncated
	return result, nil
}

// fileGitSize is a file's size at the target's commit, or why it has none:
// not in that commit, or not a file.
func fileGitSize(ctx context.Context, target fileGitTarget) (int64, string) {
	kind, _, err := fileGitCommand(ctx, target.Dir, 100, "cat-file", "-t", target.Commit+":"+target.Path)
	if err != nil {
		return 0, target.Path + " is not in commit " + target.Commit[:7] + ": deleted, renamed or not yet added."
	}
	if strings.TrimSpace(string(kind)) != "blob" {
		return 0, target.Path + " is a " + strings.TrimSpace(string(kind)) + " in commit " + target.Commit[:7] + ", not a file."
	}
	output, _, err := fileGitCommand(ctx, target.Dir, 100, "cat-file", "-s", target.Commit+":"+target.Path)
	size, parseErr := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || parseErr != nil {
		return 0, "Git could not read the size of " + target.Path + "."
	}
	return size, ""
}

func fileGitError(err error) error {
	if errors.Is(err, errGitTimeout) {
		return fmt.Errorf("git took too long to read the checkout")
	}
	return fmt.Errorf("git could not read the checkout: %w", err)
}

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(bytes)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", bytes)
}

// blameLine is one line of git blame --porcelain: its commit and text.
type blameLine struct {
	Hash, Text string
}

// blameCommit is what blame says of a commit: who wrote it, when, and its
// subject, none of which is stored.
type blameCommit struct {
	Author, AuthorAt, Summary string
}

// parseBlame reads git blame --porcelain output.
func parseBlame(output string) ([]blameLine, map[string]*blameCommit) {
	lines, commits := []blameLine{}, map[string]*blameCommit{}
	var current *blameCommit
	var hash string
	for _, line := range strings.Split(output, "\n") {
		if text, ok := strings.CutPrefix(line, "\t"); ok {
			lines = append(lines, blameLine{Hash: hash, Text: text})
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 && gitCommitID.MatchString(fields[0]) {
			hash = fields[0]
			if current = commits[hash]; current == nil {
				current = &blameCommit{}
				commits[hash] = current
			}
			continue
		}
		if current == nil {
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "author":
			current.Author = value
		case "author-time":
			if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
				current.AuthorAt = time.Unix(seconds, 0).UTC().Format(time.RFC3339)
			}
		case "summary":
			current.Summary = value
		}
	}
	return lines, commits
}

// FileBlame is the commit that last changed each line of a file, grouped
// into runs of lines, and for each commit the sessions linked to it, each
// opening where it made the commit.
func (c *Catalog) FileBlame(ctx context.Context, options FileGitOptions) (map[string]any, error) {
	target, result, err := c.fileGitTarget(ctx, options, true)
	if err != nil || target.Dir == "" {
		return result, err
	}
	result["path"] = target.Path
	size, reason := fileGitSize(ctx, target)
	if reason != "" {
		result["note"] = reason
		return result, nil
	}
	if size > int64(fileGitMaxBytes) {
		result["note"] = fmt.Sprintf("This file is %s, more than blame reads (%s).", formatSize(size), formatSize(int64(fileGitMaxBytes)))
		return result, nil
	}
	output, truncated, err := fileGitCommand(ctx, target.Dir, 8*fileGitMaxBytes, "blame", "--porcelain", "--no-textconv", target.Commit, "--", target.Path)
	if err != nil {
		return nil, fileGitError(err)
	}
	if truncated {
		result["note"] = "Blame of this file is too long to show."
		return result, nil
	}
	lines, commits := parseBlame(string(output))
	if len(lines) > fileGitBlameMaxRows {
		result["note"] = fmt.Sprintf("This file has %d lines, more than blame shows (%d).", len(lines), fileGitBlameMaxRows)
		return result, nil
	}
	text := make([]string, len(lines))
	groups := []map[string]any{}
	for index, line := range lines {
		text[index] = line.Text
		if index == 0 || lines[index-1].Hash != line.Hash {
			groups = append(groups, map[string]any{"hash": line.Hash, "start": index + 1, "count": 0})
		}
		groups[len(groups)-1]["count"] = groups[len(groups)-1]["count"].(int) + 1
	}
	described := map[string]any{}
	for hash, commit := range commits {
		sessions, err := c.commitSessions(ctx, target.Project.RepositoryID, hash)
		if err != nil {
			return nil, err
		}
		var stored int
		if err := c.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM git_commits WHERE repository_id=? AND hash=?`, target.Project.RepositoryID, hash).Scan(&stored); err != nil {
			return nil, err
		}
		described[hash] = map[string]any{"author": commit.Author, "author_at": commit.AuthorAt, "summary": commit.Summary, "sessions": sessions, "read": stored > 0}
	}
	result["lines"], result["groups"], result["commits"] = text, groups, described
	return result, nil
}

// FileDiff is a commit's change to one file, or to every file without a
// path, as a unified diff against its first parent.
func (c *Catalog) FileDiff(ctx context.Context, options FileGitOptions) (map[string]any, error) {
	if strings.TrimSpace(options.Commit) == "" {
		return nil, fmt.Errorf("commit is required")
	}
	target, result, err := c.fileGitTarget(ctx, options, false)
	if err != nil || target.Dir == "" {
		return result, err
	}
	args, err := firstParentDiff(ctx, target, "-p", "-M")
	if err != nil {
		return nil, err
	}
	if target.Path != "" {
		args = append(args, "--", target.Path)
		result["path"] = target.Path
	}
	output, truncated, err := fileGitCommand(ctx, target.Dir, fileGitMaxBytes, args...)
	if err != nil {
		return nil, fileGitError(err)
	}
	result["diff"], result["truncated"] = string(output), truncated
	return result, nil
}

// firstParentDiff is the diff-tree command comparing a commit with its
// first parent, or with nothing for a first commit.
func firstParentDiff(ctx context.Context, target fileGitTarget, options ...string) ([]string, error) {
	parents, _, err := fileGitCommand(ctx, target.Dir, 4000, "rev-list", "--parents", "-n", "1", target.Commit)
	if err != nil {
		return nil, fileGitError(err)
	}
	args := append([]string{"diff-tree", "-r", "--no-commit-id", "--no-color", "--no-textconv", "--no-ext-diff"}, options...)
	if fields := strings.Fields(string(parents)); len(fields) > 1 {
		return append(args, fields[1], target.Commit), nil
	}
	return append(args, "--root", target.Commit), nil
}

// gitLogFormat is a commit's hash, parents, author, author time, subject
// and, with %B, message, separated by unit separators.
const gitLogFormat = "%H%x1f%P%x1f%an%x1f%aI%x1f%cI%x1f%s"

// gitLogCommit reads one commit of gitLogFormat.
func gitLogCommit(line string) map[string]any {
	fields := strings.Split(line, "\x1f")
	if len(fields) < 6 || !gitCommitID.MatchString(fields[0]) {
		return nil
	}
	return map[string]any{"hash": fields[0], "parents": strings.Fields(fields[1]), "author": fields[2], "author_at": fields[3], "committed_at": fields[4], "subject": fields[5]}
}

// fileLogLimit is the commits a page of history holds.
var fileLogLimit = 50

type FileLogOptions struct {
	Project string
	// Path is a file or directory, "" for the whole repository.
	Path   string
	Commit string
	Offset int
}

// FileLog is the history of a file, a directory or the repository, read
// from the checkout from a commit back, a page at a time, newest first.
// Each commit carries what the library knows of it: where it stands and
// the sessions linked to it.
func (c *Catalog) FileLog(ctx context.Context, options FileLogOptions) (map[string]any, error) {
	target, result, err := c.fileGitTarget(ctx, FileGitOptions{Project: options.Project, Path: options.Path, Commit: options.Commit}, false)
	if err != nil || target.Dir == "" {
		return result, err
	}
	offset := max(options.Offset, 0)
	args := []string{"log", "--format=" + gitLogFormat, "--no-color", "-n", strconv.Itoa(fileLogLimit + 1), "--skip=" + strconv.Itoa(offset), target.Commit}
	if target.Path != "" {
		args = append(args, "--", target.Path)
	}
	output, _, err := fileGitCommand(ctx, target.Dir, fileGitMaxBytes, args...)
	if err != nil {
		return nil, fileGitError(err)
	}
	commits := []map[string]any{}
	for _, line := range strings.Split(string(output), "\n") {
		if commit := gitLogCommit(line); commit != nil {
			commits = append(commits, commit)
		}
	}
	result["more"] = len(commits) > fileLogLimit
	if len(commits) > fileLogLimit {
		commits = commits[:fileLogLimit]
	}
	for _, commit := range commits {
		if err := c.storedCommitFacts(ctx, target.Project, commit); err != nil {
			return nil, err
		}
	}
	result["path"], result["offset"], result["commits"] = target.Path, offset, commits
	return result, nil
}

// storedCommitFacts adds what the library stored of a commit Git listed:
// whether the scan read it, where it stands and the sessions linked to it.
func (c *Catalog) storedCommitFacts(ctx context.Context, project fileProject, commit map[string]any) error {
	hash := firstString(commit["hash"])
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT on_mainline,reverted_by FROM git_commits WHERE repository_id=? AND hash=?`, project.RepositoryID, hash)
	if err != nil {
		return err
	}
	commit["read"] = len(rows) > 0
	if len(rows) > 0 {
		commit["on_mainline"], commit["reverted_by"] = nullableBool(rows[0]["on_mainline"]), rows[0]["reverted_by"]
	}
	commit["sessions"], err = c.commitSessions(ctx, project.RepositoryID, hash)
	commit["url"] = filePageURL(project.Ref, "", "", hash)
	return err
}

// CommitDetail is a commit as the checkout holds it: its message, author,
// parents and the files it changed against its first parent, with their
// lines added and removed.
func (c *Catalog) CommitDetail(ctx context.Context, options FileGitOptions) (map[string]any, error) {
	if strings.TrimSpace(options.Commit) == "" {
		return nil, fmt.Errorf("commit is required")
	}
	target, result, err := c.fileGitTarget(ctx, FileGitOptions{Project: options.Project, Commit: options.Commit}, false)
	if err != nil || target.Dir == "" {
		return result, err
	}
	output, _, err := fileGitCommand(ctx, target.Dir, 64<<10, "log", "-1", "--no-color", "--format="+gitLogFormat+"%x1f%b", target.Commit)
	if err != nil {
		return nil, fileGitError(err)
	}
	commit := gitLogCommit(strings.SplitN(string(output), "\n", 2)[0])
	if commit == nil {
		return nil, fmt.Errorf("git could not read commit %s", target.Commit)
	}
	if fields := strings.SplitN(string(output), "\x1f", 7); len(fields) == 7 {
		commit["body"] = strings.TrimSpace(fields[6])
	}
	args, err := firstParentDiff(ctx, target, "--numstat", "--no-renames")
	if err != nil {
		return nil, err
	}
	stat, truncated, err := fileGitCommand(ctx, target.Dir, fileGitMaxBytes, args...)
	if err != nil {
		return nil, fileGitError(err)
	}
	files := []map[string]any{}
	for _, line := range strings.Split(string(stat), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		file := map[string]any{"path": fields[2], "binary": fields[0] == "-"}
		if added, err := strconv.Atoi(fields[0]); err == nil {
			file["lines_added"] = added
		}
		if removed, err := strconv.Atoi(fields[1]); err == nil {
			file["lines_removed"] = removed
		}
		files = append(files, file)
	}
	commit["files"], commit["files_truncated"] = files, truncated
	if err := c.storedCommitFacts(ctx, target.Project, commit); err != nil {
		return nil, err
	}
	result["commit"] = commit
	return result, nil
}

// fileLastCommitDepth bounds the commits read to find a directory's
// entries' last commits; an entry unchanged in them has none.
var fileLastCommitDepth = 1000

// LastCommits is the newest commit that changed each entry of a directory,
// from the tree's commit back, as a file list shows beside each name. One
// walk of history reads them; entries it does not reach have none.
func (c *Catalog) LastCommits(ctx context.Context, options FileGitOptions) (map[string]any, error) {
	target, result, err := c.fileGitTarget(ctx, options, false)
	if err != nil || target.Dir == "" {
		return result, err
	}
	args := []string{"log", "--no-color", "--no-renames", "--name-only", "--format=%x1e" + gitLogFormat, "-n", strconv.Itoa(fileLastCommitDepth), target.Commit}
	prefix := ""
	if target.Path != "" {
		args, prefix = append(args, "--", target.Path), target.Path+"/"
	}
	output, truncated, err := fileGitCommand(ctx, target.Dir, 4*fileGitMaxBytes, args...)
	if err != nil {
		return nil, fileGitError(err)
	}
	entries := map[string]any{}
	for _, record := range strings.Split(string(output), "\x1e") {
		header, names, _ := strings.Cut(record, "\n")
		commit := gitLogCommit(header)
		if commit == nil {
			continue
		}
		delete(commit, "parents")
		delete(commit, "committed_at")
		for _, name := range strings.Split(names, "\n") {
			inside, ok := strings.CutPrefix(name, prefix)
			if !ok || inside == "" {
				continue
			}
			child, _, _ := strings.Cut(inside, "/")
			if _, found := entries[child]; !found {
				entries[child] = commit
			}
		}
	}
	result["path"], result["entries"], result["truncated"] = target.Path, entries, truncated
	return result, nil
}
