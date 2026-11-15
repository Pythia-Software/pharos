package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// queryResultMaxAge is how long a query whose window is relative to now
// ("-7d" to "+0s") stays fresh while the catalog does not change: its rows
// only age out of the window meanwhile.
var queryResultMaxAge = 10 * time.Minute

// localZoneIdentity tells the service's time zones apart. The system zone
// has no name in Go ("Local"), so it is known by its abbreviation and
// offset in midwinter and midsummer of the current year: the same all year
// in one zone, and different between zones that keep different time.
func localZoneIdentity(now time.Time) string {
	var parts []string
	for _, month := range []time.Month{time.January, time.July} {
		name, offset := time.Date(now.Year(), month, 1, 12, 0, 0, 0, time.Local).Zone()
		parts = append(parts, name+strconv.Itoa(offset))
	}
	return strings.Join(parts, "/")
}

// buildIdentity names the running program: its executable's size and
// modification time, which an update changes.
var buildIdentity = sync.OnceValue(func() string {
	if path, err := os.Executable(); err == nil {
		if info, err := os.Stat(path); err == nil {
			return strconv.FormatInt(info.Size(), 36) + "." + strconv.FormatInt(info.ModTime().UnixNano(), 36)
		}
	}
	return "unknown"
})

// queryResultSettings are the saved settings a query dataset reads on the
// service rather than from its request: Scatter and Parallelism read the
// Parallelism gap and counting, People message labels the Habits engaged
// gap. A change to one makes every stored answer stale. A dataset that
// starts reading another saved setting this way must add its key here.
var queryResultSettings = []any{parallelismSettingsKey, habitsSettingsKey}

// queryResultLedgers name the state of append-only logs outside the
// cached-view generation, by scope (queryDatasetGeneration). The MCP call
// log only gains rows, and trims its oldest in the same transaction, so its
// newest id changes whenever its contents do.
var queryResultLedgers = map[string]string{
	generationMCPCalls: "SELECT COALESCE(MAX(id),0) FROM mcp_calls",
}

// queryResults caches query-v3 responses: a card's or a dashboard's request,
// keyed by its dataset, URL parameters and body without the client's clock.
// Its scope is the dataset's cache generation (queryDatasetGeneration), so a
// labeling commit leaves a Usage card fresh.
// queryResultRows is the longest list of rows stored: a first page of at
// most this many. Anything longer, or further in, is a table.
var queryResultRows int64 = 50

