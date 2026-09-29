package archive

import (
	"fmt"
	"math"
	"strings"
)

// Wording helpers for detectors. Findings speak the user's language: one
// number per claim, rounded honestly ("1 of every 3" for 34%), and no
// internal terms (docs/auto-optimization-design.md, "Writing a finding").

// countNoun writes a count with its noun: "1 conversation", "12 conversations".
func countNoun(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	if strings.HasSuffix(noun, "s") || strings.HasSuffix(noun, "sh") || strings.HasSuffix(noun, "ch") {
		return fmt.Sprintf("%s %ses", thousands(count), noun)
	}
	if strings.HasSuffix(noun, "y") && !strings.HasSuffix(noun, "ey") {
		return fmt.Sprintf("%s %sies", thousands(count), strings.TrimSuffix(noun, "y"))
	}
	return fmt.Sprintf("%s %ss", thousands(count), noun)
}

func thousands(value int) string {
	text := fmt.Sprint(value)
	if value < 10_000 && value > -10_000 {
		if len(text) == 4 && value > 0 {
			return text[:1] + "," + text[1:]
		}
		return text
	}
	var out []string
	for len(text) > 3 {
		out = append([]string{text[len(text)-3:]}, out...)
		text = text[:len(text)-3]
	}
	return strings.Join(append([]string{text}, out...), ",")
}

// simpleFraction rounds a rate to a fraction a person would say: "1 of
// every n" when that is within about a tenth of the rate, otherwise the
// nearest a/b with b at most 10.
func simpleFraction(rate float64) (int, int) {
	best, bestB := math.Inf(1), 2
	for _, b := range []int{2, 3, 4, 5, 6, 7, 8, 9, 10, 12, 15, 20, 25, 30, 40, 50, 100} {
		if diff := math.Abs(1/float64(b)-rate) / rate; diff < best {
			best, bestB = diff, b
		}
	}
	if best <= 0.12 {
		return 1, bestB
	}
	bestA, bestB, best := 1, 2, math.Inf(1)
	for b := 2; b <= 10; b++ {
		for a := 1; a < b; a++ {
			if diff := math.Abs(float64(a)/float64(b) - rate); diff < best-1e-9 {
				best, bestA, bestB = diff, a, b
			}
		}
	}
	return bestA, bestB
}

// fractionPhrase says how often: "1 of every 3 conversations that ran
// pytest", "about half of them", "nearly all". With an empty noun it
// returns the bare ratio, as in "1 in 3".
func fractionPhrase(rate float64, noun string) string {
	switch {
	case rate <= 0:
		if noun == "" {
			return "none"
		}
		return "none of the " + noun
	case rate >= 0.95:
		if noun == "" {
			return "nearly all"
		}
		return "nearly all " + noun
	case rate < 0.01:
		if noun == "" {
			return "fewer than 1 in 100"
		}
		return "fewer than 1 in every 100 " + noun
	case rate >= 0.45 && rate <= 0.55:
		if noun == "" {
			return "about 1 in 2"
		}
		return "about half of the " + noun
	}
	a, b := simpleFraction(rate)
	if noun == "" {
		return fmt.Sprintf("%d in %d", a, b)
	}
	return fmt.Sprintf("%d of every %d %s", a, b, noun)
}

// dollars rounds money the way a person would say it.
func dollars(value float64) string {
	switch {
	case value >= 1000:
		return "$" + thousands(int(math.Round(value/10)*10))
	case value >= 10:
		return fmt.Sprintf("$%d", int(math.Round(value)))
	case value >= 1:
		return fmt.Sprintf("$%.1f", value)
	case value > 0:
		return "under $1"
	}
	return "$0"
}

// tokensPhrase writes a token count as "9.8M tokens".
func tokensPhrase(value float64) string {
	return compactNumber(value) + " tokens"
}

func compactNumber(value float64) string {
	switch abs := math.Abs(value); {
	case abs >= 1e9:
		return trimZero(fmt.Sprintf("%.1f", value/1e9)) + "B"
	case abs >= 1e6:
		return trimZero(fmt.Sprintf("%.1f", value/1e6)) + "M"
	case abs >= 1e3:
		return trimZero(fmt.Sprintf("%.0f", value/1e3)) + "k"
	}
	return fmt.Sprintf("%.0f", value)
}

func trimZero(text string) string { return strings.TrimSuffix(text, ".0") }

// minutesPhrase writes agent time as "6.1 h" or "40 min".
func minutesPhrase(minutes float64) string {
	if minutes >= 90 {
		return trimZero(fmt.Sprintf("%.1f", minutes/60)) + " h"
	}
	return fmt.Sprintf("%.0f min", math.Max(minutes, 0))
}

// dayLabel writes a local day as "Sep 27".
func dayLabel(day string) string {
	parsed := dayTime(day)
	if parsed.IsZero() {
		return day
	}
	return parsed.Format("Jan 2")
}
