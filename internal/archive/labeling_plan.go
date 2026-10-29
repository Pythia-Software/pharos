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

// labelingCalibration is what one route cost per request when measured on
// requests of 4 independent short items. Overhead is the input beyond the
// items' own text and context (prompt, taxonomy, schema and CLI framing) per
// request. Output includes reasoning, which providers already count as
// output. These are defaults; plan's flags override each of them.
type labelingCalibration struct {
	source string
	// effort is the reasoning effort the calibration was measured at.
	effort                     string
	overheadTokens, outputItem float64
	secondsPerRequest          float64
}

var labelingCalibrations = map[string]labelingCalibration{
	"claude": {"claude-haiku-5-5 at effort high (default calibration)", "high", 3579.67 * 4, 1006.36, 16.96},
	"codex":  {"gpt-6-luna at effort high (default calibration)", "high", 4027.74 * 4, 211.75, 10.14},
	"agy":    {"gemini-3.8-flash at effort low (default calibration)", "low", 16549, 222.17, 13.98},
}

// labelingCharsPerToken converts item characters to tokens. About 3.6
// characters per o200k_base token is typical of user requests; agent replies
// and thinking are less dense, so it overestimates them.
const labelingCharsPerToken = 3.6

// labelingPlanItem is one item as the plan sizes it.
type labelingPlanItem struct {
	id, cohort, split, family string
	chars                     int
	tokens                    float64
}

// labelingItemTokens is a conservative input estimate for text: ASCII at
// charsPerToken characters per token, and every other character at half a
// token per UTF-8 byte (1.5 for a CJK character, 2 for an emoji), so dense
// scripts are not underestimated by the English-calibrated ratio.
func labelingItemTokens(text string, charsPerToken float64) float64 {
	ascii, other := 0, 0
	for _, r := range text {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			other += utf8.RuneLen(r)
		}
	}
	return float64(ascii)/charsPerToken + float64(other)/2
}

// packLabelingRequests groups items, in the order given, into requests of
// at most perRequest items whose estimated input, the fixed overhead plus
// the items' tokens, stays within budget, and whose characters stay within
// maxChars (0 for no character cap). An item that does not fit even alone is
// returned in oversize and placed in no request: a request is never silently
// larger than either cap.
func packLabelingRequests(tokens []float64, chars []int, perRequest int, overhead, budget float64, maxChars int) (requests [][]int, oversize []int) {
	current, total, length := []int{}, overhead, 0
	for index, size := range tokens {
		if overhead+size > budget || maxChars > 0 && chars[index] > maxChars {
			oversize = append(oversize, index)
			continue
		}
		if len(current) > 0 && (len(current) >= perRequest || total+size > budget || maxChars > 0 && length+chars[index] > maxChars) {
			requests = append(requests, current)
			current, total, length = []int{}, overhead, 0
		}
		current = append(current, index)
		total += size
		length += chars[index]
	}
	if len(current) > 0 {
		requests = append(requests, current)
	}
	return requests, oversize
}

// labelingPackWindow bounds how far ahead the family-aware packer looks
// for an item that may join the open request.
const labelingPackWindow = 256

// packLabelingFamilies packs like packLabelingRequests but never puts two
// items of one family (so of one conversation) in a request: the teacher
// must not see one conversation's later message beside an earlier one
// under "INDEPENDENT ITEMS". Each request takes, in order, the first items
// within labelingPackWindow of the oldest unpacked item that fit its caps
// and add a new family; the rest wait for later requests.
func packLabelingFamilies(tokens []float64, chars []int, families []string, perRequest int, overhead, budget float64, maxChars int) (requests [][]int, oversize []int) {
	queue := []int{}
	for index, size := range tokens {
		if overhead+size > budget || maxChars > 0 && chars[index] > maxChars {
			oversize = append(oversize, index)
			continue
		}
		queue = append(queue, index)
	}
	taken := make([]bool, len(queue))
	head := 0
	for {
		for head < len(queue) && taken[head] {
			head++
		}
		if head == len(queue) {
			return requests, oversize
		}
		current, seen := []int{}, map[string]bool{}
		total, length, examined := overhead, 0, 0
		for at := head; at < len(queue) && examined < labelingPackWindow && len(current) < perRequest; at++ {
			if taken[at] {
				continue
			}
			examined++
			index := queue[at]
			if seen[families[index]] || total+tokens[index] > budget || maxChars > 0 && length+chars[index] > maxChars {
				continue
			}
			taken[at] = true
			seen[families[index]] = true
			current = append(current, index)
			total += tokens[index]
			length += chars[index]
		}
		requests = append(requests, current)
	}
}

