package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gbdubs/pharos/internal/querytable"
)

// The CO₂ calculator turns reconciled token categories into an estimate of
// inference energy and emissions. It is an estimate, not a measurement:
// providers do not publish per-token energy for their models, so each model
// is placed in a size tier whose energy per output token comes from published
// measurements of comparable models, and input, cache-write, and cache-read
// tokens are charged as fractions of that. carbon/co2_factors.json holds every
// factor with its source; the Usage card applies data-center overhead (PUE)
// and a grid's carbon intensity, which the reader can change.

// carbonCategories are the token categories the calculator prices, in the
// order the Usage card lists them. Output includes reasoning.
var carbonCategories = []string{"uncached_input", "cache_write", "cache_read", "output"}

// carbonScenarios pair the low, central, and high value of every factor.
var carbonScenarios = []string{"low", "central", "high"}

type carbonRange struct {
	Low     float64 `json:"low"`
	Central float64 `json:"central"`
	High    float64 `json:"high"`
}

func (r carbonRange) get(scenario string) float64 {
	switch scenario {
	case "low":
		return r.Low
	case "high":
		return r.High
	}
	return r.Central
}

type carbonTier struct {
	Key   string   `json:"key"`
	Label string   `json:"label"`
	Match []string `json:"match"`
	// OutputWhPerMTok is IT (server) energy per million output tokens,
	// before data-center overhead.
	OutputWhPerMTok carbonRange `json:"output_wh_per_mtok"`
	Basis           string      `json:"basis"`
	Sources         []string    `json:"sources"`
}

type carbonRatio struct {
	carbonRange
	Basis   string   `json:"basis"`
	Sources []string `json:"sources"`
}

type carbonGrid struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	GramsPerKWh float64  `json:"g_co2e_per_kwh"`
	Basis       string   `json:"basis"`
	Sources     []string `json:"sources"`
}

type carbonEquivalent struct {
	Key          string   `json:"key"`
	Label        string   `json:"label"`
	GramsPerUnit float64  `json:"g_co2e_per_unit"`
	Sources      []string `json:"sources"`
}

type carbonSource struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Published string `json:"published"`
	Finding   string `json:"finding"`
}

type carbonDocument struct {
	Version     int    `json:"version"`
	RetrievedAt string `json:"retrieved_at"`
	// Tiers are matched in order against the model name; the first tier
	// with a matching substring wins, and DefaultTier takes the rest.
	Tiers       []carbonTier `json:"tiers"`
	DefaultTier string       `json:"default_tier"`
	// CategoryRatios are energy per token relative to an output token, keyed
	// by every category in carbonCategories except output.
	CategoryRatios map[string]carbonRatio `json:"ratios"`
	PUE            carbonRatio            `json:"pue"`
	Grids          []carbonGrid           `json:"grids"`
	DefaultGrid    string                 `json:"default_grid"`
	Equivalents    []carbonEquivalent     `json:"equivalents"`
	Caveats        []string               `json:"caveats"`
	Sources        []carbonSource         `json:"sources"`
}

func carbonDocumentBytes() ([]byte, error) {
	if value, err := assets.ReadFile("assets/carbon/co2_factors.json"); err == nil {
		return value, nil
	}
	// `macos/build-app.sh` copies the canonical file into embedded assets; these
	// fallbacks keep `go test` and `go run` working from the source tree.
	for _, candidate := range []string{
		filepath.Join("carbon", "co2_factors.json"),
		filepath.Join("..", "..", "carbon", "co2_factors.json"),
	} {
		if value, err := os.ReadFile(candidate); err == nil {
			return value, nil
		}
	}
	return nil, fmt.Errorf("carbon factors file not found: carbon/co2_factors.json")
}

func parseCarbon(data []byte) (carbonDocument, []string) {
	var doc carbonDocument
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return doc, []string{fmt.Sprintf("carbon json: %v", err)}
	}
	return doc, validateCarbon(doc)
}

