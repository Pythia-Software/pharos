package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/gbdubs/pharos/estimation"
)

const estimationCalibrationKey = "usage_estimation_calibration_v1"

const estimationGenerationSQL = `SELECT COALESCE((SELECT value FROM meta WHERE key='tool_ledger_generation'),'') || '/' ||
 COALESCE((SELECT value FROM meta WHERE key='usage_estimation_revision'),'')`

// Timer corrections and ancestry changes request a fresh fit even on the
// same day. New timers join the next daily refinement without invalidating
// the last compatible snapshot on each coding ingest.
func (c *Catalog) ensureUsageEstimation() error {
	// Restrict recent-donor scans to eligible native counters, in clock order.
	if _, err := c.DB.Exec(`CREATE INDEX IF NOT EXISTS model_requests_estimation_idx ON model_requests(requested_at DESC,id)
 WHERE is_estimate=0 AND reasoning_output_tokens>0 AND effort IS NOT NULL AND speed_source IN ('speed','service_tier')`); err != nil {
		return err
	}
	if _, err := c.DB.Exec("DROP TRIGGER IF EXISTS usage_estimation_evidence_insert"); err != nil {
		return err
	}
	bump := `INSERT INTO meta(key,value) VALUES('usage_estimation_revision',hex(randomblob(16))) ON CONFLICT(key) DO UPDATE SET value=excluded.value;`
	for _, trigger := range []string{

		"evidence_delete AFTER DELETE ON model_request_evidence WHEN OLD.thinking_ms IS NOT NULL BEGIN " + bump + " END",
		"evidence_update AFTER UPDATE ON model_request_evidence WHEN (OLD.thinking_ms IS NOT NULL OR NEW.thinking_ms IS NOT NULL) AND (OLD.thinking_ms IS NOT NEW.thinking_ms OR OLD.conversation_id IS NOT NEW.conversation_id OR OLD.native_id IS NOT NEW.native_id) BEGIN " + bump + " END",
		"ancestry_update AFTER UPDATE OF parent_id,account,provider,harness ON conversations WHEN OLD.parent_id IS NOT NEW.parent_id OR OLD.account IS NOT NEW.account OR OLD.provider IS NOT NEW.provider OR OLD.harness IS NOT NEW.harness BEGIN " + bump + " END",
	} {
		if _, err := c.DB.Exec("CREATE TRIGGER IF NOT EXISTS usage_estimation_" + trigger); err != nil {
			return err
		}
	}
	return c.ensureUsageEstimationStore()
}

type estimationRateGroup struct {
	Rate     estimationRange `json:"rate"`
	Requests int             `json:"requests"`
	Families int             `json:"families"`
}
type estimationCalibration struct {
	Version    string                         `json:"version"`
	Manifest   string                         `json:"manifest_digest"`
	Generation string                         `json:"source_generation"`
	BuiltAt    string                         `json:"built_at"`
	Candidates int                            `json:"candidates"`
	Requests   int                            `json:"requests"`
	Groups     map[string]estimationRateGroup `json:"groups"`
}
type estimationDonor struct {
	Native, Account, Conversation, Model, Effort, Speed string
	Seconds, Tokens                                     float64
}

func estimationDurationBin(seconds float64) int {
	for i, edge := range []float64{1, 3, 10, 30, 100} {
		if seconds < edge {
			return i
		}
	}
	return 5
}
func estimationGroupKey(model, effort, speed string, seconds float64) string {
	return jsonText([]any{model, effort, speed, estimationDurationBin(seconds)})
}

