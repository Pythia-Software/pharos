package archive

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func runClaudeImportCLI(ctx context.Context, config Config, args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: pharos claude-import {collect,status,watch} [--name NAME] [--job ID] [PATH ...]")
	}
	flags := flag.NewFlagSet("claude-import "+args[0], flag.ContinueOnError)
	name := flags.String("name", "claude-ai-imports", "stable Claude collection/source name")
	id := flags.String("job", "", "existing import job to repair")
	interval := flags.Duration("interval", 5*time.Second, "folder check interval for watch")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *interval < time.Second {
		return errors.New("watch interval must be at least one second")
	}
	collection, err := claudeImportName(*name)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	if args[0] == "status" {
		if flags.NArg() != 0 {
			return errors.New("use --job to select an import job")
		}
		if *id != "" {
			job, err := loadClaudeJob(config, collection, *id)
			if err != nil {
				return err
			}
			return encoder.Encode(job)
		}
		jobs, err := listClaudeImports(config)
		if err != nil {
			return err
		}
		return encoder.Encode(map[string]any{"jobs": jobs})
	}
	if args[0] == "collect" {
		job, err := collectClaudeExport(ctx, config, claudeImportRequest{Paths: flags.Args(), Name: collection, ID: *id})
		if job != nil {
			if e := encoder.Encode(job); err == nil {
				err = e
			}
		}
		if err == nil && job.State == "incomplete" {
			return errors.New("Claude export is incomplete; downloaded parts were preserved; use --job to resume")
		}
		return err
	}
	if args[0] != "watch" || flags.NArg() != 1 || *id != "" {
		return errors.New("watch expects one export inbox folder; collect accepts a manifest or files")
	}
	folder := expandPath(flags.Arg(0))
	info, err := os.Stat(folder)
	if err != nil || !info.IsDir() {
		return errors.New("watch expects a readable export inbox folder")
	}
	// The watcher uses the same collector. It wakes on portable directory
	// observations and stops with the command, rather than adding a new daemon.
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	previous := ""
	// A collected manifest is not re-verified each time an unrelated download
	// changes the folder; it is checked again when its own file changes.
	collected := map[string]string{}
	for {
		files, err := claudeImportPaths([]string{folder})
		if err != nil {
			return err
		}
		var state []string
		stamps := map[string]string{}
		for _, file := range files {
			info, err := os.Stat(file)
			if err != nil {
				return err
			}
			stamps[file] = fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
			state = append(state, file+":"+stamps[file])
		}
		fingerprint := hashBytes([]byte(strings.Join(state, "\n")))
		if fingerprint != previous {
			previous = fingerprint
			children := map[string]bool{}
			for _, file := range files {
				if !strings.EqualFold(filepath.Ext(file), ".json") {
					continue
				}
				manifest, _, ok, err := readClaudeManifest(file)
				if err != nil || !ok {
					continue
				}
				for _, part := range manifest.DataFiles {
					if claudeExportPath(part.Filename) && strings.EqualFold(filepath.Ext(part.Filename), ".json") {
						children[filepath.Join(filepath.Dir(file), filepath.FromSlash(part.Filename))] = true
					}
				}
			}
			for _, file := range files {
				if children[file] || collected[file] == stamps[file] {
					continue
				}
				if !strings.EqualFold(filepath.Ext(file), ".json") {
					continue
				}
				_, _, manifest, err := readClaudeManifest(file)
				if err != nil {
					// An inbox can be Downloads. Report only files that claim to
					// be Claude manifests, not every unrelated JSON document.
					if !manifest && !strings.HasPrefix(strings.ToLower(filepath.Base(file)), "manifest-") {
						continue
					}
					if e := encoder.Encode(map[string]any{"file": file, "error": err.Error()}); e != nil {
						return e
					}
					continue
				}
				if !manifest {
					continue
				}
				// Check before lease admission too: an inbox watcher can stay
				// alive while the library's volume is ejected.
				if err := config.checkLibrary(volumeIdentity); err != nil {
					return err
				}
				var job *claudeImportJob
				err = runCLIOperation(config, "claude-import", OperationSpec{Kind: "capture.import", Variant: "claude-ai"}, func(context.Context, *Op) error {
					var collectErr error
					job, collectErr = collectClaudeExport(ctx, config, claudeImportRequest{Paths: []string{file}, Name: collection})
					return collectErr
				})
				if job != nil {
					if err == nil && job.State == "collected" {
						collected[file] = stamps[file]
					}
					if e := encoder.Encode(job); e != nil {
						return e
					}
				}
				if err != nil {
					if e := encoder.Encode(map[string]any{"file": file, "error": err.Error()}); e != nil {
						return e
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func runClaudeImportCommand(config Config, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) > 0 && (args[0] == "status" || args[0] == "watch") {
		return runClaudeImportCLI(ctx, config, args, os.Stdout)
	}
	return runCLIOperation(config, "claude-import", OperationSpec{Kind: "capture.import", Variant: "claude-ai"}, func(context.Context, *Op) error { return runClaudeImportCLI(ctx, config, args, os.Stdout) })
}
