package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

type teamsContentCandidate struct {
	ConversationID, NativeID, ThreadToken, ContentToken, ComponentToken, Origin, Input string
	Digest                                                                             [32]byte
}
type teamsContentInventory struct {
	teamsV2Inventory
	Candidates []teamsContentCandidate
}

// teamsCodexRollouts enumerates exactly the source adapter's accepted files.
// Any compressed, symlinked, unreadable or excessively large inventory is
// unavailable rather than silently treated as complete.
func teamsCodexRollouts(source SourceConfig) ([]string, error) {
	a := jsonlAdapter{baseAdapter: baseAdapter{config: source}, provider: "codex"}
	var files []string
	err := filepath.WalkDir(source.Path, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errTeamsSourceUnavailable
		}
		if d.IsDir() {
			return nil
		}
		if a.transcriptName(path) && a.accept(path) {
			if !strings.HasSuffix(strings.ToLower(path), ".jsonl") {
				return errTeamsSourceUnavailable
			}
			files = append(files, filepath.Clean(path))
			if len(files) > 30000 {
				return errTeamsSourceUnavailable
			}
		}
		return nil
	})
	if err != nil {
		return nil, errTeamsSourceUnavailable
	}
	sort.Strings(files)
	return files, nil
}

// A complete content inventory is admitted only for a single, currently
// mapped Codex source with an exact file/proof bijection. Every component
// carries its own full-file evidence atom and a dependency on the thread's
// privacy atom. This is a local observation; the server's closure remains
// authoritative before any content read or model handoff.
func (c *Catalog) teamsObserveV2Content(ctx context.Context, cfg Config, d LibraryDescriptor, credential *teams.EnrollmentCredential) (teamsContentInventory, error) {
	var out teamsContentInventory
	if len(cfg.Sources) != 1 || !cfg.Sources[0].Enabled || !isCodexSource(cfg.Sources[0].Kind) {
		return out, errTeamsSourceUnavailable
	}
	source := cfg.Sources[0]
	before, err := teamsCodexRollouts(source)
	if err != nil || len(before) == 0 {
		return out, errTeamsSourceUnavailable
	}
	base, err := c.teamsObserveV2Codex(ctx, cfg, d, credential)
	if err != nil || base.Coverage.IdentityAmbiguous != 0 || base.Coverage.OverlapUnresolved != 0 || len(base.Items) != len(before) {
		return out, errTeamsSourceUnavailable
	}
	key := credential.SourceKey()
	defer clear(key)
	account, err := teams.SourceWorkNamespace(key, "codex")
	if err != nil {
		return out, err
	}
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var sourceRev, privacyRev int64
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&sourceRev); err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&privacyRev); err != nil {
		return out, err
	}
	generation, err := readCacheGenerations(ctx, tx)
	if err != nil || sourceRev != base.SourceRevision || privacyRev != base.PrivacyRevision || generation.all != base.Generation {
		return out, errTeamsSnapshotChanged
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.native_id,p.origin FROM conversations c JOIN teams_native_source_proofs p ON p.conversation_id=c.id JOIN teams_source_bindings b ON b.binding_id=p.binding_id AND b.superseded_at='' WHERE c.provider='codex' AND b.host_id=? AND b.source_name=? ORDER BY c.id,p.origin`, currentHost().ID, source.Name)
	if err != nil {
		return out, err
	}
	type candidate struct{ id, native, origin string }
	var found []candidate
	for rows.Next() {
		var x candidate
		if err = rows.Scan(&x.id, &x.native, &x.origin); err != nil {
			rows.Close()
			return out, err
		}
		found = append(found, x)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	if len(found) != len(before) {
		return out, errTeamsSourceUnavailable
	}
	byThread := map[string]int{}
	for i, item := range base.Items {
		byThread[item.Token] = i
	}
	seenFiles := map[string]bool{}
	seenIDs := map[string]bool{}
	var added []sourcewire.Item
	for _, v := range found {
		if seenIDs[v.id] || seenFiles[filepath.Clean(v.origin)] {
			return out, errTeamsSourceUnavailable
		}
		seenIDs[v.id] = true
		seenFiles[filepath.Clean(v.origin)] = true
		thread, e := teams.SourceToken(key, "codex", account, v.native)
		if e != nil {
			return out, e
		}
		index, ok := byThread[thread]
		if !ok || base.Items[index].Protection == "unavailable" {
			return out, errTeamsSourceUnavailable
		}
		messages, digest, e := teamsCodexContentFile(source, v.origin, v.native)
		if e != nil {
			return out, e
		}
		match, e := teamsCodexIndexedContentMatches(ctx, tx, v.id, messages)
		if e != nil || !match {
			return out, errTeamsSourceUnavailable
		}
		privacy, e := readTeamsPrivacy(ctx, tx, v.id)
		if e != nil {
			return out, e
		}
		input, e := teamsManualReviewInput(ctx, tx, v.id)
		if e != nil {
			return out, e
		}
		protection := "unknown"
		if privacy.UserPrivate {
			protection = "private"
		} else if privacy.ClassifierState == "clear" && privacy.ClassifierReviewed && privacy.ClassifierInput == input {
			protection = "public"
		}
		base.Items[index].Protection = protection
		content, e := teams.SourceToken(key, "codex-content", account, v.native)
		if e != nil {
			return out, e
		}
		witness, e := teams.SourceWitness(key, "codex", "codex_transcript", v.native)
		if e != nil {
			return out, e
		}
		revision, e := teams.SourceEvidenceRevision(key, content, "codex_transcript", 1, digest[:])
		if e != nil {
			return out, e
		}
		component, e := teams.SourceToken(key, "codex-component", account, v.native)
		if e != nil {
			return out, e
		}
		componentWitness, e := teams.SourceWitness(key, "codex", "content_component", v.native)
		if e != nil {
			return out, e
		}
		added = append(added, sourcewire.Item{Kind: "content_evidence", Token: content, Witness: witness, Protection: protection, Profile: &sourcewire.Profile{ID: "codex_transcript", Version: 1}, EvidenceRevision: revision, Dependencies: []string{thread}}, sourcewire.Item{Kind: "content_component", Token: component, Witness: componentWitness, Protection: protection, Dependencies: []string{content}})
		out.Candidates = append(out.Candidates, teamsContentCandidate{v.id, v.native, thread, content, component, v.origin, input, digest})
	}
	for _, path := range before {
		if !seenFiles[path] {
			return out, errTeamsSourceUnavailable
		}
	}
	after, err := teamsCodexRollouts(source)
	if err != nil || len(before) != len(after) {
		return out, errTeamsSnapshotChanged
	}
	for i := range before {
		if before[i] != after[i] {
			return out, errTeamsSnapshotChanged
		}
	}
	base.Items = append(base.Items, added...)
	sort.Slice(base.Items, func(i, j int) bool { return base.Items[i].Token < base.Items[j].Token })
	base.Complete = true
	if err = tx.Commit(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return out, err
	}
	out.teamsV2Inventory = base
	// Ensure the ordinary page encoder accepts every component before returning.
	if _, err = json.Marshal(out.Items); err != nil {
		return teamsContentInventory{}, err
	}
	return out, nil
}
