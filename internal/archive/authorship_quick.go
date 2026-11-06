package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// Frequent work is bounded independently of the daily classifier. Neither a
// missing checkpoint nor historical changes can promote it to a full pass.
var (
	authorshipQuickLimit    = 250
	authorshipQuickBudget   = 10 * time.Second
	authorshipExactBudget   = 2 * time.Second
	authorshipQuickMaxBytes = 4 << 20
)

const authorshipQuickVersion = authorshipVersion + "/quick-v1"

const authorshipQuickDDL = `
CREATE TABLE IF NOT EXISTS authorship_pending(message_id TEXT PRIMARY KEY, revision TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS authorship_message_state(message_id TEXT PRIMARY KEY, source_hash TEXT NOT NULL, version TEXT NOT NULL, provisional INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS authorship_provisional ON authorship_message_state(message_id) WHERE provisional=1;
`

// Source identity deliberately excludes derived relationships: those can be
// reconsidered overnight without downgrading unchanged refined messages.
func authorshipSourceHash(input authorInput) string {
	return hashBytes([]byte(jsonText([]any{input.ID, input.ConversationID, input.WorkspaceID, input.Text, input.Sender, formatTime(input.SentAt)})))
}

// The visible ledger may be current provisionally while its full generation
// stays behind. Callers that require historical refinement use the latter.
func (c *Catalog) authorshipEstimateGenerations() (want, built string, err error) {
	want, built, err = c.authorshipGenerations()
	if err != nil || want == built {
		return
	}
	quick, err := c.metaValue(context.Background(), "authorship_quick_generation")
	if err != nil {
		return "", "", err
	}
	if quick != "" {
		built = quick
	}
	return want, built, nil
}

