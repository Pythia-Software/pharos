package archive

import "time"

// Calendar interpretation mirrors the server schedule contract.
type teamsScheduledCalendar struct {
	Cadence  string
	Timezone string
	AnchorAt time.Time
}

type teamsScheduledCivil struct {
	year                            int
	month                           time.Month
	day, hour, minute, second, nano int
}

func teamsScheduledCivilOf(t time.Time) teamsScheduledCivil {
	y, m, d := t.Date()
	h, n, s := t.Clock()
	return teamsScheduledCivil{y, m, d, h, n, s, t.Nanosecond()}
}
func (c teamsScheduledCivil) naive() time.Time {
	return time.Date(c.year, c.month, c.day, c.hour, c.minute, c.second, c.nano, time.UTC)
}
func teamsScheduledDaysIn(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// teamsResolveScheduledLocal selects the earlier instant in a fold. A nonexistent civil
// timestamp advances to the first valid local second after the gap.
func teamsResolveScheduledLocal(c teamsScheduledCivil, loc *time.Location) (time.Time, error) {
	naive := c.naive()
	offsets := map[int]bool{}
	for h := -48; h <= 48; h++ {
		_, offset := naive.Add(time.Duration(h) * time.Hour).In(loc).Zone()
		offsets[offset] = true
	}
	resolve := func(x teamsScheduledCivil) (time.Time, bool) {
		base := x.naive()
		var best time.Time
		for offset := range offsets {
			candidate := base.Add(-time.Duration(offset) * time.Second)
			if teamsScheduledCivilOf(candidate.In(loc)) == x && (best.IsZero() || candidate.Before(best)) {
				best = candidate
			}
		}
		return best, !best.IsZero()
	}
	if t, ok := resolve(c); ok {
		return t.UTC(), nil
	}
	// Advance to the next whole second: fractional instants inside a gap have
	// no valid counterpart, while transition boundaries have whole-second precision.
	start := naive.Truncate(time.Second).Add(time.Second)
	for step := 0; step < 26*60*60; step++ {
		probe := start.Add(time.Duration(step) * time.Second)
		if t, ok := resolve(teamsScheduledCivilOf(probe)); ok {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errTeamsQueryUnsupported
}

// Boundary returns the nth civil boundary from the original anchor. Monthly
// clamping is recomputed from the original day, so Jan 31 -> Feb 28 -> Mar 31.
func (c teamsScheduledCalendar) Boundary(index int) (time.Time, error) {
	if index < 0 || index > 10000 || c.AnchorAt.IsZero() {
		return time.Time{}, errTeamsQueryUnsupported
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return time.Time{}, errTeamsQueryUnsupported
	}
	anchor := teamsScheduledCivilOf(c.AnchorAt.In(loc))
	target := anchor
	switch c.Cadence {
	case "daily", "weekly":
		days := index
		if c.Cadence == "weekly" {
			days *= 7
		}
		t := anchor.naive().AddDate(0, 0, days)
		target = teamsScheduledCivilOf(t)
	case "monthly":
		total := anchor.year*12 + int(anchor.month) - 1 + index
		y, m := total/12, time.Month(total%12+1)
		d := anchor.day
		if max := teamsScheduledDaysIn(y, m); d > max {
			d = max
		}
		target.year = y
		target.month = m
		target.day = d
	default:
		return time.Time{}, errTeamsQueryUnsupported
	}
	return teamsResolveScheduledLocal(target, loc)
}

func (c teamsScheduledCalendar) Window(index int) (time.Time, time.Time, error) {
	start, err := c.Boundary(index)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err := c.Boundary(index + 1)
	if err != nil || !end.After(start) {
		return time.Time{}, time.Time{}, errTeamsQueryUnsupported
	}
	return start, end, nil
}