// fitEstimationRates uses arithmetic rates, weighted equally by linked root
// family within duration bands. It never exponentiates a log fit and calls the
// result expected consumption. Unsupported cells keep their published priors.
func fitEstimationRates(donors []estimationDonor, parents map[string]string, policy estimationLocalPolicy) (map[string]estimationRateGroup, int) {
	type cell struct {
		families map[string][]float64
		rates    []float64
	}
	cells, seen, retained := map[string]*cell{}, map[string]bool{}, 0
	for _, r := range donors {
		if r.Seconds < .1 || r.Seconds > 600 || r.Tokens <= 0 || r.Model == "" || r.Effort == "" || (r.Speed != "standard" && r.Speed != "fast") || r.Native == "" {
			continue
		}
		identity := jsonText([]string{r.Account, r.Native})
		if seen[identity] {
			continue
		}
		seen[identity] = true
		family, visited := r.Conversation, map[string]bool{}
		for parents[family] != "" && !visited[family] {
			visited[family] = true
			family = parents[family]
		}
		if visited[family] { // unresolved cycle is not independent calibration evidence
			continue
		}
		key := estimationGroupKey(r.Model, r.Effort, r.Speed, r.Seconds)
		c := cells[key]
		if c == nil {
			c = &cell{families: map[string][]float64{}}
			cells[key] = c
		}
		rate := r.Tokens / r.Seconds
		if math.IsNaN(rate) || math.IsInf(rate, 0) {
			continue
		}
		c.rates = append(c.rates, rate)
		c.families[family] = append(c.families[family], rate)
		retained++
	}
	groups := map[string]estimationRateGroup{}
	for key, c := range cells {
		if len(c.rates) < policy.MinimumRequests || len(c.families) < policy.MinimumFamilies {
			continue
		}
		mean := 0.0
		// Sort keys so persisted coefficients are deterministic across replays.
		keys := make([]string, 0, len(c.families))
		for family := range c.families {
			keys = append(keys, family)
		}
		sort.Strings(keys)
		for _, family := range keys {
			sum := 0.0
			for _, v := range c.families[family] {
				sum += v
			}
			mean += sum / float64(len(c.families[family])) / float64(len(keys))
		}
		sort.Float64s(c.rates)
		groups[key] = estimationRateGroup{Rate: estimationRange{mean, min(mean, c.rates[(len(c.rates)-1)/10]), max(mean, c.rates[(len(c.rates)-1)*9/10])}, Requests: len(c.rates), Families: len(c.families)}
	}
	return groups, retained
}

func (c *Catalog) loadEstimationCalibration(ctx context.Context) (estimationCalibration, error) {
	return c.loadEstimationCalibrationFrom(ctx, c.reader())
}

type estimationReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (c *Catalog) loadEstimationCalibrationFrom(ctx context.Context, db estimationReader) (estimationCalibration, error) {
	var result estimationCalibration
	var value string
	err := db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", estimationCalibrationKey).Scan(&value)
	if err == sql.ErrNoRows {
		return result, nil
	}
	if err != nil || value == "" {
		return result, err
	}
	if json.Unmarshal([]byte(value), &result) != nil || result.Version != usageEstimatorVersion || result.Manifest != hashBytes(estimation.Assumptions) {
		return estimationCalibration{}, nil
	}
	manifest, err := bundledEstimation()
	if err != nil {
		return estimationCalibration{}, err
	}
	at, ok := parseTime(result.BuiltAt)
	if !ok || c.clock().Sub(at) > time.Duration(manifest.Local.MaximumAgeDays)*24*time.Hour || at.After(c.clock().Add(time.Minute)) {
		return estimationCalibration{}, nil
	}
	// Serve the last compatible snapshot while the next refinement is pending.
	// Source corrections request a refresh; they do not make a saved fit vanish.
	return result, nil
}