// labelingTextAllowed admits at most limit items per cohort and target
// text, counting in item-ID order: IDs are hashes, so the kept items are a
// deterministic choice spread across splits and time.
func labelingTextAllowed(seen map[[2]string]int, cohort, hash string, limit int) bool {
	if limit <= 0 {
		return true
	}
	key := [2]string{cohort, hash}
	if seen[key] >= limit {
		return false
	}
	seen[key]++
	return true
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
	MaxRequestChars                  int
	Concurrency, LimitItems          int
	Cohorts, Splits                  []string
	IncludeDuplicates                bool
	MaxPerText                       int
	OverheadTokens, OutputPerItem    float64
	SecondsPerRequest, CharsPerToken float64
	TokensPerMinute                  float64
	Today                            time.Time
}

func runLabelingPlan(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("labeling plan", flag.ContinueOnError)
	opts := labelingPlanOptions{Today: time.Now()}
	flags.StringVar(&opts.Dir, "dir", "", "prepared dataset directory")
	flags.StringVar(&opts.Route, "route", "", "claude, codex or agy")
	flags.StringVar(&opts.Model, "model", "", "model the route will run, priced with the built-in price book")
	flags.StringVar(&opts.Effort, "effort", "", "reasoning effort")
	flags.IntVar(&opts.ItemsPerRequest, "items-per-request", 4, "items per request (4 was measured)")
	flags.Float64Var(&opts.MaxRequestTokens, "max-request-tokens", 24000, "estimated input tokens per request, fixed overhead included")
	flags.IntVar(&opts.MaxRequestChars, "max-request-chars", 0, "also cap the characters of item text and context per request (0: no cap)")
	flags.IntVar(&opts.Concurrency, "concurrency", 3, "concurrent requests")
	flags.IntVar(&opts.LimitItems, "limit-items", 0, "plan only the first N selected items")
	flags.BoolVar(&opts.IncludeDuplicates, "include-duplicates", false, "also plan delegation units that repeat a child's message")
	flags.IntVar(&opts.MaxPerText, "max-per-text", 0, "at most N items with the same cohort and target text (0: no limit)")
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
		return fmt.Errorf("unknown route %q: use claude, codex or agy", opts.Route)
	}
	if opts.ItemsPerRequest < 1 || opts.MaxPerText < 0 || opts.MaxRequestTokens <= 0 || opts.MaxRequestChars < 0 || opts.Concurrency < 1 || opts.CharsPerToken <= 0 || opts.TokensPerMinute <= 0 {
		return errors.New("--items-per-request, --max-request-tokens, --concurrency, --chars-per-token and --tokens-per-minute must be positive, and --max-request-chars not negative")
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
	return sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(10000)")
}