// UpdateAuthorship performs one frequent pass, never draining an unbounded
// backlog or launching the full classifier. A deadline leaves queued work for
// the next pass; caller cancellation remains an interruption.
func (c *Catalog) UpdateAuthorship(parent context.Context, generation string) (failure error) {
	defer func() {
		if failure == nil && parent.Err() == nil {
			c.authorship.mu.Lock()
			c.authorship.estimatesAttempted = true
			c.authorship.mu.Unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(parent, authorshipQuickBudget)
	defer cancel()
	err := c.withAuthorshipLease(func() error {
		representative, err := c.authorshipRepresentatives(ctx)
		if err != nil {
			return err
		}
		if err := c.refreshAuthorshipMirrors(ctx, representative); err != nil {
			return err
		}
		pending, err := c.authorshipPending(ctx)
		if err != nil {
			return err
		}
		if !pending {
			return c.publishAuthorshipEstimates(ctx, generation)
		}
		exactCtx, stop := context.WithTimeout(ctx, authorshipExactBudget)
		exact, err := c.tryExactAuthorship(exactCtx, generation)
		if err != nil && exactCtx.Err() != nil {
			err = exactCtx.Err()
		}
		stop()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if exact && err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := c.runAuthorshipLanes(ctx, representative); err != nil {
			return err
		}
		return c.publishAuthorshipEstimates(ctx, generation)
	})
	if parent.Err() != nil {
		return parent.Err()
	}
	if ctx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}

// An exact append is an optimization, under a separate short deadline. The
// fallback below never reads the agent corpus or discovers launches.
func (c *Catalog) tryExactAuthorship(ctx context.Context, generation string) (bool, error) {
	run, err := c.incrementalAuthorship(ctx)
	if err != nil {
		return false, err
	}
	if run.classifier == nil || len(run.inputs) > max(1, authorshipQuickLimit) {
		return false, nil
	}
	size := 0
	for _, input := range run.inputs {
		size += len(input.Text)
	}
	if size > authorshipQuickMaxBytes {
		return false, nil
	}
	results, err := classifyAuthorship(ctx, c.reader(), run.inputs, run.classifier)
	if err != nil {
		return false, err
	}
	plan, err := c.planAuthorshipScope(ctx, results, run.launches, &run.since)
	if err != nil {
		return false, err
	}
	// Use the same publication protocol as the full classifier.
	if err := c.writeTransaction(ctx, "invalidate authorship checkpoint", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key='authorship_state_version'`)
		return err
	}); err != nil {
		return false, err
	}
	if err := c.storeAuthorshipRows(ctx, plan, map[string]int{}); err != nil {
		return false, err
	}
	if err := c.storeAuthorshipState(ctx, run, results, generation, false); err != nil {
		return false, err
	}
	reportWork(ctx, "authorship mode: exact suffix", "message", len(results), len(results))
	return true, nil
}

func (c *Catalog) quickAuthorshipBatch(ctx context.Context, representative map[string]string, limit int, cutoff string, recent bool) (int, error) {
	predicate, order := "(sent_at<? OR sent_at IS NULL)", "queued_at,rowid"
	if recent {
		predicate, order = "sent_at>=?", "sent_at DESC,message_id"
	}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT p.message_id,p.revision,m.conversation_id,c.workspace_id,m.text,m.created_at,m.sender,m.role,m.kind,
  w.source_kind,c.harness,c.parent_id,s.source_hash,s.version,s.disposition,
  (SELECT l.parent_id FROM conversation_launches l WHERE l.child_id=c.id) launch_parent,
  (SELECT l.program FROM conversation_launches l WHERE l.child_id=c.id) launch_program
  FROM (SELECT message_id,revision FROM authorship_pending WHERE `+predicate+` ORDER BY `+order+` LIMIT ?) p
  LEFT JOIN messages m ON m.id=p.message_id LEFT JOIN conversations c ON c.id=m.conversation_id
  LEFT JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN authorship_message_state s ON s.message_id=p.message_id`, cutoff, limit)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	inputs := []authorInput{}
	ids := []string{}
	revisions := map[string]string{}
	outcomes := map[string][]any{}
	for _, row := range rows {
		id := asString(row["message_id"])
		revisions[id] = asString(row["revision"])
		input := authorInput{ID: id, ConversationID: asString(row["conversation_id"]), WorkspaceID: asString(row["workspace_id"]), Text: asString(row["text"]), Sender: asString(row["sender"]), SourceKind: asString(row["source_kind"]), Harness: asString(row["harness"])}
		at, valid := parseTime(asString(row["created_at"]))
		input.SentAt = at
		if asString(row["role"]) != "user" || asString(row["kind"]) != "message" || !valid || strings.TrimSpace(input.Text) == "" || representative[input.WorkspaceID] != "" {
			ids = append(ids, id) // Delete stale classifications of removed/ineligible rows.
			if asString(row["conversation_id"]) != "" {
				disposition := authorshipIneligible
				if representative[input.WorkspaceID] != "" {
					disposition = authorshipSuppressed
				}
				outcomes[id] = []any{id, authorshipSourceHash(input), authorshipVersion, 0, disposition, representative[input.WorkspaceID]}
			}
			continue
		}
		hash := authorshipSourceHash(input)
		if asString(row["disposition"]) == authorshipCounted && asString(row["source_hash"]) == hash && (asString(row["version"]) == authorshipVersion || asString(row["version"]) == authorshipQuickVersion) {
			continue
		}
		if len(input.Text) > authorshipQuickMaxBytes {
			outcomes[id] = []any{id, hash, authorshipQuickVersion, 1, authorshipDeferred, ""}
			ids = append(ids, id)
			continue
		}
		// A parent may be a continuation rather than a delegation. Leave this
		// historical inference to refinement instead of guessing from parent_id.
		if parent := asString(row["launch_parent"]); parent != "" {
			input.Launch = &conversationLaunch{parent: parent, program: asString(row["launch_program"])}
		}
		inputs = append(inputs, input)
		ids = append(ids, id)
	}
	sort.Slice(inputs, func(i, j int) bool {
		if !inputs[i].SentAt.Equal(inputs[j].SentAt) {
			return inputs[i].SentAt.Before(inputs[j].SentAt)
		}
		return inputs[i].ID < inputs[j].ID
	})
	classifier := newClassifier(nil)
	classifier.quick = true
	keys, conversations := []string{}, map[string]bool{}
	for _, input := range inputs {
		keys = append(keys, templateKey(input.Text))
		conversations[input.ConversationID] = true
	}
	templates, err := queryMapsContext(ctx, c.reader(), `SELECT key,sessions_json,days_json FROM authorship_templates WHERE key IN (SELECT value FROM json_each(?))`, jsonText(keys))
	if err != nil {
		return 0, err
	}
	for _, row := range templates {
		use := &templateUse{}
		if err := json.Unmarshal([]byte(asString(row["sessions_json"])), &use.sessions); err != nil {
			return 0, err
		}
		if err := json.Unmarshal([]byte(asString(row["days_json"])), &use.days); err != nil {
			return 0, err
		}
		classifier.templates[asString(row["key"])] = use
	}
	if len(inputs) > 0 {
		classifier.questions, err = c.loadAuthorshipQuestionsBefore(ctx, sortedNameIDs(conversations), inputs[len(inputs)-1].SentAt)
		if err != nil {
			return 0, err
		}
	}
	results := []authorResult{}
	reportWork(ctx, "estimating your writing", "message", 0, len(inputs))
	for i, input := range inputs {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		// Indexed lookup keeps typing-speed checks local to this conversation.
		var previous string
		err := c.reader().QueryRowContext(ctx, `SELECT created_at FROM messages WHERE conversation_id=? AND role='user' AND kind='message' AND created_at<? ORDER BY created_at DESC LIMIT 1`, input.ConversationID, formatTime(input.SentAt)).Scan(&previous)
		if err != nil && err != sql.ErrNoRows {
			return 0, err
		}
		if at, ok := parseTime(previous); ok {
			classifier.lastUserSent[input.ConversationID] = at
		}
		results = append(results, authorResult{Input: input, Spans: classifier.classify(input), Answers: classifier.answers})
		reportWork(ctx, "estimating your writing", "message", i+1, len(inputs))
	}
	// An empty non-nil ID slice means no output writes, not a whole-table diff.
	if ids == nil {
		ids = []string{}
	}
	plan, err := c.planAuthorshipScopeMode(ctx, results, nil, nil, ids)
	if err != nil {
		return 0, err
	}
	err = c.writeTransaction(ctx, "save writing estimates", func(tx *sql.Tx) error {
		// Check every token before writing: a source edit during classification
		// keeps the whole batch queued, including after an interrupted process.
		for id, revision := range revisions {
			var current string
			if err := tx.QueryRowContext(ctx, `SELECT revision FROM authorship_pending WHERE message_id=?`, id).Scan(&current); err != nil {
				return err
			}
			if current != revision {
				return nil
			}
		}
		if err := executeAuthorshipMutations(ctx, tx, plan, nil); err != nil {
			return err
		}
		for _, values := range outcomes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO authorship_message_state(message_id,source_hash,version,provisional,disposition,representative_id) VALUES(?,?,?,?,?,?) ON CONFLICT(message_id) DO UPDATE SET source_hash=excluded.source_hash,version=excluded.version,provisional=excluded.provisional,disposition=excluded.disposition,representative_id=excluded.representative_id`, values...); err != nil {
				return err
			}
		}
		for id, revision := range revisions {
			if _, err := tx.ExecContext(ctx, `DELETE FROM authorship_pending WHERE message_id=? AND revision=?`, id, revision); err != nil {
				return err
			}
		}
		// Any provisional output means the persisted full-classifier checkpoint
		// cannot claim these output rows are still fully analyzed.
		if len(plan) > 0 || len(outcomes) > 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key='authorship_state_version'`); err != nil {
				return err
			}
		}
		for key, value := range map[string]string{"authorship_quick_at": now(), "authorship_quick_version": authorshipQuickVersion} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
				return err
			}
		}
		return nil
	})
	return len(rows), err
}

