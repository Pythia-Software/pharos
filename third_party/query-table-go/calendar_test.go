package querytable

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestCalendarFutureYearBoundaries(t *testing.T) {
	for _, zone := range []string{"America/New_York", "Australia/Adelaide"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		for _, year := range []int{2044, 2045, 2100, 2400} {
			for _, interval := range []string{"day", "week", "month", "year"} {
				for _, instant := range []time.Time{time.Date(year, 12, 31, 23, 45, 0, 0, time.UTC), time.Date(year+1, 1, 1, 0, 15, 0, 0, time.UTC)} {
					t.Run(zone+"/"+instant.Format(time.RFC3339)+"/"+interval, func(t *testing.T) {
						local := instant.In(loc)
						want := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
						switch interval {
						case "week":
							want = want.AddDate(0, 0, -((int(local.Weekday()) + 6) % 7))
						case "month":
							want = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
						case "year":
							want = time.Date(local.Year(), 1, 1, 0, 0, 0, 0, loc)
						}
						got, err := calendarFloor(instant, interval, loc)
						if err != nil || !got.Equal(want) {
							t.Fatalf("floor=%s err=%v, want %s", got, err, want.UTC())
						}
						next, err := calendarNext(got, interval, loc)
						if err != nil || !next.After(got) {
							t.Fatalf("next=%s err=%v, floor=%s", next, err, got)
						}
					})
				}
			}
		}
	}
}

func TestCalendarFutureHalfHourZone(t *testing.T) {
	loc, err := time.LoadLocation("Australia/Adelaide")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2044, 12, 31, 23, 30, 0, 0, time.UTC)
	for _, instant := range []time.Time{want.Add(15 * time.Minute), want.Add(45 * time.Minute)} {
		got, err := calendarFloor(instant, "hour", loc)
		if err != nil || !got.Equal(want) {
			t.Fatalf("floor(%s)=%s err=%v, want %s", instant, got, err, want)
		}
	}
	next, err := calendarNext(want, "hour", loc)
	if err != nil || !next.Equal(want.Add(time.Hour)) {
		t.Fatalf("next=%s err=%v, want %s", next, err, want.Add(time.Hour))
	}
}

func TestCalendarEmptySeriesDoesNotWalkRange(t *testing.T) {
	for _, buckets := range [][]SQLiteMetricV2Bucket{nil, {{Keys: []any{nil, "a"}, Count: 1, Value: float64(1)}}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result := SQLiteMetricV2{Buckets: buckets}
		config := MetricDateGrouping{Field: "at", Interval: "hour", Range: "custom", IncludeEmpty: true, Start: "1000-01-01", End: "9999-01-01"}
		spec := AggSpec{ID: "empty", Op: "count", GroupBy: []string{"at", "k"}}
		err := fillCalendarBuckets(ctx, &result, config, spec, MetricQuery{}, time.Now(), time.UTC, 2000)
		cancel()
		if err != nil || !reflect.DeepEqual(result.Buckets, buckets) {
			t.Fatalf("empty series: buckets=%#v err=%v", result.Buckets, err)
		}
		config.End = "0900-01-01"
		if err = fillCalendarBuckets(context.Background(), &result, config, spec, MetricQuery{}, time.Now(), time.UTC, 2000); err == nil {
			t.Fatal("empty series bypassed range validation")
		}
	}
}
