package archive

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Refusals are global and permanent. A confirmed refusal (outcome refused)
// of an item, keyed by its source message hash and target text hash, keeps
// that material from being sent again by any job, on any route, in this
// dataset or any dataset prepared from it. A suspected refusal
// (refusal_suspected) blocks every other job too; only its own job may send
// it again, through reconcile --retry-suspected-refusals. The check runs
// when a job is created, when a request is leased and submitted, and when
// reconcile re-queues.

// labelingRefusalSource is another dataset whose refusals a job honours.
type labelingRefusalSource struct {
	Path           string `json:"path"`
	ManifestSHA256 string `json:"manifest_sha256"`
	Reason         string `json:"reason"`
}

// labelingRefusals holds refusal keys read from other datasets: confirmed
// ones, and suspected ones with the dataset and job that suspected them.
type labelingRefusals struct {
	confirmed map[string]bool
	suspected map[string]bool
}

func newLabelingRefusals() *labelingRefusals {
	return &labelingRefusals{confirmed: map[string]bool{}, suspected: map[string]bool{}}
}

// labelingManifestHash is the SHA-256 of a dataset's manifest.json.
func labelingManifestHash(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, labelingManifestFile))
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}

// labelingPredecessors follows a dataset's --splits-from lineage (the
// manifest's splits.inherited_from), nearest first.
func labelingPredecessors(dir string) ([]labelingRefusalSource, error) {
	sources := []labelingRefusalSource{}
	seen := map[string]bool{}
	for current := dir; ; {
		abs, err := filepath.Abs(current)
		if err != nil {
			return nil, err
		}
		seen[abs] = true
		data, err := os.ReadFile(filepath.Join(abs, labelingManifestFile))
		if err != nil {
			return nil, err
		}
		var manifest struct {
			Splits struct {
				InheritedFrom string `json:"inherited_from"`
			} `json:"splits"`
		}
		json.Unmarshal(data, &manifest)
		previous := manifest.Splits.InheritedFrom
		if previous == "" {
			return sources, nil
		}
		if previous, err = filepath.Abs(previous); err != nil {
			return nil, err
		}
		if seen[previous] {
			return sources, nil
		}
		hash, err := labelingManifestHash(previous)
		if err != nil {
			return nil, fmt.Errorf("the dataset %s was prepared from %s (--splits-from), whose refusals it must honour, but that dataset cannot be read: %w", abs, previous, err)
		}
		sources = append(sources, labelingRefusalSource{Path: previous, ManifestSHA256: hash, Reason: "splits-from lineage"})
		current = previous
	}
}

// labelingRefusalSourcesFor lists the datasets a job in dir honours: the
// --splits-from lineage of dir, and of each extra dataset, the extra
// datasets themselves (--refusals-from, or the dataset imported from).
func labelingRefusalSourcesFor(dir string, extra []string, reason string) ([]labelingRefusalSource, error) {
	sources, err := labelingPredecessors(dir)
	if err != nil {
		return nil, err
	}
	self, _ := filepath.Abs(dir)
	seen := map[string]bool{self: true}
	for _, source := range sources {
		seen[source.Path] = true
	}
	for _, other := range extra {
		abs, err := filepath.Abs(other)
		if err != nil {
			return nil, err
		}
		chain, err := labelingPredecessors(abs)
		if err != nil {
			return nil, err
		}
		hash, err := labelingManifestHash(abs)
		if err != nil {
			return nil, err
		}
		for _, source := range append([]labelingRefusalSource{{Path: abs, ManifestSHA256: hash, Reason: reason}}, chain...) {
			if !seen[source.Path] {
				seen[source.Path] = true
				sources = append(sources, source)
			}
		}
	}
	return sources, nil
}

// loadLabelingRefusals reads the refusals of the given datasets. A
// recorded dataset that cannot be read stops the run: its refusals would
// otherwise silently stop applying.
func loadLabelingRefusals(sources []labelingRefusalSource) (*labelingRefusals, error) {
	refusals := newLabelingRefusals()
	for _, source := range sources {
		db, err := openLabelingDataset(source.Path)
		if err != nil {
			return nil, fmt.Errorf("refusals of %s (%s): %w; restore that dataset, whose refusals this job honours", source.Path, source.Reason, err)
		}
		err = func() error {
			defer db.Close()
			if !labelingHasLedger(db) {
				return nil
			}
			rows, err := queryMaps(db, `SELECT DISTINCT u.message_sha256||':'||i.target_hash key, r.outcome FROM item_results r JOIN items i ON i.id=r.item_id
				JOIN units u ON u.id=i.unit_id WHERE r.outcome IN ('refused','refusal_suspected')`)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if row["outcome"] == itemRefused {
					refusals.confirmed[firstString(row["key"])] = true
				} else {
					refusals.suspected[firstString(row["key"])] = true
				}
			}
			return nil
		}()
		if err != nil {
			return nil, err
		}
	}
	return refusals, nil
}

