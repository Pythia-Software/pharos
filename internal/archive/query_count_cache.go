package archive

import (
	"context"
	"database/sql"
	"strings"
	"sync"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// queryCountLimit bounds the remembered counts. A page load makes a few
// dozen; tests lower it.
var queryCountLimit = 256

type queryCountKey struct {
	// scope is which writes advance generation (cacheGenerations.of).
	scope      string
	generation int64
	// timezone is the process's local zone: day, week and month fields
	// convert with SQLite's 'localtime', which the statement's text does
	// not capture.
	timezone string
	count    string // qt.PopulationCount.Key
}

// queryCountCache remembers exact population counts between Query Table
// requests. Each count is tagged with the data generation read inside the
// transaction that counted it, and is answered only to a transaction that
// reads the same generation, so a reused total always describes the
// snapshot of the page it is returned with. It lives in memory: requests
// never write.
type queryCountCache struct {
	mu sync.Mutex
	// newest is each scope's latest generation; counts hold only those.
	newest map[string]int64
	counts map[queryCountKey]int64
	// stats tally how counts were answered since the service started, so
	// the hit rate under real use can be read from /api/diagnostics/query-counts.
	stats queryCountStats
}

type queryCountStats struct {
	// Reused counts came from the cache; Fast from a maintained rollup;
	// Counted ran the executor's statement.
	Reused  int64 `json:"reused"`
	Fast    int64 `json:"fast"`
	Counted int64 `json:"counted"`
	// Generations is how many times a newer data generation cleared the
	// cache; Remembered is how many counts it holds now. Generation is the
	// newest counted in any scope (queryCountKey.scope).
	Generations int64 `json:"generations"`
	Remembered  int   `json:"remembered"`
	Generation  int64 `json:"generation"`
}

func (c *queryCountCache) get(key queryCountKey) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.counts[key]
	if ok {
		c.stats.Reused++
	}
	return n, ok
}

func (c *queryCountCache) tally(fast bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fast {
		c.stats.Fast++
	} else {
		c.stats.Counted++
	}
}

func (c *queryCountCache) snapshot() queryCountStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	stats := c.stats
	stats.Remembered = len(c.counts)
	for _, generation := range c.newest {
		stats.Generation = max(stats.Generation, generation)
	}
	return stats
}

func (c *queryCountCache) put(key queryCountKey, n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	newest := c.newest[key.scope]
	if key.generation < newest {
		return // a reader of an older snapshot; no later request reads it
	}
	if key.generation > newest {
		if newest != 0 {
			c.stats.Generations++
		}
		if c.newest == nil {
			c.newest = map[string]int64{}
		}
		c.newest[key.scope] = key.generation
		for old := range c.counts {
			if old.scope == key.scope {
				delete(c.counts, old)
			}
		}
	} else if len(c.counts) >= queryCountLimit {
		c.counts = nil
	}
	if c.counts == nil {
		c.counts = map[queryCountKey]int64{}
	}
	c.counts[key] = n
}

// queryGeneration is the data generation tx reads: every write to a table
// that a cached query reads advances it (see queryCacheTables).
func queryGeneration(ctx context.Context, tx *sql.Tx) (int64, error) {
	generations, err := readCacheGenerations(ctx, tx)
	return generations.all, err
}

// queryCoreDatasets are the SQL-backed sources whose rows no label or
// agent-event write changes (queryCacheDomains): their counts outlast those
// writes. A source that joins such a table must not be listed.
var queryCoreDatasets = map[string]bool{
	"tool_calls": true, "tools": true, "messages": true, "writing_messages": true, "skill_usages": true,
}

// queryDatasetGeneration is the generation scope dataset's counts are
// current for.
func queryDatasetGeneration(dataset string) string {
	if queryCoreDatasets[dataset] {
		return generationCore
	}
	return generationAll
}

// queryPopulationCount answers what it can of a population count without
// running it, inside the counting transaction. ok false falls back to the
// executor's count.
type queryPopulationCount func(ctx context.Context, tx *sql.Tx, p qt.PopulationCount) (total int64, ok bool, err error)

// populationCounter reuses exact counts within a data generation, trying
// fast before running the executor's statement. Use it only for a source
// whose rows are a function of its SQL, its arguments, the local timezone
// and tables that advance the generation (see queryCacheTables).
func (c *Catalog) populationCounter(scope string, fast ...queryPopulationCount) qt.PopulationCounter {
	return qt.PopulationCounterFunc(func(ctx context.Context, tx *sql.Tx, p qt.PopulationCount, count func(context.Context) (int64, error)) (int64, error) {
		generations, err := readCacheGenerations(ctx, tx)
		if err != nil {
			return 0, err
		}
		key := queryCountKey{scope, generations.of(scope), toolRollupTimezone(), p.Key}
		if total, ok := c.queryCounts.get(key); ok {
			return total, nil
		}
		for _, answer := range fast {
			total, ok, err := answer(ctx, tx, p)
			if err != nil {
				return 0, err
			}
			if ok {
				c.queryCounts.tally(true)
				c.queryCounts.put(key, total)
				return total, nil
			}
		}
		total, err := count(ctx)
		if err != nil {
			return 0, err
		}
		c.queryCounts.tally(false)
		c.queryCounts.put(key, total)
		return total, nil
	})
}

