package archive

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gbdubs/pharos/pricing"
)

// labelingCalibration is what one measured route cost per request. The
// constants come from the 2026-10-27 Luna/Haiku comparison: 18 requests of 4
// independent short items each at effort high, summarized in the handoff's
// evidence/corpus-budget/20261027/calibration.json (from
// evidence/label-models/20261027 receipts). Overhead is the measured input
// beyond the items' own text and context (prompt, taxonomy, schema and CLI
// framing) per request: fixed_input_per_item × 4. Output includes reasoning,
// which providers already count as output.
type labelingCalibration struct {
	source                     string
	overheadTokens, outputItem float64
	secondsPerRequest          float64
}

var labelingCalibrations = map[string]labelingCalibration{
	"claude": {"claude-haiku-5-5, effort high (calibration.json haiku-high, measured 2026-10-27)", 3579.67 * 4, 1006.36, 16.96},
	"codex":  {"gpt-6-luna, effort high (calibration.json luna-high, measured 2026-10-27)", 4027.74 * 4, 211.75, 10.14},
}

// labelingCharsPerToken converts item characters to tokens: 3.59 characters
// per o200k_base token was the density of sampled user requests in the
// 2026-10-27 production census (evidence/corpus-budget/20261027/
// token-sample.json); replies (3.97) and thinking (5.04) were less dense, so
// it overestimates them. The benchmark's own items ran at 4.47.
const labelingCharsPerToken = 3.6

// labelingPlanItem is one item as the plan sizes it.
type labelingPlanItem struct {
	id, cohort, split string
	chars             int
}

// packLabelingRequests groups items, in the order given, into requests of
// at most perRequest items whose estimated input, the fixed overhead plus
// the items' tokens, stays within budget. An item that does not fit even
// alone is returned in oversize and placed in no request: a request is never
// silently larger than the budget.
func packLabelingRequests(tokens []float64, perRequest int, overhead, budget float64) (requests [][]int, oversize []int) {
	current, total := []int{}, overhead
	for index, size := range tokens {
		if overhead+size > budget {
			oversize = append(oversize, index)
			continue
		}
		if len(current) > 0 && (len(current) >= perRequest || total+size > budget) {
			requests = append(requests, current)
			current, total = []int{}, overhead
		}
		current = append(current, index)
		total += size
	}
	if len(current) > 0 {
		requests = append(requests, current)
	}
	return requests, oversize
}

