package archive

import (
	"math"
	"strconv"
)

// Fine bins of one numeric value, for the dashboards' heatmap over a
// continuous axis (web/src/value-bins.ts). A record stores its bin as the
// bin's lower edge, so a query groups by it. Both sides compute an edge by
// parsing the same decimal text, so the doubles agree exactly
// (testdata/value-bins.json).
//
//   - binLog: ten bins a decade at 1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6 and 8 times
//     a power of ten. Zero and below is one bin at 0.
//   - binCount: whole numbers. 0 to 9 are a bin each, then log bins from 10.
//   - binLinear: steps of 1/per from 0. Below 0 is clamped to 0.
type binScheme struct {
	scale string
	per   int
}

var (
	binLog   = binScheme{scale: "log"}
	binCount = binScheme{scale: "count"}
)

func binLinear(per int) binScheme { return binScheme{scale: "linear", per: per} }

var logMantissas = []string{"1", "1.2", "1.5", "2", "2.5", "3", "4", "5", "6", "8"}

func binEdge(mantissa string, decade int) float64 {
	value, _ := strconv.ParseFloat(mantissa+"e"+strconv.Itoa(decade), 64)
	return value
}

// valueBin is the lower edge of the bin holding value.
func valueBin(value float64, scheme binScheme) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	if scheme.scale == "linear" {
		per := float64(scheme.per)
		index := max(0, math.Floor(value*per))
		for index > 0 && index/per > value {
			index--
		}
		for (index+1)/per <= value {
			index++
		}
		return index / per
	}
	if value <= 0 {
		return 0
	}
	if scheme.scale == "count" && value < 10 {
		return math.Floor(value)
	}
	decade := int(math.Floor(math.Log10(value)))
	for binEdge("1", decade) > value {
		decade--
	}
	for binEdge("1", decade+1) <= value {
		decade++
	}
	found := binEdge("1", decade)
	for _, mantissa := range logMantissas {
		if edge := binEdge(mantissa, decade); edge <= value {
			found = edge
		}
	}
	return found
}
