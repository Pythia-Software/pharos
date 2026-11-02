package archive

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Refusals, and what they block:
//
//   - A pinned refusal (a confirmed refusal of a request of one item) is
//     the material's own: its content key (source message hash and target
//     text hash) goes into the refusal registry, a library-wide append-only
//     file, and no job of any dataset or route sends that content again.
//   - A co-packed refusal (a refused request of several items) cannot be
//     attributed to one item: each item is blocked by its item ID only, in
//     its dataset and in datasets that import from it, never by content.
//   - A suspected refusal holds the item's request in other jobs while it
//     is the item's latest result in its own job; once that job resolves it
//     (reconcile --retry-suspected-refusals), the request can be sent.
//
// The registry is read fresh before a request is sent; one that cannot be
// read or parsed stops the run, so refusals never silently stop applying.

// labelingRefusalRegistryFile is the registry's default name, in the
// directory that holds the datasets.
const labelingRefusalRegistryFile = "labeling-refusals.jsonl"

// labelingDefaultRegistry is the registry beside a dataset directory.
func labelingDefaultRegistry(dir string) string {
	abs, _ := filepath.Abs(dir)
	return filepath.Join(filepath.Dir(abs), labelingRefusalRegistryFile)
}

// labelingRefusalEntry is one registry line.
type labelingRefusalEntry struct {
	Key        string `json:"key"`
	Dataset    string `json:"dataset"`
	Manifest   string `json:"dataset_manifest_sha256"`
	Job        string `json:"job"`
	Item       string `json:"item"`
	Attempt    string `json:"attempt"`
	RecordedAt string `json:"recorded_at"`
}

var labelingRegistryMu sync.Mutex

// readLabelingRegistry returns the registry's content keys and its number
// of entries. A missing file is empty; a line that does not parse is an
// error.
func readLabelingRegistry(path string) (map[string]bool, int, error) {
	keys := map[string]bool{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return keys, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("refusal registry %s: %w", path, err)
	}
	entries := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1<<16), 1<<20)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var entry labelingRefusalEntry
		if err := json.Unmarshal([]byte(text), &entry); err != nil || entry.Key == "" {
			return nil, 0, fmt.Errorf("refusal registry %s line %d does not parse; repair it before running (refusals must not silently stop applying)", path, line)
		}
		keys[entry.Key] = true
		entries++
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("refusal registry %s: %w", path, err)
	}
	return keys, entries, nil
}

// appendLabelingRegistry appends entries durably (fsync) before the ledger
// records the refusal: a registry entry without a ledger row only blocks
// more, never less.
func appendLabelingRegistry(path string, entries []labelingRefusalEntry) error {
	if len(entries) == 0 {
		return nil
	}
	labelingRegistryMu.Lock()
	defer labelingRegistryMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("refusal registry %s: %w", path, err)
	}
	defer file.Close()
	var buffer bytes.Buffer
	for _, entry := range entries {
		data, _ := json.Marshal(entry)
		buffer.Write(append(data, '\n'))
	}
	if _, err := file.Write(buffer.Bytes()); err != nil {
		return fmt.Errorf("refusal registry %s: %w", path, err)
	}
	return file.Sync()
}

// labelingRefusalCheck is what blocks items now, for one job.
type labelingRefusalCheck struct {
	registry map[string]bool
}

// loadLabelingRefusalCheck reads the registry for a check. seen is the
// number of entries the job saw before (a registry that shrank was
// replaced or truncated: an error); it is updated.
func loadLabelingRefusalCheck(path string, seen *int) (*labelingRefusalCheck, error) {
	keys, entries, err := readLabelingRegistry(path)
	if err != nil {
		return nil, err
	}
	if seen != nil {
		if entries < *seen {
			return nil, fmt.Errorf("refusal registry %s holds %d entries but held %d before; it was truncated or replaced, restore it before running", path, entries, *seen)
		}
		*seen = entries
	}
	return &labelingRefusalCheck{registry: keys}, nil
}

