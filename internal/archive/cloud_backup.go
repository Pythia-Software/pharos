package archive

import (
	"context"
	"database/sql"
	"math"
	"sort"
	"time"
)

type librarySizePoint struct {
	At    time.Time
	Bytes int64
}

type cloudGrowth struct {
	BytesPerDay float64 `json:"bytes_per_day"`
	HistoryDays float64 `json:"history_days"`
	Basis       string  `json:"basis"`
}

// Use the change in stored bytes over the last 90 days. Interpolate the
// boundary if observations straddle it; never extrapolate a short history.
// The fallback is a fixed amount (5% of today's size per 30 days), not compound
// growth. A shrinking library gets a flat forecast instead of negative sizes.
func cloudLibraryGrowth(points []librarySizePoint, now time.Time, current int64) cloudGrowth {
	result := cloudGrowth{BytesPerDay: float64(current) * .05 / 30, Basis: "fallback"}
	points = append([]librarySizePoint(nil), points...)
	points = append(points, librarySizePoint{now, current})
	sort.Slice(points, func(i, j int) bool { return points[i].At.Before(points[j].At) })
	var valid []librarySizePoint
	for _, point := range points {
		if point.Bytes > 0 && !point.At.After(now) {
			valid = append(valid, point)
		}
	}
	if len(valid) < 2 {
		return result
	}
	result.HistoryDays = math.Max(0, now.Sub(valid[0].At).Hours()/24)
	boundary := now.Add(-90 * 24 * time.Hour)
	for i := len(valid) - 2; i >= 0; i-- {
		left, right := valid[i], valid[i+1]
		if left.At.After(boundary) {
			continue
		}
		baseline := float64(left.Bytes)
		if right.At.After(left.At) {
			baseline += float64(right.Bytes-left.Bytes) * boundary.Sub(left.At).Seconds() / right.At.Sub(left.At).Seconds()
		}
		result.BytesPerDay = math.Max(0, (float64(current)-baseline)/90)
		result.HistoryDays = 90
		result.Basis = "measured_90_days"
		break
	}
	return result
}

// Drive checks already measure the data on disk. Retain one observation per
// UTC day, plus prior complete backup sizes where available. Keep a boundary
// anchor older than 90 days so the forecast can interpolate the rolling window.
func (c *Catalog) recordCloudLibrarySize(ctx context.Context, config Config, state *driveState) cloudGrowth {
	current := state.sizes.LibraryBytes
	fallback := cloudLibraryGrowth(nil, state.checked, current)
	if current <= 0 || ctx.Err() != nil {
		return fallback
	}
	err := c.writeTransaction(ctx, "library-size-history", func(tx *sql.Tx) error {
		for _, record := range loadBackupHistory(config).Backups {
			at, err := time.Parse(time.RFC3339Nano, record.FinishedAt)
			if err != nil || record.Partial || record.BytesTotal <= 0 || at.After(state.checked) {
				continue
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO library_size_history(day,measured_at,bytes) VALUES(?,?,?) ON CONFLICT(day) DO NOTHING", at.UTC().Format("2006-01-02"), at.UTC().Format(time.RFC3339Nano), record.BytesTotal); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO library_size_history(day,measured_at,bytes) VALUES(?,?,?) ON CONFLICT(day) DO UPDATE SET measured_at=excluded.measured_at, bytes=excluded.bytes", state.checked.UTC().Format("2006-01-02"), state.checked.UTC().Format(time.RFC3339Nano), current)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM library_size_history WHERE day < (SELECT max(day) FROM library_size_history WHERE day <= ?)", state.checked.Add(-90*24*time.Hour).UTC().Format("2006-01-02"))
		return err
	})
	if err != nil {
		return fallback
	}
	rows, err := c.reader().QueryContext(ctx, "SELECT measured_at,bytes FROM library_size_history ORDER BY day")
	if err != nil {
		return fallback
	}
	defer rows.Close()
	var points []librarySizePoint
	for rows.Next() {
		var day string
		var bytes int64
		if err := rows.Scan(&day, &bytes); err != nil {
			return fallback
		}
		at, err := time.Parse(time.RFC3339Nano, day)
		if err == nil {
			points = append(points, librarySizePoint{at, bytes})
		}
	}
	if rows.Err() != nil {
		return fallback
	}
	return cloudLibraryGrowth(points, state.checked, current)
}