// validateCarbon checks that every factor is ordered low ≤ central ≤ high,
// positive, and cites a listed https source.
func validateCarbon(doc carbonDocument) []string {
	problems := []string{}
	if doc.Version != 1 {
		problems = append(problems, "version must be 1")
	}
	if _, err := time.Parse("2006-01-02", doc.RetrievedAt); err != nil {
		problems = append(problems, "retrieved_at must be YYYY-MM-DD")
	}
	sources := map[string]bool{}
	for index, source := range doc.Sources {
		if source.ID == "" || source.Title == "" || !strings.HasPrefix(source.URL, "https://") || source.Finding == "" {
			problems = append(problems, fmt.Sprintf("sources[%d] %s: id, title, https url, and finding are required", index, source.ID))
		}
		if sources[source.ID] {
			problems = append(problems, fmt.Sprintf("sources[%d] %s: duplicate id", index, source.ID))
		}
		sources[source.ID] = true
	}
	cited := func(label string, ids []string) {
		if len(ids) == 0 {
			problems = append(problems, label+": cite at least one source")
		}
		for _, id := range ids {
			if !sources[id] {
				problems = append(problems, fmt.Sprintf("%s: unknown source %q", label, id))
			}
		}
	}
	ordered := func(label string, value carbonRange) {
		if !(value.Low > 0 && value.Low <= value.Central && value.Central <= value.High) {
			problems = append(problems, label+": need 0 < low ≤ central ≤ high")
		}
	}
	tiers := map[string]bool{}
	for index, tier := range doc.Tiers {
		label := fmt.Sprintf("tiers[%d] %s", index, tier.Key)
		if tier.Key == "" || tier.Label == "" || tier.Basis == "" {
			problems = append(problems, label+": key, label, and basis are required")
		}
		tiers[tier.Key] = true
		ordered(label, tier.OutputWhPerMTok)
		cited(label, tier.Sources)
	}
	if !tiers[doc.DefaultTier] {
		problems = append(problems, "default_tier must name a tier")
	}
	for _, category := range carbonCategories[:len(carbonCategories)-1] {
		ratio, ok := doc.CategoryRatios[category]
		if !ok {
			problems = append(problems, "ratios."+category+" is required")
			continue
		}
		ordered("ratios."+category, ratio.carbonRange)
		cited("ratios."+category, ratio.Sources)
	}
	for category := range doc.CategoryRatios {
		if category == "output" || !slices.Contains(carbonCategories, category) {
			problems = append(problems, "ratios."+category+": not a token category (output is 1 by definition)")
		}
	}
	ordered("pue", doc.PUE.carbonRange)
	if doc.PUE.Low < 1 {
		problems = append(problems, "pue: cannot be below 1")
	}
	cited("pue", doc.PUE.Sources)
	grids := map[string]bool{}
	for index, grid := range doc.Grids {
		label := fmt.Sprintf("grids[%d] %s", index, grid.Key)
		if grid.Key == "" || grid.Label == "" || grid.GramsPerKWh <= 0 {
			problems = append(problems, label+": key, label, and a positive g_co2e_per_kwh are required")
		}
		grids[grid.Key] = true
		cited(label, grid.Sources)
	}
	if !grids[doc.DefaultGrid] {
		problems = append(problems, "default_grid must name a grid")
	}
	for index, equivalent := range doc.Equivalents {
		label := fmt.Sprintf("equivalents[%d] %s", index, equivalent.Key)
		if equivalent.Key == "" || equivalent.Label == "" || equivalent.GramsPerUnit <= 0 {
			problems = append(problems, label+": key, label, and a positive g_co2e_per_unit are required")
		}
		cited(label, equivalent.Sources)
	}
	return problems
}

func loadCarbonDocument() (carbonDocument, error) {
	data, err := carbonDocumentBytes()
	if err != nil {
		return carbonDocument{}, err
	}
	doc, problems := parseCarbon(data)
	if len(problems) > 0 {
		return doc, fmt.Errorf("carbon/co2_factors.json: %s", strings.Join(problems, "; "))
	}
	return doc, nil
}

// tier places a model in a size tier by the first matching name fragment.
// Unrecognized and placeholder models take the default tier.
func (doc carbonDocument) tier(model string) (string, bool) {
	family := modelFamily(model)
	for _, tier := range doc.Tiers {
		for _, fragment := range tier.Match {
			if fragment != "" && strings.Contains(family, strings.ToLower(fragment)) {
				return tier.Key, true
			}
		}
	}
	return doc.DefaultTier, false
}

