package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"math/big"
	"reflect"
	"sort"
	"time"

	"github.com/gbdubs/pharos/pricing"
)

// teamsExactPriceBook is usable only when the current SQLite price/alias book
// exactly matches the pinned embedded book. Rates used for money are parsed as
// decimal rationals from the immutable source, never from SQLite REAL values.
type teamsExactPriceBook struct {
	native priceBook
	rates  map[string][3]*big.Rat // input, cache read, output per million tokens
}

func (c *Catalog) teamsLoadExactPriceBook(ctx context.Context, tx *sql.Tx, pinnedDigest string) (*teamsExactPriceBook, error) {
	if pinnedDigest == "" || pinnedDigest != hashBytes(pricing.CostChanges) {
		return nil, nil
	}
	var current string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='pricing_digest'`).Scan(&current)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if current != pinnedDigest {
		return nil, nil
	}
	parsed, problems := parsePricing(pricing.CostChanges)
	if len(problems) != 0 {
		return nil, nil
	}
	native, err := c.loadPriceBookFrom(ctx, tx)
	if err != nil {
		return nil, err
	}
	expected := priceBook{prices: map[string][]priceInterval{}, aliases: map[string][]aliasInterval{}}
	seenPrices := map[string]bool{}
	seenAliases := map[string]bool{}
	for _, x := range parsed.Changes {
		if x.Status != "confirmed" {
			continue
		}
		key := modelFamily(x.Model)
		identity := key + "\x00" + x.EffectiveFrom
		if key == "" || seenPrices[identity] {
			return nil, nil
		}
		seenPrices[identity] = true
		expected.prices[key] = append(expected.prices[key], priceInterval{from: x.EffectiveFrom, model: x.Model, rates: [5]*float64{x.Input, x.CacheRead, x.CacheWrite5m, x.CacheWrite1h, x.Output}, assumption: x.Assumption})
	}
	for _, x := range parsed.Aliases {
		if x.Status != "confirmed" {
			continue
		}
		key := modelFamily(x.Alias)
		identity := key + "\x00" + x.EffectiveFrom
		if key == "" || seenAliases[identity] {
			return nil, nil
		}
		seenAliases[identity] = true
		expected.aliases[key] = append(expected.aliases[key], aliasInterval{from: x.EffectiveFrom, modelKey: modelFamily(x.Model)})
	}
	for key, rows := range expected.prices {
		sort.Slice(rows, func(i, j int) bool { return rows[i].from < rows[j].from })
		for i := 0; i+1 < len(rows); i++ {
			rows[i].to = rows[i+1].from
		}
		expected.prices[key] = rows
	}
	for key, rows := range expected.aliases {
		sort.Slice(rows, func(i, j int) bool { return rows[i].from < rows[j].from })
		for i := 0; i+1 < len(rows); i++ {
			rows[i].to = rows[i+1].from
		}
		expected.aliases[key] = rows
	}
	for key, rows := range native.prices {
		sort.Slice(rows, func(i, j int) bool { return rows[i].from < rows[j].from })
		native.prices[key] = rows
	}
	for key, rows := range native.aliases {
		sort.Slice(rows, func(i, j int) bool { return rows[i].from < rows[j].from })
		native.aliases[key] = rows
	}
	// A manually edited library price or alias cannot silently inherit the
	// embedded book's identity, even when its metadata digest was not changed.
	if !reflect.DeepEqual(native, expected) {
		return nil, nil
	}
	var raw struct {
		Changes []struct {
			Model  string       `json:"model"`
			From   string       `json:"effective_from"`
			Status string       `json:"status"`
			Input  *json.Number `json:"input"`
			Cached *json.Number `json:"cache_read"`
			Output *json.Number `json:"output"`
		} `json:"changes"`
	}
	decoder := json.NewDecoder(bytes.NewReader(pricing.CostChanges))
	decoder.UseNumber()
	if err = decoder.Decode(&raw); err != nil {
		return nil, err
	}
	book := &teamsExactPriceBook{native: native, rates: map[string][3]*big.Rat{}}
	for _, x := range raw.Changes {
		if x.Status != "confirmed" {
			continue
		}
		var rates [3]*big.Rat
		for i, n := range []*json.Number{x.Input, x.Cached, x.Output} {
			if n == nil {
				continue
			}
			text := n.String()
			if len(text) > 64 {
				return nil, nil
			}
			r, ok := new(big.Rat).SetString(text)
			if !ok || r.Sign() < 0 || r.Cmp(big.NewRat(1000000, 1)) > 0 {
				return nil, nil
			}
			rates[i] = r
		}
		key := modelFamily(x.Model)
		identity := key + "\x00" + x.From
		if _, duplicate := book.rates[identity]; duplicate {
			return nil, nil
		}
		interval := native.price(key, x.From)
		if interval == nil || interval.from != x.From {
			return nil, nil
		}
		for i, nativeIndex := range []int{0, 1, 4} {
			value := interval.rates[nativeIndex]
			if (rates[i] == nil) != (value == nil) {
				return nil, nil
			}
			if value != nil {
				rounded, _ := rates[i].Float64()
				if rounded != *value {
					return nil, nil
				}
			}
		}
		book.rates[identity] = rates
	}
	return book, nil
}

// priceFact returns nil for unavailable categories or unmatched prices. Rates
// are USD/million tokens: multiplying by a count gives microdollars. One
// nonnegative fact is rounded once, half up, to the nearest microdollar.
func (b *teamsExactPriceBook) priceFact(f teamsCodexRequestFact) (*int64, bool) {
	if b == nil || f.Input < 0 || f.Output < 0 || f.Input > teamsMaxExactInteger || f.Output > teamsMaxExactInteger || (!f.Cached.Known && (f.Input > 0 || f.Cached.Value != 0)) || (f.Cached.Known && (f.Cached.Value < 0 || f.Cached.Value > f.Input)) {
		return nil, false
	}
	instant, err := time.Parse(time.RFC3339Nano, f.Timestamp)
	if err != nil || instant.UTC().Format(time.RFC3339Nano) != f.Timestamp {
		return nil, false
	}
	day := instant.Format("2006-01-02")
	key := b.native.resolve(f.Model, day)
	interval := b.native.price(key, day)
	if interval == nil {
		return nil, false
	}
	rates, ok := b.rates[key+"\x00"+interval.from]
	if !ok {
		return nil, false
	}
	cached := f.Cached.Value
	quantities := [3]int64{f.Input - cached, cached, f.Output}
	sum := new(big.Rat)
	for i, n := range quantities {
		if n == 0 {
			continue
		}
		if rates[i] == nil {
			return nil, false
		}
		sum.Add(sum, new(big.Rat).Mul(new(big.Rat).SetInt64(n), rates[i]))
	}
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(sum.Num(), sum.Denom(), r)
	if new(big.Int).Lsh(r, 1).Cmp(sum.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() || q.Sign() < 0 || q.Int64() > teamsMaxExactInteger {
		return nil, false
	}
	value := q.Int64()
	return &value, interval.assumption
}