// labelingItemBlock says why an item may not be sent by job now: "refused"
// (a pinned refusal of its content in the registry or this dataset, or a
// refusal of the item itself in any job of this dataset), "suspected" (its
// latest result in another job is a suspected refusal), or "".
func labelingItemBlock(q queryer, check *labelingRefusalCheck, job, item string) (string, error) {
	rows, err := queryMaps(q, `SELECT u.message_sha256||':'||i.target_hash key FROM items i JOIN units u ON u.id=i.unit_id WHERE i.id=?`, item)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("item %s is not in the dataset", item)
	}
	key := firstString(rows[0]["key"])
	if check.registry[key] {
		return itemRefused, nil
	}
	// This dataset's own ledger: the item refused in any job, or its
	// content refused in a request of one item.
	own, err := queryMaps(q, `SELECT 1 FROM item_results WHERE outcome='refused' AND item_id=? LIMIT 1`, item)
	if err != nil {
		return "", err
	}
	if len(own) > 0 {
		return itemRefused, nil
	}
	pinned, err := queryMaps(q, `SELECT 1 FROM item_results r JOIN requests q ON q.id=r.request_id JOIN items i ON i.id=r.item_id JOIN units u ON u.id=i.unit_id
		WHERE r.outcome='refused' AND q.items=1 AND u.message_sha256||':'||i.target_hash=? LIMIT 1`, key)
	if err != nil {
		return "", err
	}
	if len(pinned) > 0 {
		return itemRefused, nil
	}
	suspected, err := queryMaps(q, `SELECT 1 FROM (SELECT r.job,r.outcome,ROW_NUMBER() OVER (PARTITION BY r.job ORDER BY r.created_at DESC, r.rowid DESC) k
		FROM item_results r WHERE r.item_id=? AND r.job<>? AND r.outcome NOT IN ('refused_elsewhere','blocked_oversize')) WHERE k=1 AND outcome='refusal_suspected' LIMIT 1`, item, job)
	if err != nil {
		return "", err
	}
	if len(suspected) > 0 {
		return "suspected", nil
	}
	return "", nil
}

// applyLabelingRefusals settles a request before it is sent, inside tx:
// items a refusal blocks leave it with a refused_elsewhere result; if an
// item is only suspected elsewhere, the whole request is held (state
// waiting_suspicion, back to pending at the next run start). It returns how
// many items were removed, how many remain, and whether it is held.
func applyLabelingRefusals(tx *sql.Tx, check *labelingRefusalCheck, job, request string) (blocked, remaining int, held bool, err error) {
	rows, err := queryMaps(tx, `SELECT item_id,contract_hash FROM request_items WHERE request_id=? AND blocked IS NULL ORDER BY position`, request)
	if err != nil {
		return 0, 0, false, err
	}
	for _, row := range rows {
		item := firstString(row["item_id"])
		reason, err := labelingItemBlock(tx, check, job, item)
		if err != nil {
			return 0, 0, false, err
		}
		switch reason {
		case "":
			remaining++
		case "suspected":
			remaining++
			held = true
		default:
			blocked++
			if _, err := tx.Exec(`UPDATE request_items SET blocked=? WHERE request_id=? AND item_id=?`, reason, request, item); err != nil {
				return 0, 0, false, err
			}
			if err := insertLabelingRefusedElsewhere(tx, "refused-elsewhere:"+request, job, request, item, firstString(row["contract_hash"]), "blocked_by_refusal"); err != nil {
				return 0, 0, false, err
			}
		}
	}
	state := ""
	switch {
	case remaining == 0 && blocked > 0:
		state = requestRefusedElsewhere
	case held:
		state = requestWaitingSuspicion
	}
	if blocked > 0 || state != "" {
		if _, err := tx.Exec(`UPDATE requests SET items=?,state=CASE WHEN ?<>'' THEN ? ELSE state END,updated_at=?,note=? WHERE id=?`, remaining, state, state, now(),
			fmt.Sprintf("%d items blocked by a refusal; held for a suspicion elsewhere: %v", blocked, held), request); err != nil {
			return 0, 0, false, err
		}
	}
	return blocked, remaining, held, nil
}

// insertLabelingRefusedElsewhere records an item a refusal keeps from
// being sent: no labels, the reason as its issue.
func insertLabelingRefusedElsewhere(tx *sql.Tx, attempt, job, request, item, hash, reason string) error {
	result := labelingItemResult{ItemID: item, Outcome: itemRefusedElsewhere, Labels: []string{}, Uncertain: []string{}, Evidence: []labelingEvidence{},
		Issues: []string{reason}}
	return insertLabelingItemResult(tx, attempt, job, request, result, hash, "", "")
}

// labelingLedgerRefusals lists a dataset's refused item IDs (any job,
// pinned or co-packed) and its pinned refusals' content keys. A dataset
// importing from another takes that one's refused item IDs too.
func labelingLedgerRefusals(db *sql.DB) (items, pinned map[string]bool, err error) {
	items, pinned = map[string]bool{}, map[string]bool{}
	if !labelingHasLedger(db) {
		return items, pinned, nil
	}
	rows, err := queryMaps(db, `SELECT DISTINCT r.item_id, u.message_sha256||':'||i.target_hash key, COALESCE(q.items,0) n FROM item_results r
		JOIN items i ON i.id=r.item_id JOIN units u ON u.id=i.unit_id LEFT JOIN requests q ON q.id=r.request_id WHERE r.outcome='refused'`)
	if err != nil {
		return nil, nil, err
	}
	for _, row := range rows {
		items[firstString(row["item_id"])] = true
		if integer(row["n"]) == 1 {
			pinned[firstString(row["key"])] = true
		}
	}
	return items, pinned, nil
}

// labelingManifestHash is the SHA-256 of a dataset's manifest.json.
func labelingManifestHash(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, labelingManifestFile))
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}
