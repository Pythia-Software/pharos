package archive

import (
	"math"
	"strconv"
	"strings"
)

var tokenUnits = [...]string{"", "K", "M", "B", "T"}

// formatTokens writes a token count as the web UI does (token-count.js):
// three significant figures, at most one decimal below 1,000, then K, M, B
// and T. A value that rounds up to the next unit displays in it: 999,500 is
// "1M". testdata/token-count-cases.json holds the cases both share.
func formatTokens(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return ""
	}
	abs, sign := math.Abs(value), ""
	if abs < 0.05 {
		return "0"
	}
	if value < 0 {
		sign = "-"
	}
	if abs < 999.5 {
		if rounded := math.Round(abs*10) / 10; rounded < 100 {
			return sign + strings.TrimSuffix(strconv.FormatFloat(rounded, 'f', 1, 64), ".0")
		}
		return sign + strconv.FormatFloat(math.Round(abs), 'f', 0, 64)
	}
	step := math.Pow(10, math.Floor(math.Log10(abs))-2)
	rounded := math.Round(abs/step) * step
	unit := min(len(tokenUnits)-1, int(math.Floor(math.Log10(rounded)+1e-9))/3)
	scaled := rounded / math.Pow(1000, float64(unit))
	decimals := max(0, 2-int(math.Floor(math.Log10(scaled)+1e-9)))
	text := strconv.FormatFloat(scaled, 'f', decimals, 64)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	return sign + text + tokenUnits[unit]
}