// labelingRefusalBlock says whether an item may not be sent by job (""
// for a job not yet created): "refused" for a confirmed refusal anywhere,
// "refusal_suspected" for a suspicion in another job or dataset, else "".
func labelingRefusalBlock(q queryer, refusals *labelingRefusals, key, job string) (string, error) {
	if refusals.confirmed[key] {
		return itemRefused, nil
	}
	rows, err := queryMaps(q, `SELECT r.outcome,r.job FROM item_results r JOIN items i ON i.id=r.item_id JOIN units u ON u.id=i.unit_id
		WHERE u.message_sha256||':'||i.target_hash=? AND r.outcome IN ('refused','refusal_suspected')`, key)
	if err != nil {
		return "", err
	}
	suspected := refusals.suspected[key]
	for _, row := range rows {
		if row["outcome"] == itemRefused {
			return itemRefused, nil
		}
		if firstString(row["job"]) != job {
			suspected = true
		}
	}
	if suspected {
		return itemRefusalSuspected, nil
	}
	return "", nil
}

// labelingItemRefusalKey is an item's refusal key in this dataset.
func labelingItemRefusalKey(q queryer, item string) (string, error) {
	rows, err := queryMaps(q, `SELECT u.message_sha256||':'||i.target_hash key FROM items i JOIN units u ON u.id=i.unit_id WHERE i.id=?`, item)
	if err != nil || len(rows) == 0 {
		return "", errors.Join(err, fmt.Errorf("item %s is not in the dataset", item))
	}
	return firstString(rows[0]["key"]), nil
}

// applyLabelingRefusals removes from a request, inside tx, every item a
// refusal now blocks: the item is marked blocked in the request and gets a
// refused_elsewhere result naming the reason. It returns how many items
// were removed and how many remain.
func applyLabelingRefusals(tx *sql.Tx, refusals *labelingRefusals, job, request string) (blocked, remaining int, err error) {
	rows, err := queryMaps(tx, `SELECT item_id,contract_hash FROM request_items WHERE request_id=? AND blocked IS NULL ORDER BY position`, request)
	if err != nil {
		return 0, 0, err
	}
	for _, row := range rows {
		item := firstString(row["item_id"])
		key, err := labelingItemRefusalKey(tx, item)
		if err != nil {
			return 0, 0, err
		}
		reason, err := labelingRefusalBlock(tx, refusals, key, job)
		if err != nil {
			return 0, 0, err
		}
		if reason == "" {
			remaining++
			continue
		}
		blocked++
		if _, err := tx.Exec(`UPDATE request_items SET blocked=? WHERE request_id=? AND item_id=?`, reason, request, item); err != nil {
			return 0, 0, err
		}
		result := labelingItemResult{ItemID: item, Outcome: itemRefusedElsewhere, Labels: []string{}, Uncertain: []string{}, Evidence: []labelingEvidence{},
			Issues: []string{"blocked_by_" + reason}}
		if err := insertLabelingItemResult(tx, "refused-elsewhere:"+request, job, request, result, firstString(row["contract_hash"]), "", ""); err != nil &&
			!strings.Contains(err.Error(), "UNIQUE") {
			return 0, 0, err
		}
	}
	if blocked > 0 {
		state := ""
		if remaining == 0 {
			state = requestRefusedElsewhere
		}
		if _, err := tx.Exec(`UPDATE requests SET items=?,state=CASE WHEN ?<>'' THEN ? ELSE state END,updated_at=?,
			note=? WHERE id=?`, remaining, state, state, now(), fmt.Sprintf("%d items blocked by a refusal elsewhere", blocked), request); err != nil {
			return 0, 0, err
		}
	}
	return blocked, remaining, nil
}

// jobLabelingRefusals loads the refusals a job honours, from the sources
// recorded at its creation.
func jobLabelingRefusals(db *sql.DB, job string) (*labelingRefusals, error) {
	var text sql.NullString
	if err := db.QueryRow(`SELECT refusal_sources FROM jobs WHERE name=?`, job).Scan(&text); err != nil {
		return nil, err
	}
	var sources []labelingRefusalSource
	json.Unmarshal([]byte(text.String), &sources)
	return loadLabelingRefusals(sources)
}