// carbonTokens splits a ledger row into the calculator's categories.
// Unclassified tokens are charged as uncached input, the costlier input kind.
func carbonTokens(row map[string]any) map[string]int64 {
	return map[string]int64{
		"uncached_input": integer(row["uncached_input_tokens"]) + integer(row["unclassified_tokens"]),
		"cache_write":    integer(row["cache_creation_input_tokens"]),
		"cache_read":     integer(row["cache_read_input_tokens"]),
		"output":         integer(row["output_tokens"]),
	}
}

// carbonAssumptions are the factors the Carbon page's settings() selects for
// the reader's saved choices (the pharos-carbon-v1 preference). Usage rows
// and the menu bar apply them with calculate()'s arithmetic, so their totals
// match the page.
type carbonAssumptions struct {
	perMillion map[string]float64 // IT Wh per million output tokens, by tier
	ratio      map[string]float64 // energy relative to output, by category
	pue, grid  float64
}

func (doc carbonDocument) assumptions(prefs map[string]any) carbonAssumptions {
	scenario := func(key string) string {
		value := firstString(prefs[key])
		if value == "low" || value == "high" {
			return value
		}
		return "central"
	}
	gridKey := defaultString(prefs["grid"], doc.DefaultGrid)
	grid := 0.0
	found := false
	for _, candidate := range doc.Grids {
		if candidate.Key == gridKey {
			grid, found = candidate.GramsPerKWh, true
		}
	}
	if gridKey == "custom" {
		custom, _ := number(prefs["customGrid"])
		if math.IsNaN(custom) {
			custom = 0
		}
		grid, found = max(0, custom), true
	}
	if !found {
		for _, candidate := range doc.Grids {
			if candidate.Key == doc.DefaultGrid {
				grid = candidate.GramsPerKWh
			}
		}
	}
	scale := 1.0
	if strings.EqualFold(firstString(prefs["inputSensitivity"]), "third") {
		scale = 5.0 / 3
	}
	a := carbonAssumptions{perMillion: map[string]float64{}, ratio: map[string]float64{"output": 1},
		pue: doc.PUE.get(scenario("pueScenario")), grid: grid}
	for _, tier := range doc.Tiers {
		a.perMillion[tier.Key] = tier.OutputWhPerMTok.get(scenario("scenario"))
	}
	for _, category := range carbonCategories[:len(carbonCategories)-1] {
		a.ratio[category] = doc.CategoryRatios[category].get(scenario("scenario")) * scale
	}
	return a
}

// wh is IT energy before PUE, in calculate()'s operation order.
func (a carbonAssumptions) wh(tier, category string, tokens float64) float64 {
	return tokens / 1e6 * a.perMillion[tier] * a.ratio[category]
}

func (a carbonAssumptions) grams(wh float64) float64 { return wh * a.pue * a.grid / 1000 }

// key identifies the resolved factors, so values derived from them can be
// cached until the reader's settings or the factors file change.
func (a carbonAssumptions) key() string {
	hash := fnv.New64a()
	fmt.Fprint(hash, a.perMillion, a.ratio, a.pue, a.grid)
	return strconv.FormatUint(hash.Sum64(), 36)
}

// carbonPreferences reads the Carbon page's saved assumptions.
func (c *Catalog) carbonPreferences(ctx context.Context) (map[string]any, error) {
	var value string
	err := c.reader().QueryRowContext(ctx, "SELECT value FROM ui_preferences WHERE key='pharos-carbon-v1'").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	var prefs map[string]any
	_ = json.Unmarshal([]byte(value), &prefs)
	return prefs, err
}

func (c *Catalog) carbonAssumptions(ctx context.Context) (carbonAssumptions, error) {
	doc, err := loadCarbonDocument()
	if err != nil {
		return carbonAssumptions{}, err
	}
	prefs, err := c.carbonPreferences(ctx)
	return doc.assumptions(prefs), err
}

// withCarbon copies usage rows with their estimated facility energy and
// emissions under the given assumptions. Rows carry carbon_tier already.
// withCarbon fills the estimate columns in place; callers pass rows they just
// built, so no shared cache copy is modified.
func withCarbon(rows []map[string]any, a carbonAssumptions) []map[string]any {
	for _, row := range rows {
		tier, tokens, wh := firstString(row["carbon_tier"]), carbonTokens(row), 0.0
		for _, category := range carbonCategories {
			wh += a.wh(tier, category, float64(tokens[category]))
		}
		row["energy_wh"], row["co2e_g"] = wh*a.pue, a.grams(wh)
		row["energy_kwh"], row["co2e_kg"] = wh*a.pue/1000, a.grams(wh)/1000
	}
	return rows
}