// refreshUsageEstimationCalibration runs only in the existing Tools maintenance
// stage. Readers load one small saved document and never scan donor transcripts.
// A read transaction gives the donor pass a consistent generation; a writer
// guard discards it if indexing advanced while it was computed.
func (c *Catalog) refreshUsageEstimationCalibration(ctx context.Context) error {
	manifest, err := bundledEstimation()
	if err != nil {
		return err
	}
	previous, err := c.loadEstimationCalibration(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var generation string
	err = tx.QueryRowContext(ctx, estimationGenerationSQL).Scan(&generation)
	if err != nil {
		return err
	}
	_, priorCorrection, _ := strings.Cut(previous.Generation, "/")
	_, currentCorrection, _ := strings.Cut(generation, "/")
	if previous.Version != "" && priorCorrection == currentCorrection && previous.BuiltAt[:10] == c.clock().UTC().Format("2006-01-02") {
		return nil
	}
	cutoff := c.clock().AddDate(0, 0, -manifest.Local.MaximumAgeDays).UTC().Format(time.RFC3339Nano)
	rows, err := tx.QueryContext(ctx, `WITH candidates AS MATERIALIZED (
 SELECT r.native_id,c.account,r.conversation_id,r.model,r.effort,r.speed,r.reasoning_output_tokens,
 (SELECT MAX(e.thinking_ms) FROM model_request_evidence e WHERE e.conversation_id=r.conversation_id AND e.native_id=r.native_id) thinking_ms
 FROM model_requests r JOIN conversations c ON c.id=r.conversation_id
 WHERE r.is_estimate=0 AND c.provider='claude' AND c.harness LIKE 'claude-code%' AND r.model LIKE 'claude-%'
 AND r.requested_at>=? AND r.reasoning_output_tokens>0 AND r.effort IS NOT NULL
 AND r.speed_source IN ('speed','service_tier')
 ORDER BY r.requested_at DESC,r.id LIMIT ?)
 SELECT native_id,account,conversation_id,model,effort,speed,reasoning_output_tokens,thinking_ms/1000.0
 FROM candidates WHERE thinking_ms BETWEEN 100 AND 600000`, cutoff, manifest.Local.MaximumRequests)
	if err != nil {
		return err
	}
	donors := []estimationDonor{}
	for rows.Next() {
		var r estimationDonor
		var effort, speed sql.NullString
		if err := rows.Scan(&r.Native, &r.Account, &r.Conversation, &r.Model, &effort, &speed, &r.Tokens, &r.Seconds); err != nil {
			rows.Close()
			return err
		}
		r.Effort, r.Speed = effort.String, speed.String
		donors = append(donors, r)
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return rowErr
	}
	parents := map[string]string{}
	parentRows, err := tx.QueryContext(ctx, "SELECT id,COALESCE(parent_id,'') FROM conversations WHERE provider='claude'")
	if err != nil {
		return err
	}
	for parentRows.Next() {
		var id, parent string
		if err := parentRows.Scan(&id, &parent); err != nil {
			parentRows.Close()
			return err
		}
		parents[id] = parent
	}
	rowErr = parentRows.Err()
	parentRows.Close()
	if rowErr != nil {
		return rowErr
	}
	groups, retained := fitEstimationRates(donors, parents, manifest.Local)
	result := estimationCalibration{Version: usageEstimatorVersion, Manifest: hashBytes(estimation.Assumptions), Generation: generation,
		BuiltAt: c.clock().UTC().Format(time.RFC3339Nano), Candidates: len(donors), Requests: retained, Groups: groups}
	// Release the WAL snapshot before acquiring the writer.
	if err := tx.Rollback(); err != nil {
		return err
	}
	return c.writeTransaction(ctx, "usage estimation calibration", func(writer *sql.Tx) error {
		var current string
		err := writer.QueryRowContext(ctx, estimationGenerationSQL).Scan(&current)
		if err != nil {
			return err
		}
		if current != generation {
			return nil
		}
		_, err = writer.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", estimationCalibrationKey, jsonText(result))
		return err
	})
}

func estimationThinkingRate(manifest estimationManifest, calibration estimationCalibration, model, effort, speed string, seconds float64, prior estimationRange) (estimationRange, estimationRateGroup, float64) {
	if !validEstimationNumber(seconds) || seconds < .1 || seconds > 600 {
		return prior, estimationRateGroup{}, 0
	}
	group := calibration.Groups[estimationGroupKey(model, effort, speed, seconds)]
	if group.Families < manifest.Local.MinimumFamilies || group.Requests < manifest.Local.MinimumRequests || !group.Rate.valid() || group.Rate.Expected <= 0 {
		return prior, estimationRateGroup{}, 0
	}
	w := float64(group.Families) / float64(group.Families+manifest.Local.PriorFamilies)
	rate := prior.times(1 - w).plus(group.Rate.times(w))
	// Local fit can refine the mean, but cannot erase published transfer sensitivity.
	rate.Lower, rate.Upper = min(rate.Lower, prior.Lower), max(rate.Upper, prior.Upper)
	return rate, group, w
}

// Local duration bands describe individual thinking intervals. Applying the
// band for a whole multi-call response would silently turn several short
// intervals into one long interval. Merge overlapping clocks, then calibrate
// each connected interval once; retain explicit summary/block assumptions for
// the unclocked portion without adding another whole-response prediction.
func estimateThinkingContributions(r *usageEstimationResponse, seconds estimationRange, manifest estimationManifest, calibration estimationCalibration, profile estimationProfile) (estimationRange, estimationRateGroup, float64) {
	prior := profile.ThinkingRate
	merged := connectedEstimationThinkingIntervals(r.blocks)
	result, support := estimationRange{}, estimationRateGroup{}
	weighted := 0.0
	contribute := func(duration estimationRange) {
		rate, group, weight := estimationThinkingRate(manifest, calibration, r.model, r.effort, r.speed, duration.Expected, prior)
		result = result.plus(duration.multiply(rate))
		weighted += duration.Expected * weight
		if weight > 0 && (support.Families == 0 || group.Families < support.Families) {
			support.Families = group.Families
		}
		if weight > 0 && (support.Requests == 0 || group.Requests < support.Requests) {
			support.Requests = group.Requests
		}
	}
	for _, interval := range merged {
		duration := interval.end.Sub(interval.start).Seconds()
		contribute(estimationRange{duration, duration, duration})
	}
	for _, block := range r.blocks {
		if block.kind != "thinking" || estimationThinkingClock(block) {
			continue
		}
		duration := profile.BlockSeconds
		if block.summaries > 0 {
			duration = profile.SummarySeconds.times(float64(block.summaries))
		}
		contribute(duration)
	}
	weight := 0.0
	if seconds.Expected > 0 {
		weight = weighted / seconds.Expected
	}
	return result, support, weight
}