// embeddedPriceBook loads the price book from the pricing file built into
// this executable, through the same tables and views a catalog syncs it
// into, held in memory.
func embeddedPriceBook(ctx context.Context, today time.Time) (priceBook, error) {
	db, err := sql.Open("sqlite", "file:labeling-prices?mode=memory")
	if err != nil {
		return priceBook{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schemaSQL()); err != nil {
		return priceBook{}, err
	}
	doc, problems := parsePricing(pricing.CostChanges)
	if len(problems) > 0 {
		return priceBook{}, fmt.Errorf("pricing file: %s", strings.Join(problems, "; "))
	}
	catalog := &Catalog{DB: db, now: func() time.Time { return today }}
	if err := catalog.replacePricing(doc, hashBytes(pricing.CostChanges)); err != nil {
		return priceBook{}, err
	}
	return catalog.loadPriceBookFrom(ctx, db)
}

type labelingPlanOptions struct {
	Dir, Route, Model, Effort        string
	ItemsPerRequest                  int
	MaxRequestTokens                 float64
	Concurrency, LimitItems          int
	Cohorts, Splits                  []string
	IncludeDuplicates                bool
	OverheadTokens, OutputPerItem    float64
	SecondsPerRequest, CharsPerToken float64
	TokensPerMinute                  float64
	Today                            time.Time
}

func runLabelingPlan(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("labeling plan", flag.ContinueOnError)
	opts := labelingPlanOptions{Today: time.Now()}
	flags.StringVar(&opts.Dir, "dir", "", "prepared dataset directory")
	flags.StringVar(&opts.Route, "route", "", "claude or codex")
	flags.StringVar(&opts.Model, "model", "", "model the route will run, priced with the built-in price book")
	flags.StringVar(&opts.Effort, "effort", "", "reasoning effort")
	flags.IntVar(&opts.ItemsPerRequest, "items-per-request", 4, "items per request (4 was measured)")
	flags.Float64Var(&opts.MaxRequestTokens, "max-request-tokens", 24000, "estimated input tokens per request, fixed overhead included")
	flags.IntVar(&opts.Concurrency, "concurrency", 3, "concurrent requests")
	flags.IntVar(&opts.LimitItems, "limit-items", 0, "plan only the first N selected items")
	flags.BoolVar(&opts.IncludeDuplicates, "include-duplicates", false, "also plan delegation units that repeat a child's message")
	var cohorts, splits stringList
	flags.Var(&cohorts, "cohort", "plan only this cohort (repeatable)")
	flags.Var(&splits, "split", "plan only this split (repeatable)")
	flags.Float64Var(&opts.OverheadTokens, "overhead-tokens", 0, "input tokens per request beyond items (default: the route's calibration)")
	flags.Float64Var(&opts.OutputPerItem, "output-tokens-per-item", 0, "output tokens per item, reasoning included (default: the route's calibration)")
	flags.Float64Var(&opts.SecondsPerRequest, "seconds-per-request", 0, "wall time of one request (default: the route's calibration)")
	flags.Float64Var(&opts.CharsPerToken, "chars-per-token", labelingCharsPerToken, "characters of item text per input token")
	flags.Float64Var(&opts.TokensPerMinute, "tokens-per-minute", 2_000_000, "assumed input capacity; output capacity is a fifth of it")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || opts.Dir == "" || opts.Model == "" || opts.Effort == "" {
		return errors.New(labelingUsage)
	}
	if labelingCalibrations[opts.Route].source == "" {
		return fmt.Errorf("unknown route %q: use claude or codex", opts.Route)
	}
	if opts.ItemsPerRequest < 1 || opts.MaxRequestTokens <= 0 || opts.Concurrency < 1 || opts.CharsPerToken <= 0 || opts.TokensPerMinute <= 0 {
		return errors.New("items per request, request characters, concurrency, characters per token and tokens per minute must be positive")
	}
	opts.Cohorts, opts.Splits = cohorts, splits
	plan, err := planLabeling(context.Background(), opts)
	if err != nil {
		return err
	}
	if err := writeLabelingJSON(stdout, plan); err != nil {
		return err
	}
	if oversize := plan["oversize_items"].([]string); len(oversize) > 0 {
		return fmt.Errorf("%d items do not fit one request within --max-request-tokens (listed as oversize_items); none was planned oversized", len(oversize))
	}
	return nil
}

// openLabelingDataset opens a prepared dataset read-only. It refuses one
// whose preparation did not publish a manifest.
func openLabelingDataset(dir string) (*sql.DB, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, labelingManifestFile)); err != nil {
		return nil, fmt.Errorf("%s has no complete prepared dataset (no %s); run labeling prepare", dir, labelingManifestFile)
	}
	path := filepath.Join(dir, labelingDatasetFile)
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?mode=ro&_pragma=query_only(1)")
}

