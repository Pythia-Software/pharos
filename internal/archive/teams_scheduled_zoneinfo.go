package archive

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"errors"
	"io"
	"sync"
	"time"
)

// teamsZoneinfo2026c is the unmodified Go 1.27.1 zoneinfo.zip, generated from
// IANA tzdata 2026c. LoadLocationFromTZData never consults host timezone data.
//
//go:embed zoneinfo_2026c.zip
var teamsZoneinfo2026c []byte

var teamsScheduledLocations sync.Map

func loadTeamsScheduledLocation(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, errTeamsQueryUnsupported
	}
	if v, ok := teamsScheduledLocations.Load(name); ok {
		return v.(*time.Location), nil
	}
	archive, err := zip.NewReader(bytes.NewReader(teamsZoneinfo2026c), int64(len(teamsZoneinfo2026c)))
	if err != nil {
		return nil, errTeamsQueryUnsupported
	}
	for _, file := range archive.File {
		if file.Name != name {
			continue
		}
		r, e := file.Open()
		if e != nil {
			return nil, errTeamsQueryUnsupported
		}
		data, e := io.ReadAll(io.LimitReader(r, 1<<20))
		closeErr := r.Close()
		if e != nil || closeErr != nil || len(data) == 0 || len(data) >= 1<<20 {
			return nil, errTeamsQueryUnsupported
		}
		loc, e := time.LoadLocationFromTZData(name, data)
		if e != nil {
			return nil, errTeamsQueryUnsupported
		}
		actual, _ := teamsScheduledLocations.LoadOrStore(name, loc)
		return actual.(*time.Location), nil
	}
	return nil, errors.New("unsupported scheduled timezone")
}