type carbonWindow struct {
	Key string `json:"key"`
	// Tokens by tier, then category.
	Tokens map[string]map[string]int64 `json:"tokens"`
	// EnergyWh is IT energy before PUE, by scenario, then category, plus
	// "total".
	EnergyWh map[string]map[string]float64 `json:"energy_wh"`
	// CO2eGrams applies each scenario's PUE and the default grid.
	CO2eGrams map[string]float64 `json:"co2e_g"`
}

// energy fills the window's IT energy from its token counts.
func (doc carbonDocument) energy(window *carbonWindow) {
	perTier := map[string]carbonRange{}
	for _, tier := range doc.Tiers {
		perTier[tier.Key] = tier.OutputWhPerMTok
	}
	grid := 0.0
	for _, candidate := range doc.Grids {
		if candidate.Key == doc.DefaultGrid {
			grid = candidate.GramsPerKWh
		}
	}
	window.EnergyWh, window.CO2eGrams = map[string]map[string]float64{}, map[string]float64{}
	for _, scenario := range carbonScenarios {
		energy := map[string]float64{"total": 0}
		for _, category := range carbonCategories {
			ratio := 1.0
			if category != "output" {
				ratio = doc.CategoryRatios[category].get(scenario)
			}
			sum := 0.0
			for tier, tokens := range window.Tokens {
				sum += float64(tokens[category]) / 1e6 * perTier[tier].get(scenario) * ratio
			}
			energy[category] = sum
			energy["total"] += sum
		}
		window.EnergyWh[scenario] = energy
		window.CO2eGrams[scenario] = energy["total"] * doc.PUE.get(scenario) * grid / 1000
	}
}

// carbonHealth answers GET /api/health/carbon: token totals by tier and
// category for all time and the last 30 days, their estimated energy and
// emissions, the factors behind them, and which models took the default tier.
func (c *Catalog) carbonHealth() (map[string]any, error) {
	doc, err := loadCarbonDocument()
	if err != nil {
		return nil, err
	}
	rows, err := c.localUsageBaseRows(context.Background(), false)
	if err != nil {
		return nil, err
	}
	since := c.clock().AddDate(0, 0, -29).Format("2006-01-02")
	all := &carbonWindow{Key: "all", Tokens: map[string]map[string]int64{}}
	recent := &carbonWindow{Key: "30d", Tokens: map[string]map[string]int64{}}
	unmatched := map[string]int64{}
	for _, row := range rows {
		model := firstString(row["model"])
		tier, matched := doc.tier(model)
		tokens := carbonTokens(row)
		windows := []*carbonWindow{all}
		if firstString(row["day"]) >= since {
			windows = append(windows, recent)
		}
		for _, window := range windows {
			if window.Tokens[tier] == nil {
				window.Tokens[tier] = map[string]int64{}
			}
			for category, count := range tokens {
				window.Tokens[tier][category] += count
			}
		}
		if !matched && !placeholderModel(model) {
			unmatched[model] += integer(row["total_tokens"])
		}
	}
	doc.energy(all)
	doc.energy(recent)
	models := []map[string]any{}
	for model, tokens := range unmatched {
		models = append(models, map[string]any{"model": model, "tokens": tokens})
	}
	sort.Slice(models, func(i, j int) bool { return integer(models[i]["tokens"]) > integer(models[j]["tokens"]) })
	return map[string]any{"factors": doc, "windows": []*carbonWindow{all, recent}, "default_tier_models": models,
		"generated_at": c.clock().UTC().Format(time.RFC3339)}, nil
}

// CarbonHealth provides cached whole-library totals for the CO₂ calculator.
func (c *Catalog) CarbonHealth(ctx context.Context) (map[string]any, error) {
	return cachedValue(ctx, c, "health:carbon", func(context.Context) (map[string]any, error) {
		return c.carbonHealth()
	})
}