// planLabeling estimates a labeling run of a prepared dataset: requests,
// tokens, price and runtime. It reads only the dataset and calls no model.
func planLabeling(ctx context.Context, opts labelingPlanOptions) (map[string]any, error) {
	db, err := openLabelingDataset(opts.Dir)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT i.id,u.cohort,COALESCE(u.split,''),i.chars,u.context_prompt,u.duplicate_of IS NOT NULL FROM items i JOIN units u ON u.id=i.unit_id ORDER BY i.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []labelingPlanItem{}
	duplicates := 0
	for rows.Next() {
		var item labelingPlanItem
		var context string
		var duplicate bool
		if err := rows.Scan(&item.id, &item.cohort, &item.split, &item.chars, &context, &duplicate); err != nil {
			return nil, err
		}
		if len(opts.Cohorts) > 0 && !slices.Contains(opts.Cohorts, item.cohort) || len(opts.Splits) > 0 && !slices.Contains(opts.Splits, item.split) {
			continue
		}
		if duplicate && !opts.IncludeDuplicates {
			duplicates++
			continue
		}
		item.chars += utf8.RuneCountInString(context)
		items = append(items, item)
		if opts.LimitItems > 0 && len(items) == opts.LimitItems {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	calibration := labelingCalibrations[opts.Route]
	overhead, output, seconds := calibration.overheadTokens, calibration.outputItem, calibration.secondsPerRequest
	if opts.OverheadTokens > 0 {
		overhead = opts.OverheadTokens
	}
	if opts.OutputPerItem > 0 {
		output = opts.OutputPerItem
	}
	if opts.SecondsPerRequest > 0 {
		seconds = opts.SecondsPerRequest
	}
	sizes := make([]float64, len(items))
	chars := 0
	for index, item := range items {
		sizes[index] = float64(item.chars) / opts.CharsPerToken
		chars += item.chars
	}
	requests, oversize := packLabelingRequests(sizes, opts.ItemsPerRequest, overhead, opts.MaxRequestTokens)
	oversized := []string{}
	for _, index := range oversize {
		oversized = append(oversized, items[index].id)
		chars -= items[index].chars
	}
	input := float64(len(requests))*overhead + float64(chars)/opts.CharsPerToken
	outputTokens := float64(len(items)-len(oversize)) * output
	day := opts.Today.Format("2006-01-02")
	book, err := embeddedPriceBook(ctx, opts.Today)
	if err != nil {
		return nil, err
	}
	cost := book.cost(opts.Model, day, map[string]int64{"uncached_input_tokens": int64(input), "output_tokens": int64(outputTokens)})
	price := map[string]any{"model": opts.Model, "day": day, "status": cost.status, "priced_model": nilIfEmpty(cost.pricedModel), "cost_usd": nil}
	if cost.cost != nil && cost.status != "unpriced" {
		price["cost_usd"] = *cost.cost
	} else {
		price["note"] = "the built-in price book has no confirmed price for this model on this day; no cost is guessed"
	}
	serial := float64(len(requests)) * seconds
	hours := max(serial/float64(opts.Concurrency)/3600, input/opts.TokensPerMinute/60, outputTokens/(opts.TokensPerMinute/5)/60) * 1.25
	assumptions := []string{
		"Calibration: " + calibration.source + ".",
		fmt.Sprintf("Input per request: %.0f tokens of prompt, taxonomy, schema and CLI framing, plus item text and context at %.2f characters per token.", overhead, opts.CharsPerToken),
		fmt.Sprintf("Output per item: %.0f tokens, reasoning included.", output),
		"Price: standard rates for uncached input and output from the built-in price book; no cache or batch discount.",
		fmt.Sprintf("Runtime: %.2f s per request; the larger of request time over %d concurrent requests, input over %.0f tokens/min, and output over a fifth of that, plus 25%%; planning high is 3 times that. Capacity is assumed, not measured.",
			seconds, opts.Concurrency, opts.TokensPerMinute),
	}
	if opts.Effort != "high" {
		assumptions = append(assumptions, fmt.Sprintf("Effort %s was not calibrated; the measurements are at effort high.", opts.Effort))
	}
	selection := map[string]any{"cohorts": opts.Cohorts, "splits": opts.Splits, "limit_items": opts.LimitItems,
		"include_duplicates": opts.IncludeDuplicates, "duplicate_items_skipped": duplicates}
	return map[string]any{
		"dataset": opts.Dir, "route": opts.Route, "model": opts.Model, "effort": opts.Effort, "selection": selection,
		"items": len(items) - len(oversize), "item_chars": chars, "requests": len(requests), "items_per_request": opts.ItemsPerRequest,
		"max_request_tokens": opts.MaxRequestTokens, "oversize_items": oversized,
		"tokens":      map[string]any{"input": int64(input), "output": int64(outputTokens)},
		"price":       price,
		"runtime":     map[string]any{"concurrency": opts.Concurrency, "serial_hours": serial / 3600, "hours": hours, "planning_high_hours": hours * 3},
		"assumptions": assumptions,
	}, nil
}
