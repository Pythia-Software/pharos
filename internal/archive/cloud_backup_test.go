package archive

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestCloudLibraryGrowth(t *testing.T) {
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	point := func(days int, bytes int64) librarySizePoint { return librarySizePoint{now.AddDate(0, 0, -days), bytes} }
	for _, test := range []struct {
		name   string
		points []librarySizePoint
		want   float64
		basis  string
	}{
		{"no history", nil, 1000 * .05 / 30, "fallback"},
		{"short history", []librarySizePoint{point(89, 100)}, 1000 * .05 / 30, "fallback"},
		{"90 days", []librarySizePoint{point(90, 100)}, 10, "measured_90_days"},
		{"interpolated boundary", []librarySizePoint{point(100, 0), point(95, 50), point(80, 200)}, 10, "measured_90_days"},
		{"shrinking", []librarySizePoint{point(90, 2000)}, 0, "measured_90_days"},
		{"ignore future", []librarySizePoint{point(-1, 100000), point(90, 100)}, 10, "measured_90_days"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := cloudLibraryGrowth(test.points, now, 1000)
			if math.Abs(got.BytesPerDay-test.want) > 1e-8 || got.Basis != test.basis {
				t.Fatalf("growth = %+v, want %g %s", got, test.want, test.basis)
			}
		})
	}
}

func TestCloudLibrarySizeHistory(t *testing.T) {
	catalog, config := testCatalog(t)
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	for day := -100; day <= 0; day++ {
		state := &driveState{checked: now.AddDate(0, 0, day), sizes: librarySizes{LibraryBytes: int64(2000 + day*10)}}
		got := catalog.recordCloudLibrarySize(context.Background(), config, state)
		if day == 0 && (got.Basis != "measured_90_days" || got.BytesPerDay != 10) {
			t.Fatalf("growth: %+v", got)
		}
	}
	state := &driveState{checked: now, sizes: librarySizes{LibraryBytes: 2100}}
	catalog.recordCloudLibrarySize(context.Background(), config, state)
	var count int
	var bytes int64
	if err := catalog.DB.QueryRow("SELECT count(*), max(bytes) FROM library_size_history").Scan(&count, &bytes); err != nil {
		t.Fatal(err)
	}
	if count != 91 || bytes != 2100 {
		t.Fatalf("history count %d, latest %d", count, bytes)
	}
}
