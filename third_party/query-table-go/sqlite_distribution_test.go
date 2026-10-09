package querytable

import (
	"database/sql/driver"
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestSQLiteBoxQuartilesAndTailSampling(t *testing.T) {
	a := &sqliteBoxAggregate{sum: sqliteRat(0)}
	for i := 0; i < 30; i++ {
		a.Step([]driver.Value{float64(-100 - i), "tukey"})
	}
	for i := 0; i < 200; i++ {
		a.Step([]driver.Value{float64(0), "tukey"})
	}
	for i := 0; i < 30; i++ {
		a.Step([]driver.Value{float64(100 + i), "tukey"})
	}
	raw, e := a.Value()
	if e != nil {
		t.Fatal(e)
	}
	var payload struct {
		Value        *float64
		Error        string
		Distribution MetricBoxDistribution
	}
	if e = json.Unmarshal([]byte(raw.(string)), &payload); e != nil {
		t.Fatal(e)
	}
	s := payload.Distribution.Summary
	if payload.Error != "" || s.Q1 != 0 || s.Median != 0 || s.Q3 != 0 || s.Low != 0 || s.High != 0 || s.Mean != 0 || s.OutlierCount != 60 || len(s.Outliers) != 20 || s.Outliers[0] != -129 || s.Outliers[9] != -120 || s.Outliers[10] != 120 || s.Outliers[19] != 129 {
		t.Fatal(s)
	}
	a = &sqliteBoxAggregate{sum: sqliteRat(0)}
	for _, n := range []float64{1, 2, 3, 100} {
		a.Step([]driver.Value{n, "tukey"})
	}
	raw, _ = a.Value()
	if e = json.Unmarshal([]byte(raw.(string)), &payload); e != nil {
		t.Fatal(e)
	}
	s = payload.Distribution.Summary
	if s.Q1 != 1.75 || s.Median != 2.5 || s.Q3 != 27.25 || s.Low != 1 || s.High != 3 || s.OutlierCount != 1 || s.Outliers[0] != 100 {
		t.Fatal(s)
	}
}
func TestSQLiteHistogramEdgesPrecisionAndBounds(t *testing.T) {
	f := sqliteDistributionFunctions()[0]
	for _, c := range []struct {
		lo, hi float64
		bins   int64
		edges  []float64
		code   string
	}{{2, 6, 2, []float64{2, 4, 6}, ""}, {4, 4, 10, []float64{4, 4}, ""}, {1, math.Nextafter(1, 2), 10, []float64{}, "histogram_precision"}, {math.Nextafter(0, 1), 1, 2, []float64{}, "numeric_range"}, {9007199254740992, 9007199254740992, 2, []float64{}, "unsafe_integer"}} {
		raw, e := f.Call([]driver.Value{c.lo, c.hi, c.bins})
		if e != nil {
			t.Fatal(e)
		}
		var p sqliteHistogramEdges
		if e = json.Unmarshal([]byte(raw.(string)), &p); e != nil || p.Error != c.code || !reflect.DeepEqual(p.Edges, c.edges) {
			t.Fatal(p, e)
		}
	}
	a := &sqliteHistogramAggregate{}
	for _, v := range []float64{0, 1, 2, 3, 4} {
		if e := a.Step([]driver.Value{v, `{"edges":[0,1,2,3,4]}`}); e != nil {
			t.Fatal(e)
		}
	}
	if !reflect.DeepEqual(a.counts, []int64{1, 1, 1, 2}) {
		t.Fatal(a.counts)
	}
	if e := a.Step([]driver.Value{float64(1), `{"edges":[0,2]}`}); e == nil {
		t.Fatal("accepted changing shared edges")
	}
	for _, config := range []string{`{"edges":[0,0,1]}`, `{"edges":[2,1]}`, `{"edges":[1]}`, `{"edges":[0,9007199254740992]}`} {
		if e := (&sqliteHistogramAggregate{}).Step([]driver.Value{nil, config}); e == nil {
			t.Fatal("accepted invalid edges", config)
		}
	}
}