// carbonSeriesRequest uses the Usage dataset's filters, but its view state is
// independent of Machine Tokens. Assumptions are applied by the calculator UI.
type carbonSeriesRequest struct {
	Where  []querytable.WhereTerm `json:"where"`
	Hourly bool                   `json:"hourly"`
	Split  string                 `json:"split"`
}

type carbonPoint struct {
	Day    string                      `json:"day"`
	Value  string                      `json:"value"`
	Tokens map[string]map[string]int64 `json:"tokens"`
}

func addCarbonTokens(target map[string]map[string]int64, tier string, counts map[string]int64) {
	if target[tier] == nil {
		target[tier] = map[string]int64{}
	}
	for category, count := range counts {
		target[tier][category] += count
	}
}

// carbonSeries returns counts before energy assumptions, so chart and totals
// can recalculate together without refetching. Filter at ledger-hour precision
// even for daily charts: a partial-day range must not include the whole day.
func (c *Catalog) carbonSeries(ctx context.Context, request carbonSeriesRequest, schema querytable.Schema) (map[string]any, error) {
	if !slices.Contains([]string{"", "provider", "repository_name", "tier"}, request.Split) {
		return nil, fmt.Errorf("carbon series cannot split by %q", request.Split)
	}
	doc, err := loadCarbonDocument()
	if err != nil {
		return nil, err
	}
	rows, err := c.hourlyUsageForAggregation(ctx, request.Where, schema)
	if err != nil {
		return nil, err
	}
	total := &carbonWindow{Key: "filtered", Tokens: map[string]map[string]int64{}}
	points := map[[2]string]*carbonPoint{}
	unmatched := map[string]int64{}
	undated := int64(0)
	unknownModelTokens, unclassified := int64(0), int64(0)
	for _, row := range rows {
		model := firstString(row["model"])
		tier, matched := doc.tier(model)
		counts := carbonTokens(row)
		unclassified += integer(row["unclassified_tokens"])
		addCarbonTokens(total.Tokens, tier, counts)
		if !matched {
			if placeholderModel(model) {
				unknownModelTokens += integer(row["total_tokens"])
			} else {
				unmatched[model] += integer(row["total_tokens"])
			}
		}
		day := firstString(row["day"])
		if request.Hourly {
			day = firstString(row["hour"])
		}
		if day == "" {
			undated += integer(row["total_tokens"])
			continue
		}
		value := firstString(row[request.Split])
		if request.Split == "tier" {
			value = tier
		}
		key := [2]string{day, value}
		point := points[key]
		if point == nil {
			point = &carbonPoint{Day: day, Value: value, Tokens: map[string]map[string]int64{}}
			points[key] = point
		}
		addCarbonTokens(point.Tokens, tier, counts)
	}
	series := make([]*carbonPoint, 0, len(points))
	for _, point := range points {
		series = append(series, point)
	}
	sort.Slice(series, func(i, j int) bool {
		if series[i].Day == series[j].Day {
			return series[i].Value < series[j].Value
		}
		return series[i].Day < series[j].Day
	})
	models := make([]map[string]any, 0, len(unmatched))
	for model, tokens := range unmatched {
		models = append(models, map[string]any{"model": model, "tokens": tokens})
	}
	sort.Slice(models, func(i, j int) bool {
		left, right := integer(models[i]["tokens"]), integer(models[j]["tokens"])
		if left == right {
			return firstString(models[i]["model"]) < firstString(models[j]["model"])
		}
		return left > right
	})
	// Choices come from the full retained usage, so one filter cannot hide
	// the other filter's options or strand a selection with no matching rows.
	all, err := c.localUsageBaseRows(ctx, false)
	if err != nil {
		return nil, err
	}
	choices := map[string][]string{}
	for _, field := range []string{"repository_name", "provider"} {
		values := map[string]bool{}
		for _, row := range all {
			values[firstString(row[field])] = true
		}
		choices[field] = make([]string, 0, len(values))
		for value := range values {
			choices[field] = append(choices[field], value)
		}
		sort.Strings(choices[field])
	}
	return map[string]any{"factors": doc, "windows": []*carbonWindow{total}, "series": series,
		"choices": choices, "default_tier_models": models, "default_tier_unknown_tokens": unknownModelTokens,
		"unclassified_tokens": unclassified, "undated_tokens": undated}, nil
}