// planLabeling estimates a labeling run of a prepared dataset: requests,
// tokens, price and runtime. It reads only the dataset and calls no model.
func planLabeling(ctx context.Context, opts labelingPlanOptions) (map[string]any, error) {
	db, err := openLabelingDataset(opts.Dir)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT i.id,u.cohort,COALESCE(u.split,''),i.chars,i.text,u.context_prompt,u.duplicate_of IS NOT NULL,
		COALESCE(u.family_id,u.conversation_id),i.target_hash FROM items i JOIN units u ON u.id=i.unit_id ORDER BY i.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []labelingPlanItem{}
	duplicates, repeatedText := 0, 0
	perText := map[[2]string]int{}
	for rows.Next() {
		var item labelingPlanItem
		var text, context, hash string
		var duplicate bool
		if err := rows.Scan(&item.id, &item.cohort, &item.split, &item.chars, &text, &context, &duplicate, &item.family, &hash); err != nil {
			return nil, err
		}
		if len(opts.Cohorts) > 0 && !slices.Contains(opts.Cohorts, item.cohort) || len(opts.Splits) > 0 && !slices.Contains(opts.Splits, item.split) {
			continue
		}
		if duplicate && !opts.IncludeDuplicates {
			duplicates++
			continue
		}
		if !labelingTextAllowed(perText, item.cohort, hash, opts.MaxPerText) {
			repeatedText++
			continue
		}
		item.chars += utf8.RuneCountInString(context)
		item.tokens = labelingItemTokens(text, opts.CharsPerToken) + labelingItemTokens(context, opts.CharsPerToken)
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
	sizes, lengths, families := make([]float64, len(items)), make([]int, len(items)), make([]string, len(items))
	chars := 0
	for index, item := range items {
		sizes[index] = item.tokens
		lengths[index] = item.chars
		families[index] = item.family
		chars += item.chars
	}
	unseparated, _ := packLabelingRequests(sizes, lengths, opts.ItemsPerRequest, overhead, opts.MaxRequestTokens, opts.MaxRequestChars)
	requests, oversize := packLabelingFamilies(sizes, lengths, families, opts.ItemsPerRequest, overhead, opts.MaxRequestTokens, opts.MaxRequestChars)
	contentTokens := 0.0
	for index, size := range sizes {
		if !slices.Contains(oversize, index) {
			contentTokens += size
		}
	}
	oversized := []string{}
	for _, index := range oversize {
		oversized = append(oversized, items[index].id)
		chars -= items[index].chars
	}
	input := float64(len(requests))*overhead + contentTokens
	outputTokens := float64(len(items)-len(oversize)) * output
	day := opts.Today.Format("2006-01-02")
	book, err := embeddedPriceBook(ctx, opts.Today)
	if err != nil {
		return nil, err
	}
	// Priced as run prices the labeling model: under its exact name only,
	// so a model run would refuse as unpriced is unpriced here too.
	cost := labelingExactCost(book, opts.Model, day, map[string]int64{"uncached_input_tokens": int64(input), "output_tokens": int64(outputTokens)})
	price := map[string]any{"model": opts.Model, "day": day, "status": cost.status, "priced_model": nilIfEmpty(cost.pricedModel), "cost_usd": nil}
	if cost.cost != nil && cost.status != "unpriced" {
		price["cost_usd"] = *cost.cost
	} else {
		price["note"] = "the built-in price book has no price for this exact model name on this day (run would refuse it without --allow-unpriced); no cost is guessed"
	}
	serial := float64(len(requests)) * seconds
	hours := max(serial/float64(opts.Concurrency)/3600, input/opts.TokensPerMinute/60, outputTokens/(opts.TokensPerMinute/5)/60) * 1.25
	assumptions := []string{
		"Calibration: " + calibration.source + ".",
		fmt.Sprintf("Input per request: %.0f tokens of prompt, taxonomy, schema and CLI framing, plus item text and context at %.2f ASCII characters per token and half a token per UTF-8 byte of other characters.", overhead, opts.CharsPerToken),
		fmt.Sprintf("Output per item: %.0f tokens, reasoning included.", output),
		"Price: standard rates for uncached input and output from the built-in price book; no cache or batch discount.",
		fmt.Sprintf("Runtime: %.2f s per request; the larger of request time over %d concurrent requests, input over %.0f tokens/min, and output over a fifth of that, plus 25%%; planning high is 3 times that. Capacity is assumed, not measured.",
			seconds, opts.Concurrency, opts.TokensPerMinute),
	}
	if opts.Effort != calibration.effort {
		assumptions = append(assumptions, fmt.Sprintf("Effort %s was not calibrated; the measurements are at effort %s.", opts.Effort, calibration.effort))
	}
	// Refusals already recorded in this dataset's jobs, and how many of the
	// refused items were only packed with the refused material: they are
	// blocked permanently all the same (no re-send path).
	refusals := map[string]any{"refused_items": 0, "co_packed_blocked_items": 0, "suspected_items": 0}
	if labelingHasLedger(db) {
		var refused, coPacked, suspected int
		db.QueryRow(`SELECT COUNT(DISTINCT item_id) FROM item_results WHERE outcome='refused'`).Scan(&refused)
		db.QueryRow(`SELECT COUNT(DISTINCT item_id) FROM item_results WHERE outcome='refused' AND issues LIKE '%refused_copacked_unresolved%'`).Scan(&coPacked)
		db.QueryRow(`SELECT COUNT(DISTINCT item_id) FROM item_results WHERE outcome='refusal_suspected'`).Scan(&suspected)
		refusals = map[string]any{"refused_items": refused, "co_packed_blocked_items": coPacked, "suspected_items": suspected,
			"note": "a new job selects none of these; co-packed items are blocked although the refusal may belong to a neighbour"}
	}
	selection := map[string]any{"cohorts": opts.Cohorts, "splits": opts.Splits, "limit_items": opts.LimitItems,
		"include_duplicates": opts.IncludeDuplicates, "duplicate_items_skipped": duplicates, "max_per_text": opts.MaxPerText,
		"repeated_text_items_skipped": repeatedText}
	return map[string]any{
		"dataset": opts.Dir, "route": opts.Route, "model": opts.Model, "effort": opts.Effort, "selection": selection,
		"items": len(items) - len(oversize), "item_chars": chars, "requests": len(requests), "items_per_request": opts.ItemsPerRequest,
		"packing": map[string]any{"one_item_per_family": true, "requests_without_family_separation": len(unseparated),
			"added_requests": len(requests) - len(unseparated)},
		"max_request_tokens": opts.MaxRequestTokens, "max_request_chars": opts.MaxRequestChars, "oversize_items": oversized,
		"tokens":      map[string]any{"input": int64(input), "output": int64(outputTokens)},
		"price":       price,
		"runtime":     map[string]any{"concurrency": opts.Concurrency, "serial_hours": serial / 3600, "hours": hours, "planning_high_hours": hours * 3},
		"assumptions": assumptions, "refusals": refusals,
	}, nil
}
