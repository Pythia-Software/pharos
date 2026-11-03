package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
)

type thinkingValidation struct {
	Pairs          int          `json:"pairs"`
	Correlation    *float64     `json:"correlation"`
	MedianRatio    *float64     `json:"medianRatio"`
	Points         [][2]float64 `json:"points"`
	Counters       [][2]float64 `json:"counters"`
	CounterCount   int          `json:"counterCount"`
	CounterOverlap int          `json:"counterOverlap"`
}

func thinkingRanks(values []float64) []float64 {
	order := make([]int, len(values))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool { return values[order[i]] < values[order[j]] })
	r := make([]float64, len(values))
	for i := 0; i < len(order); {
		j := i + 1
		for j < len(order) && values[order[j]] == values[order[i]] {
			j++
		}
		for k := i; k < j; k++ {
			r[order[k]] = float64(i+j-1) / 2
		}
		i = j
	}
	return r
}
func thinkingCorrelation(a, b []float64) *float64 {
	if len(a) < 3 {
		return nil
	}
	x, y := thinkingRanks(a), thinkingRanks(b)
	mx, my := 0.0, 0.0
	for i := range x {
		mx += x[i] / float64(len(x))
		my += y[i] / float64(len(y))
	}
	xx, yy, xy := 0.0, 0.0, 0.0
	for i := range x {
		xx += (x[i] - mx) * (x[i] - mx)
		yy += (y[i] - my) * (y[i] - my)
		xy += (x[i] - mx) * (y[i] - my)
	}
	if xx == 0 || yy == 0 {
		return nil
	}
	v := xy / math.Sqrt(xx*yy)
	return &v
}
func (c *Catalog) thinkingValidation(ctx context.Context, o thinkingOptions, records []thinkingRecord) (*thinkingValidation, error) {
	return cachedValue(ctx, c, "thinking:validation:"+thinkingCohortKey(records), func(ctx context.Context) (*thinkingValidation, error) {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return c.loadThinkingValidation(bounded, records)
	})
}
func (c *Catalog) loadThinkingValidation(ctx context.Context, records []thinkingRecord) (*thinkingValidation, error) {
	result := &thinkingValidation{Points: [][2]float64{}, Counters: [][2]float64{}}
	// Select metadata from the already qualified population, without rebuilding the
	// request/tool projection for each validation query. Only numbers leave SQL.
	conversations := map[string]bool{}
	natives := map[string]string{}
	gaps := map[string]float64{}
	for _, r := range records {
		conversations[r.Conversation] = true
		natives[r.Conversation+"|"+r.Native] = r.ID
		if r.Gap != nil {
			gaps[r.ID] = *r.Gap
		}
	}
	selected := []string{}
	for id := range conversations {
		selected = append(selected, id)
	}
	sort.Strings(selected)
	selection, err := json.Marshal(selected)
	if err != nil {
		return nil, err
	}
	// Stored evidence is complete once its backfill has finished; until then
	// read each metadata body. Both are read in one snapshot.
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var complete bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM meta WHERE key='model_request_evidence_version' AND value=?)", requestEvidenceVersion).Scan(&complete); err != nil {
		return nil, err
	}
	measured := "SELECT e.* FROM selected s CROSS JOIN model_request_evidence e ON e.conversation_id=s.conversation_id"
	if !complete {
		measured = requestEvidence("SELECT m.id,m.conversation_id,m.source_order,m.text,m.raw_text FROM selected s CROSS JOIN messages m ON m.conversation_id=s.conversation_id WHERE m.kind='metadata'")
	}
	// Materialize only scalar evidence, so the timer grouping and latest-counter
	// window share a narrow numeric relation.
	source := `WITH selected AS (SELECT value conversation_id FROM json_each(?)), measured AS MATERIALIZED (` + measured + `), states AS (
 SELECT *,ROW_NUMBER() OVER(PARTITION BY conversation_id ORDER BY source_order DESC) latest FROM measured WHERE kind='cost-state')
 SELECT 'timer' evidence,conversation_id,native_id,MAX(thinking_ms) value,NULL components FROM measured
 WHERE thinking_ms IS NOT NULL GROUP BY conversation_id,native_id
 UNION ALL SELECT 'counter',conversation_id,'',total,api+tool FROM states WHERE latest=1`
	rows, err := queryMapsContext(ctx, tx, source, string(selection))
	if err != nil {
		return nil, err
	}
	x, y, ratios := []float64{}, []float64{}, []float64{}
	for _, r := range rows {
		if asString(r["evidence"]) == "counter" {
			total, components := thinkingNumber(r["value"])/1000, thinkingNumber(r["components"])/1000
			result.CounterCount++
			if components > total {
				result.CounterOverlap++
			}
			if len(result.Counters) < 1600 {
				result.Counters = append(result.Counters, [2]float64{total, components})
			}
			continue
		}
		gap, ok := gaps[natives[asString(r["conversation_id"])+"|"+asString(r["native_id"])]]
		timer := thinkingNumber(r["value"]) / 1000
		if !ok || timer <= 0 {
			continue
		}
		x = append(x, gap)
		y = append(y, timer)
		ratios = append(ratios, timer/gap)
	}
	result.Pairs = len(x)
	result.Correlation = thinkingCorrelation(x, y)
	if len(ratios) > 0 {
		v := thinkingMedian(ratios)
		result.MedianRatio = &v
	}
	for i := 0; i < min(len(x), 1600); i++ {
		at := i * len(x) / min(len(x), 1600)
		result.Points = append(result.Points, [2]float64{x[at], y[at]})
	}
	return result, nil
}

