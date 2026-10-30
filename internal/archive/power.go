package archive

import (
	"context"
	"regexp"
	"strconv"
	"time"
)

// powerState is this Mac's power source, for the background schedule's
// battery policy (docs/background-work.md §11). A Mac whose source cannot be
// read counts as plugged in.
type powerState struct {
	// Battery is whether this Mac has one; OnBattery whether it is running
	// on it now.
	Battery   bool `json:"battery"`
	OnBattery bool `json:"on_battery"`
	// Percent is the charge, -1 when unknown.
	Percent  int  `json:"percent"`
	LowPower bool `json:"low_power_mode"`
}

var (
	drawingBattery = regexp.MustCompile(`Now drawing from 'Battery Power'`)
	batteryLine    = regexp.MustCompile(`(?m)^\s*-InternalBattery[^\t]*\t(\d+)%`)
	lowPowerLine   = regexp.MustCompile(`(?m)^\s*lowpowermode\s+1\b`)
)

// parsePower reads `pmset -g batt` and `pmset -g`.
func parsePower(batt, settings string) powerState {
	state := powerState{Percent: -1, OnBattery: drawingBattery.MatchString(batt), LowPower: lowPowerLine.MatchString(settings)}
	if match := batteryLine.FindStringSubmatch(batt); match != nil {
		state.Battery = true
		state.Percent, _ = strconv.Atoi(match[1])
	}
	state.OnBattery = state.OnBattery && state.Battery
	return state
}

// readPower asks pmset, through driveTool so tests never run it.
func readPower(ctx context.Context) powerState {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	batt, err := driveTool(ctx, "pmset", "-g", "batt")
	if err != nil {
		return powerState{Percent: -1}
	}
	settings, _ := driveTool(ctx, "pmset", "-g")
	return parsePower(string(batt), string(settings))
}

// powerMaxAge is how long a reading is reused: the loop and the status
// snapshot ask often, and a power source changes rarely.
var powerMaxAge = time.Minute

func (s *Server) cachedPower(ctx context.Context) powerState {
	s.background.mu.Lock()
	defer s.background.mu.Unlock()
	if s.background.powerAt.IsZero() || time.Since(s.background.powerAt) > powerMaxAge {
		s.background.power, s.background.powerAt = readPower(ctx), time.Now()
	}
	return s.background.power
}

// The battery policy's choices per cadence: run as plugged in, run less
// often (Frequently only), or wait until plugged in.
const (
	batterySame   = "same"
	batterySlower = "slower"
	batteryWait   = "wait"
)

// batterySlowerInterval is Frequently's interval on battery when slower.
var batterySlowerInterval = 15 * time.Minute

// lowBatteryPercent is the charge below which, with LowWaits, everything
// waits until plugged in.
const lowBatteryPercent = 20

// batteryPolicy is what each cadence does on battery.
type batteryPolicy struct {
	Frequent string `json:"frequent"`
	Hourly   string `json:"hourly"`
	Daily    string `json:"daily"`
	// LowWaits makes everything wait below lowBatteryPercent or in Low
	// Power Mode.
	LowWaits bool `json:"low_battery_waits"`
}

func defaultBatteryPolicy() batteryPolicy {
	return batteryPolicy{Frequent: batterySlower, Hourly: batteryWait, Daily: batteryWait, LowWaits: true}
}

// batteryChoices are the choices each cadence offers on battery.
var batteryChoices = map[string][]string{cadenceFrequent: {batterySame, batterySlower, batteryWait}, cadenceHourly: {batterySame, batteryWait}, cadenceDaily: {batterySame, batteryWait}}

func (policy batteryPolicy) of(cadence string) string {
	return map[string]string{cadenceFrequent: policy.Frequent, cadenceHourly: policy.Hourly, cadenceDaily: policy.Daily}[cadence]
}

// onPower is what a cadence does with this power: batterySame when plugged
// in (or with no battery).
func (policy batteryPolicy) onPower(cadence string, power powerState) string {
	switch {
	case !power.OnBattery:
		return batterySame
	case policy.LowWaits && (power.LowPower || power.Percent >= 0 && power.Percent < lowBatteryPercent):
		return batteryWait
	}
	return defaultString(policy.of(cadence), batterySame)
}