// RefineAuthorship is the explicitly scheduled complete historical pass.
// It uses the same lease as estimates and preserves queued edits on failure.
func (c *Catalog) RefineAuthorship(ctx context.Context) (err error) {
	c.authorship.mu.Lock()
	if c.authorship.running {
		c.authorship.mu.Unlock()
		return errors.New("writing classification is already running")
	}
	c.authorship.running, c.authorship.refining = true, true
	c.authorship.mu.Unlock()
	defer func() {
		c.authorship.mu.Lock()
		c.authorship.running, c.authorship.refining = false, false
		c.authorship.lastError = ""
		if err != nil && ctx.Err() == nil {
			c.authorship.lastError = err.Error()
		}
		c.authorship.mu.Unlock()
	}()
	generation, _, err := c.authorshipGenerations()
	if err != nil {
		return err
	}
	return c.withAuthorshipLease(func() error { return c.rebuildAuthorship(ctx, generation, true) })
}

func (c *Catalog) authorshipPending(ctx context.Context) (bool, error) {
	var pending bool
	err := c.reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM authorship_pending)`).Scan(&pending)
	return pending, err
}

func (c *Catalog) authorshipQuickPending() bool {
	pending, err := c.authorshipPending(context.Background())
	return err != nil || pending
}

func (c *Catalog) publishAuthorshipEstimates(ctx context.Context, generation string) error {
	return c.writeTransaction(ctx, "complete writing estimates", func(tx *sql.Tx) error {
		var pending bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM authorship_pending)`).Scan(&pending); err != nil {
			return err
		}
		metadata := map[string]string{"authorship_quick_version": authorshipQuickVersion}
		if !pending {
			metadata["authorship_quick_generation"] = generation
			var deferred bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM authorship_message_state WHERE disposition='deferred')`).Scan(&deferred); err != nil {
				return err
			}
			if !deferred {
				metadata["authorship_complete_at"] = now()
			}
		}
		for key, value := range metadata {
			if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
				return err
			}
		}
		return nil
	})
}