// requestEvidenceVersion names the extraction stored in model_request_evidence.
const requestEvidenceVersion = "evidence-v1"

// requestEvidenceSQL extracts the numeric validation evidence of the metadata
// messages {messages} selects (id, conversation_id, source_order, text and
// raw_text): a response's reported thinking duration, and a valid cumulative
// cost state. A message with neither yields no row, so the stored evidence
// stays a small fraction of the metadata bodies it replaces reading.
const requestEvidenceSQL = `SELECT id message_id,conversation_id,source_order,
 CASE WHEN timer THEN native_id END native_id,CASE WHEN timer THEN thinking_ms END thinking_ms,
 CASE WHEN counter THEN 'cost-state' END kind,CASE WHEN counter THEN total END total,
 CASE WHEN counter THEN api END api,CASE WHEN counter THEN tool END tool FROM (
 SELECT *,typeof(thinking_ms) IN ('integer','real') AND typeof(native_id)='text' timer,
 kind='cost-state' AND typeof(total) IN ('integer','real') AND typeof(api) IN ('integer','real') AND typeof(tool) IN ('integer','real') counter FROM (
 SELECT id,conversation_id,source_order,CASE WHEN json_type(body,'$.message.id')='text' THEN json_extract(body,'$.message.id') END native_id,
 CASE WHEN json_type(body,'$.thinkingDurationMs') IN ('integer','real') THEN json_extract(body,'$.thinkingDurationMs') END thinking_ms,json_extract(body,'$.type') kind,
 CASE WHEN json_type(body,'$.totalDuration') IN ('integer','real') THEN json_extract(body,'$.totalDuration') END total,
 CASE WHEN json_type(body,'$.totalAPIDuration') IN ('integer','real') THEN json_extract(body,'$.totalAPIDuration') END api,
 CASE WHEN json_type(body,'$.totalToolDuration') IN ('integer','real') THEN json_extract(body,'$.totalToolDuration') END tool FROM (
 SELECT id,conversation_id,source_order,CASE WHEN json_valid(COALESCE(NULLIF(raw_text,''),text))
 THEN COALESCE(NULLIF(raw_text,''),text) ELSE '{}' END body FROM ({messages})))) WHERE timer OR counter`

func requestEvidence(messages string) string {
	return strings.ReplaceAll(requestEvidenceSQL, "{messages}", messages)
}

const requestEvidenceUpsert = ` ON CONFLICT(message_id) DO UPDATE SET conversation_id=excluded.conversation_id,source_order=excluded.source_order,
 native_id=excluded.native_id,thinking_ms=excluded.thinking_ms,kind=excluded.kind,total=excluded.total,api=excluded.api,tool=excluded.tool;`