// toolCubeFresh reports whether tool_call_cube describes the ledger tx
// reads: built from its current generation in this rollup layout, with no
// dirty partitions, the current full-rebuild revision and the process's
// timezone (the cube keeps local days).
func toolCubeFresh(ctx context.Context, tx *sql.Tx) (bool, error) {
	var fresh bool
	err := tx.QueryRowContext(ctx, `SELECT
 COALESCE((SELECT value FROM meta WHERE key='tool_rollup_generation'),'')=
 COALESCE((SELECT value FROM meta WHERE key='tool_ledger_generation'),'')||? AND
 NOT EXISTS(SELECT 1 FROM tool_rollup_dirty) AND
 COALESCE((SELECT value FROM meta WHERE key='tool_rollup_full_revision'),'')=
 COALESCE((SELECT value FROM meta WHERE key='tool_rollup_built_full_revision'),'') AND
 COALESCE((SELECT value FROM meta WHERE key='tool_rollup_timezone'),'')=?`, "/"+toolRollupVersion, toolRollupTimezone()).Scan(&fresh)
	return fresh, err
}

// toolCubeCount answers a Tool calls count whose filter the cube keeps
// exactly: equality with a text value of a grouped text field, and empty
// or non-empty tests of grouped or present-only text fields, each as its
// own AND term. Anything else, or a cube that is not fresh, declines.
func toolCubeCount(schema qt.Schema) queryPopulationCount {
	return func(ctx context.Context, tx *sql.Tx, p qt.PopulationCount) (int64, bool, error) {
		conditions, args := []string{}, []any{}
		for _, term := range p.Where {
			if term.IsGroup() || term.Negated {
				return 0, false, nil
			}
			kind := schema.Fields[term.Field].Kind
			if kind != qt.FieldText && kind != qt.FieldEnum {
				return 0, false, nil
			}
			dimension, grouped := toolCallCube.dimensionFor(term.Field)
			present, kept := toolCallCube.present[term.Field]
			switch {
			case term.Op == "=" && grouped && term.Value != "":
				// Query Table compares COALESCE(field,'') in binary collation;
				// a non-empty value never matches a missing one.
				conditions, args = append(conditions, dimension+" COLLATE BINARY = ?"), append(args, term.Value)
			case (term.Op == "is_null" || term.Op == "is_not_null") && grouped:
				empty := "(" + dimension + " IS NULL OR " + dimension + " = '')"
				if term.Op == "is_not_null" {
					empty = "NOT " + empty
				}
				conditions = append(conditions, empty)
			case (term.Op == "is_null" || term.Op == "is_not_null") && kept:
				if term.Op == "is_null" {
					present = "NOT " + present
				}
				conditions = append(conditions, present)
			default:
				return 0, false, nil
			}
		}
		if fresh, err := toolCubeFresh(ctx, tx); err != nil || !fresh {
			return 0, false, err
		}
		statement := "SELECT COALESCE(SUM(" + toolCallCube.count + "),0) " + toolCallCube.from
		if len(conditions) > 0 {
			statement += " WHERE " + strings.Join(conditions, " AND ")
		}
		var total int64
		err := tx.QueryRowContext(ctx, statement, args...).Scan(&total)
		return total, err == nil, err
	}
}

// queryPopulationFastCounts are the counts a dataset can answer from a
// maintained rollup instead of its rows.
func queryPopulationFastCounts(dataset string, schema qt.Schema, table sqlDataset) []queryPopulationCount {
	switch dataset {
	case "tool_calls":
		return []queryPopulationCount{toolCubeCount(schema)}
	case "agent_event_rates":
		return []queryPopulationCount{agentEventRateCount(table.base)}
	}
	return nil
}

// agentEventRateCount answers an Agent event rates count filtered by kind
// alone. Every message read has one row for every kind
// (agentEventRateDataset), so the count is the messages the dataset serves
// (base is its predicate over agent_event_facts f) times the kinds the
// filter keeps, without visiting a row per pair.
func agentEventRateCount(base string) queryPopulationCount {
	return func(ctx context.Context, tx *sql.Tx, p qt.PopulationCount) (int64, bool, error) {
		kinds := int64(0)
	families:
		for _, family := range agentEventFamilies {
			for _, term := range p.Where {
				value := family.Key
				if term.Field == "family_group" {
					value = family.Group
				}
				if (term.Field != "family" && term.Field != "family_group") || term.IsGroup() || term.Negated || term.Value == "" ||
					(term.Op != "=" && term.Op != "!=") {
					return 0, false, nil
				}
				if (value == term.Value) != (term.Op == "=") {
					continue families
				}
			}
			kinds++
		}
		statement := "SELECT COUNT(*) FROM agent_event_facts f"
		if base != "" {
			statement += " WHERE " + base
		}
		var messages int64
		err := tx.QueryRowContext(ctx, statement).Scan(&messages)
		return messages * kinds, err == nil, err
	}
}
