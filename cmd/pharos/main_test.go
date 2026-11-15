package main

import (
	"testing"
	"time"
)

// This test also runs in a scratch container: the production main.go must
// supply the timezone data when neither system nor GOROOT files exist.
func TestNamedTimezones(t *testing.T) {
	winter := time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)
	summer := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name         string
		winterOffset int
		summerOffset int
		summerDST    bool
	}{
		{"America/Denver", -7 * 60 * 60, -6 * 60 * 60, true},
		{"Asia/Kathmandu", 5*60*60 + 45*60, 5*60*60 + 45*60, false},
		{"Etc/UTC", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			location, err := time.LoadLocation(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			for _, season := range []struct {
				name   string
				at     time.Time
				offset int
				dst    bool
			}{
				{"winter", winter, tc.winterOffset, false},
				{"summer", summer, tc.summerOffset, tc.summerDST},
			} {
				t.Run(season.name, func(t *testing.T) {
					local := season.at.In(location)
					if _, offset := local.Zone(); offset != season.offset {
						t.Errorf("UTC offset = %d seconds, want %d", offset, season.offset)
					}
					if local.IsDST() != season.dst {
						t.Errorf("IsDST() = %t, want %t", local.IsDST(), season.dst)
					}
				})
			}
		})
	}
	t.Run("invalid", func(t *testing.T) {
		if _, err := time.LoadLocation("Invalid/Pharos"); err == nil {
			t.Fatal("accepted an invalid IANA timezone name")
		}
	})
}