// ensureRequestEvidence installs the stored validation evidence. Triggers keep
// it current with every metadata message write; a catalog that predates it,
// or an older extraction, is filled by backfillRequestEvidence, and read from
// the message bodies until that finishes.
func (c *Catalog) ensureRequestEvidence() error {
	if _, err := c.DB.Exec(`CREATE TABLE IF NOT EXISTS model_request_evidence(message_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL,
 source_order, native_id, thinking_ms, kind, total, api, tool) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS model_request_evidence_conversation_idx ON model_request_evidence(conversation_id);`); err != nil {
		return err
	}
	insert := func(ref string) string {
		return "INSERT INTO model_request_evidence " + requestEvidence("SELECT "+ref+".id id,"+ref+".conversation_id conversation_id,"+ref+".source_order source_order,"+
			ref+".text text,"+ref+".raw_text raw_text WHERE "+ref+".kind='metadata'") + requestEvidenceUpsert
	}
	for _, trigger := range []string{
		"insert AFTER INSERT ON messages WHEN NEW.kind='metadata' BEGIN " + insert("NEW") + " END",
		"update AFTER UPDATE OF id,conversation_id,kind,source_order,text,raw_text ON messages WHEN (OLD.kind='metadata' OR NEW.kind='metadata') AND " +
			"(OLD.id IS NOT NEW.id OR OLD.conversation_id IS NOT NEW.conversation_id OR OLD.kind IS NOT NEW.kind OR OLD.source_order IS NOT NEW.source_order OR " +
			"OLD.text IS NOT NEW.text OR OLD.raw_text IS NOT NEW.raw_text) BEGIN DELETE FROM model_request_evidence WHERE message_id=OLD.id; " + insert("NEW") + " END",
		"delete AFTER DELETE ON messages WHEN OLD.kind='metadata' BEGIN DELETE FROM model_request_evidence WHERE message_id=OLD.id; END",
	} {
		if _, err := c.DB.Exec("CREATE TRIGGER IF NOT EXISTS request_evidence_" + trigger); err != nil {
			return err
		}
	}
	// Start a backfill for a new extraction; one in progress keeps its cursor.
	_, err := c.DB.Exec(`DELETE FROM model_request_evidence WHERE NOT EXISTS(SELECT 1 FROM meta WHERE key='model_request_evidence_version' AND value=?1)
 AND NOT EXISTS(SELECT 1 FROM meta WHERE key='model_request_evidence_cursor');
 INSERT INTO meta(key,value) SELECT 'model_request_evidence_cursor','' WHERE NOT EXISTS(SELECT 1 FROM meta WHERE key='model_request_evidence_version' AND value=?1)
 ON CONFLICT(key) DO NOTHING`, requestEvidenceVersion)
	return err
}

// backfillRequestEvidence extracts the evidence of existing metadata messages
// a batch of conversations at a time, in conversation order, resuming from its
// cursor. Triggers already maintain every message written meanwhile.
func (c *Catalog) backfillRequestEvidence(ctx context.Context) error {
	insert := "INSERT INTO model_request_evidence " + requestEvidence(`SELECT m.id,m.conversation_id,m.source_order,m.text,m.raw_text FROM messages m
 WHERE m.kind='metadata' AND m.conversation_id IN (SELECT value FROM json_each(?1))`) + requestEvidenceUpsert
	for {
		done := false
		if err := c.writeTransaction(ctx, "request-evidence", func(tx *sql.Tx) error {
			var cursor string
			if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='model_request_evidence_cursor'").Scan(&cursor); err == sql.ErrNoRows {
				done = true
				return nil
			} else if err != nil {
				return err
			}
			rows, err := queryMapsContext(ctx, tx, "SELECT id FROM conversations WHERE id>? ORDER BY id LIMIT ?", cursor, requestTimingBatch)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				_, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key='model_request_evidence_cursor';
 INSERT INTO meta(key,value) VALUES('model_request_evidence_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, requestEvidenceVersion)
				done = true
				return err
			}
			ids := make([]string, len(rows))
			for i, row := range rows {
				ids[i] = firstString(row["id"])
			}
			batch, err := json.Marshal(ids)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, insert, string(batch)); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "UPDATE meta SET value=? WHERE key='model_request_evidence_cursor'", ids[len(ids)-1])
			return err
		}); err != nil || done {
			return err
		}
	}
}