var queryResults = registerResultDomain(&resultDomain{
	name: "query-v3", version: "1", maxAge: queryResultMaxAge,
	stamp: func(ctx context.Context, s *Server, scope string) (string, error) {
		generation, err := s.Catalog.storedGeneration(ctx, scope)
		// Day and hour fields are computed in the service's time zone, and
		// another build may answer the same request differently (a fixed
		// query, new pricing, a migration's backfill) without any row
		// changing afterwards.
		if err != nil {
			return "", err
		}
		// Saved settings the service applies itself are in neither the
		// request nor the generation, so they are named here.
		var settings string
		err = s.Catalog.reader().QueryRowContext(ctx, `SELECT COALESCE(group_concat(key||'='||value, char(10)),'') FROM (SELECT key,value FROM ui_preferences WHERE key IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(queryResultSettings)), ",")+`) ORDER BY key)`, queryResultSettings...).Scan(&settings)
		saved := sha256.Sum256([]byte(settings))
		stamp := strconv.FormatInt(generation, 10) + " " + localZoneIdentity(time.Now()) + " " + buildIdentity() + " " + hex.EncodeToString(saved[:6])
		if ledger := queryResultLedgers[scope]; ledger != "" && err == nil {
			var latest int64
			err = s.Catalog.reader().QueryRowContext(ctx, ledger).Scan(&latest)
			stamp += " " + scope + "=" + strconv.FormatInt(latest, 10)
		}
		return stamp, err
	},
	build: func(ctx context.Context, s *Server, replay []byte, now time.Time) ([]byte, error) {
		var stored queryReplay
		if err := json.Unmarshal(replay, &stored); err != nil {
			return nil, err
		}
		params, err := url.ParseQuery(stored.Params)
		if err != nil {
			return nil, err
		}
		if now.IsZero() {
			now = time.Now()
		}
		// The body is an object without its clock (queryResultRequest).
		decoder := json.NewDecoder(bytes.NewReader(stored.Body))
		decoder.UseNumber()
		var fields map[string]any
		if err := decoder.Decode(&fields); err != nil {
			return nil, err
		}
		clockInstants(fields, now, false)
		fields["now"] = now.UnixMilli()
		body, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		return s.queryV3Response(ctx, stored.Dataset, params, body)
	},
	fail: qt.WriteQueryError,
})

type queryReplay struct {
	Dataset string          `json:"dataset"`
	Params  string          `json:"params"`
	Body    json.RawMessage `json:"body"`
}

// relativeInstant is a bound resolved against the request's clock: one the
// client wrote that way ("-7d"), or an instant clockInstants rewrote.
var relativeInstant = regexp.MustCompile(`^[+-]\d+[a-zA-Z]+$|^` + clockInstant)

// clockInstant marks a filter value that was an instant a whole number of
// hours from the request's clock: a window the client computed from its page
// load ("the last 37 days, to now") rather than from the calendar. Stored as
// its distance from the clock, it is one entry at every page load, and a
// build puts the instant back for the clock it runs at.
const clockInstant = "@now"

const clockInstantLayout = "2006-01-02T15:04:05.000Z"

// clockInstants rewrites the filter values of a decoded query body: with
// mark, instants a whole number of hours from now become marks; without,
// marks become instants again. A calendar bound (the start of today) is that
// far from the clock only by coincidence, and then names the same instant.
func clockInstants(value any, now time.Time, mark bool) {
	rewrite := func(text string) string {
		if mark {
			at, err := time.Parse(clockInstantLayout, text)
			if offset := at.Sub(now); err == nil && offset%time.Hour == 0 {
				return clockInstant + strconv.FormatInt(int64(offset/time.Hour), 10)
			}
		} else if hours, err := strconv.ParseInt(strings.TrimPrefix(text, clockInstant), 10, 64); err == nil && strings.HasPrefix(text, clockInstant) {
			return now.Add(time.Duration(hours) * time.Hour).UTC().Format(clockInstantLayout)
		}
		return text
	}
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			clockInstants(item, now, mark)
		}
	case map[string]any:
		_, field := value["field"]
		_, op := value["op"]
		for name, item := range value {
			switch item := item.(type) {
			case string:
				if field && op && name == "value" {
					value[name] = rewrite(item)
				}
			case []any:
				if field && op && name == "value" {
					for i, element := range item {
						if text, ok := element.(string); ok {
							item[i] = rewrite(text)
						}
					}
				}
				clockInstants(item, now, mark)
			default:
				clockInstants(item, now, mark)
			}
		}
	}
}

// queryResultRequest is a query-v3 request as the cache stores it: the body
// re-encoded with sorted keys and without "now", so two page loads of one
// card are one entry. A body it cannot read that way is not an error here:
// the caller answers it uncached, and the decoder reports what is wrong.
func queryResultRequest(dataset string, params url.Values, body []byte) (resultRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var fields map[string]any
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return resultRequest{}, errors.New("query body is not a JSON object")
	}
	if rows, _ := fields["rows"].(bool); rows {
		// A table is read live: its pages are large, each is read once, and
		// together they must agree. A short list (a card's few rows) is one
		// self-contained answer, and is stored like any other.
		result, _ := fields["result"].(map[string]any)
		limit, _ := result["limit"].(json.Number)
		offset, _ := result["offset"].(json.Number)
		if n, err := limit.Int64(); err != nil || n > queryResultRows || offset != "" && offset != "0" {
			return resultRequest{}, errors.New("a page of table rows is not stored")
		}
	}
	request := resultRequest{scope: queryDatasetGeneration(dataset)}
	number, _ := fields["now"].(json.Number)
	ms, err := number.Int64()
	if err != nil || ms <= 0 {
		return resultRequest{}, errors.New("query body has no clock")
	}
	request.now = time.UnixMilli(ms)
	delete(fields, "now")
	clockInstants(fields, request.now, true)
	request.clock = readsClock(fields)
	canonical, err := json.Marshal(fields)
	if err != nil {
		return resultRequest{}, err
	}
	request.replay, err = json.Marshal(queryReplay{Dataset: dataset, Params: params.Encode(), Body: canonical})
	return request, err
}

func readsClock(value any) bool {
	switch value := value.(type) {
	case string:
		return relativeInstant.MatchString(value)
	case []any:
		for _, item := range value {
			if readsClock(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range value {
			if readsClock(item) {
				return true
			}
		}
	}
	return false
}
