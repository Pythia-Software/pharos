package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type cloudScheduleSpec struct {
	Cadence  int    `json:"cadence"`
	Time     string `json:"time"`
	Weekday  int    `json:"weekday"`
	Timezone string `json:"timezone"`
}

type cloudSchedule struct {
	cloudScheduleSpec
	DestinationID string `json:"destination_id"`
	Enabled       bool   `json:"enabled"`
	HostID        string `json:"host_id"`
	HostLabel     string `json:"host_label"`
	CatalogPath   string `json:"catalog_path"`
	Anchor        string `json:"anchor"`
	NextRunAt     string `json:"next_run_at,omitempty"`
	InFlight      string `json:"in_flight,omitempty"`
	Failures      int    `json:"failures"`
	LastError     string `json:"last_error,omitempty"`
	UpdatedAt     string `json:"updated_at"`
}

var errCloudNotDue = errors.New("this destination is not due for a scheduled backup")
var errCloudBusy = errors.New("a cloud operation is already running")

// Tests replace the upload at this boundary; scheduling itself never needs a
// cloud account, real library or clock sleeps to be exercised.
var cloudPerformUpload = func(s *Server, ctx context.Context, d cloudDestination, run cloudRun) {
	s.uploadCloudRun(ctx, d, run)
}

func (p cloudScheduleSpec) validate() error {
	if p.Cadence != 1 && p.Cadence != 7 && p.Cadence != 30 {
		return errors.New("choose daily, weekly or every 30 days")
	}
	if len(p.Time) != 5 || p.Time[2] != ':' {
		return errors.New("use a backup time in HH:MM format")
	}
	hour, err := strconv.Atoi(p.Time[:2])
	minute, e := strconv.Atoi(p.Time[3:])
	if err != nil || e != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 || fmt.Sprintf("%02d:%02d", hour, minute) != p.Time {
		return errors.New("choose a valid backup time")
	}
	if p.Weekday < 0 || p.Weekday > 6 {
		return errors.New("choose a valid day of the week")
	}
	if p.Timezone == "" || p.Timezone == "Local" {
		return errors.New("choose an explicit IANA time zone")
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return errors.New("choose a valid IANA time zone")
	}
	return nil
}

func cloudDefaultSchedule(p cloudConfig) cloudScheduleSpec {
	spec := cloudScheduleSpec{Cadence: p.Cadence, Time: p.Time, Weekday: p.Weekday, Timezone: p.Timezone}
	if spec.Cadence == 0 {
		spec.Cadence = 7
	}
	if spec.Time == "" {
		spec.Time = "02:00"
	}
	if spec.Timezone == "" {
		spec.Timezone = "UTC"
	}
	return spec
}

// Choose the first available wall-clock minute at/after the selected time.
// Spring-forward gaps run at the next available minute; repeated fall-back
// times run once. Dates are carried in UTC solely for calendar arithmetic.
func cloudWallTime(date time.Time, clock string, location *time.Location) time.Time {
	hour, _ := strconv.Atoi(clock[:2])
	minute, _ := strconv.Atoi(clock[3:])
	y, m, d := date.Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, location)
	end := time.Date(y, m, d+1, 0, 0, 0, 0, location)
	for t := start; t.Before(end); t = t.Add(time.Minute) {
		local := t.In(location)
		ly, lm, ld := local.Date()
		if ly == y && lm == m && ld == d && local.Hour()*60+local.Minute() >= hour*60+minute {
			return t
		}
	}
	return time.Time{}
}

func nextCloudTime(s cloudSchedule, after time.Time) (time.Time, error) {
	if err := s.cloudScheduleSpec.validate(); err != nil {
		return time.Time{}, err
	}
	location, _ := time.LoadLocation(s.Timezone)
	local := after.In(location)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	if s.Cadence == 30 {
		anchor, err := time.Parse("2006-01-02", s.Anchor)
		if err != nil {
			return time.Time{}, errors.New("backup schedule is missing its calendar anchor")
		}
		period := max(1, int(date.Sub(anchor).Hours()/24)/30)
		for i := period; i <= period+2; i++ {
			t := cloudWallTime(anchor.AddDate(0, 0, 30*i), s.Time, location)
			if t.After(after) {
				return t.UTC(), nil
			}
		}
	} else {
		for day := 0; day <= 8; day++ {
			candidate := date.AddDate(0, 0, day)
			if s.Cadence == 7 && int(candidate.Weekday()) != s.Weekday {
				continue
			}
			t := cloudWallTime(candidate, s.Time, location)
			if t.After(after) {
				return t.UTC(), nil
			}
		}
	}
	return time.Time{}, errors.New("could not determine the next backup date")
}

func cloudScheduleTx(ctx context.Context, tx *sql.Tx, id string) (cloudSchedule, error) {
	var s cloudSchedule
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT state_json FROM cloud_backup_schedules WHERE destination_id=?", id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &s)
	}
	return s, err
}

func saveCloudScheduleTx(ctx context.Context, tx *sql.Tx, s cloudSchedule, at time.Time) error {
	s.UpdatedAt = at.UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO cloud_backup_schedules(destination_id,state_json) VALUES(?,?) ON CONFLICT(destination_id) DO UPDATE SET state_json=excluded.state_json", s.DestinationID, string(raw))
	return err
}

func (c *Catalog) cloudSchedules(ctx context.Context) ([]cloudSchedule, error) {
	rows, err := c.reader().QueryContext(ctx, "SELECT state_json FROM cloud_backup_schedules ORDER BY destination_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []cloudSchedule{}
	for rows.Next() {
		var raw string
		var s cloudSchedule
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

func (c *Catalog) setCloudSchedule(ctx context.Context, id string, enabled bool, spec cloudScheduleSpec, at time.Time) error {
	if enabled {
		if err := spec.validate(); err != nil {
			return err
		}
		verified, err := c.cloudHasVerified(ctx, id, "full")
		if err != nil {
			return err
		}
		if !verified {
			return errors.New("validate a downloaded full backup before enabling scheduled backups")
		}
	}
	host := currentHost()
	if enabled && host.Fallback {
		return errHostUnknown
	}
	return c.writeTransaction(ctx, "cloud-schedule", func(tx *sql.Tx) error {
		var connected bool
		if err := tx.QueryRowContext(ctx, "SELECT credential_json<>'' FROM cloud_destinations WHERE id=?", id).Scan(&connected); err != nil {
			return err
		}
		if enabled && !connected {
			return errors.New("connect the destination before enabling its schedule")
		}
		s, err := cloudScheduleTx(ctx, tx, id)
		if errors.Is(err, sql.ErrNoRows) {
			s.DestinationID = id
		} else if err != nil {
			return err
		}
		if enabled && s.InFlight != "" {
			return errors.New("finish or cancel the current backup before changing its schedule")
		}
		if enabled && (!s.Enabled || s.cloudScheduleSpec != spec || s.HostID != host.ID || s.CatalogPath != canonical(c.Path)) {
			s.cloudScheduleSpec, s.HostID, s.HostLabel = spec, host.ID, host.Label
			s.CatalogPath = canonical(c.Path)
			location, _ := time.LoadLocation(spec.Timezone)
			s.Anchor = at.In(location).Format("2006-01-02")
			next, err := nextCloudTime(s, at)
			if err != nil {
				return err
			}
			s.NextRunAt, s.Failures, s.LastError = next.Format(time.RFC3339Nano), 0, ""
		}
		s.Enabled = enabled
		return saveCloudScheduleTx(ctx, tx, s, at)
	})
}

// Insert the run and claim its destination together. A saved in-flight ID
// prevents another service instance from starting the same destination.
func (c *Catalog) claimCloudRun(ctx context.Context, d cloudDestination, run *cloudRun, at time.Time) error {
	return c.writeTransaction(ctx, "cloud-start", func(tx *sql.Tx) error {
		var connected bool
		if err := tx.QueryRowContext(ctx, "SELECT credential_json<>'' FROM cloud_destinations WHERE id=?", d.ID).Scan(&connected); err != nil {
			return err
		}
		if !connected {
			return errors.New("destination is disconnected")
		}
		if run.Kind == "full" {
			s, err := cloudScheduleTx(ctx, tx, d.ID)
			if errors.Is(err, sql.ErrNoRows) {
				s = cloudSchedule{DestinationID: d.ID, cloudScheduleSpec: cloudDefaultSchedule(d.plan)}
			} else if err != nil {
				return err
			}
			if s.InFlight != "" {
				return errCloudBusy
			}
			if run.Trigger == "scheduled" {
				due, err := time.Parse(time.RFC3339Nano, s.NextRunAt)
				if !s.Enabled || s.HostID != currentHost().ID || s.CatalogPath != canonical(c.Path) || err != nil || due.After(at) {
					return errCloudNotDue
				}
				run.ScheduledFor = s.NextRunAt
			}
			s.InFlight = run.ID
			if err := saveCloudScheduleTx(ctx, tx, s, at); err != nil {
				return err
			}
		}
		run.UpdatedAt = at.UTC().Format(time.RFC3339Nano)
		raw, err := json.Marshal(run)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO cloud_backup_runs(id,destination_id,status_json,created_at) VALUES(?,?,?,?)", run.ID, run.DestinationID, string(raw), run.StartedAt)
		return err
	})
}

// Settling is idempotent, and also runs at startup after interrupted jobs have
// been reconciled. Success coalesces missed slots; explicit cancellation waits
// for the next normal slot. Scheduled failures retry after 1h, 6h and 24h.
func (c *Catalog) settleCloudSchedule(ctx context.Context, run cloudRun, at time.Time) error {
	if run.Kind != "full" || run.State == "running" {
		return nil
	}
	return c.writeTransaction(ctx, "cloud-schedule-result", func(tx *sql.Tx) error {
		s, err := cloudScheduleTx(ctx, tx, run.DestinationID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if s.InFlight != run.ID {
			return nil
		}
		s.InFlight = ""
		if run.State == "complete" {
			s.Failures, s.LastError = 0, ""
		} else {
			s.LastError = run.Error
		}
		if s.Enabled {
			next, err := nextCloudTime(s, at)
			if err != nil {
				s.Enabled, s.LastError = false, err.Error()
			} else {
				if run.State == "failed" && run.Trigger == "scheduled" {
					s.Failures++
					delays := []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour}
					if s.Failures <= len(delays) {
						next = at.Add(delays[s.Failures-1])
					} else {
						s.Failures = 0
					}
				} else {
					s.Failures = 0
				}
				s.NextRunAt = next.UTC().Format(time.RFC3339Nano)
			}
		}
		return saveCloudScheduleTx(ctx, tx, s, at)
	})
}

func (s *Server) startCloudRun(ctx context.Context, id, kind, trigger string, at time.Time) (cloudRun, error) {
	var run cloudRun
	d, err := s.Catalog.cloudDestination(ctx, id, true)
	if err != nil || len(d.credential) == 0 {
		return run, errors.New("connect this destination before uploading")
	}
	started := false
	defer func() {
		if !started {
			clear(d.credential)
		}
	}()
	if err = d.plan.validate(); err != nil {
		return run, err
	}
	if kind == "full" {
		verified, err := s.Catalog.cloudHasVerified(ctx, id, "sample")
		if err != nil {
			return run, err
		}
		if !verified {
			return run, errors.New("verify a downloaded small test before starting a full backup")
		}
		if trigger == "scheduled" {
			verified, err = s.Catalog.cloudHasVerified(ctx, id, "full")
			if err != nil {
				return run, err
			}
			if !verified {
				return run, errors.New("scheduled backups require a verified full restore")
			}
		}
	}
	run = cloudRun{OwnerPID: os.Getpid(), OwnerHost: currentHost().ID, ID: randomToken(), DestinationID: id, Kind: kind, Trigger: trigger, State: "running", Phase: "Preparing archive", Object: d.plan.Object, StartedAt: at.UTC().Format(time.RFC3339Nano)}
	if kind == "sample" {
		run.Object += ".roundtrip-test"
	}
	work, cancel := context.WithCancel(s.life.ctx)
	if err = s.cloud.begin(run.ID, "upload", kind, cancel); err != nil {
		cancel()
		return run, err
	}
	if err = s.Catalog.claimCloudRun(ctx, d, &run, at); err != nil {
		s.cloud.end()
		cancel()
		return run, err
	}
	perform := cloudPerformUpload
	spec := OperationSpec{Kind: "cloud.upload", Variant: kind, Subject: id, Trigger: triggerUser, Priority: priorityForeground, Legacy: run.ID}
	if trigger == "scheduled" {
		spec.Trigger, spec.Priority = triggerSchedule, priorityBackground
	}
	_, upload := s.begin(s.life.ctx, spec)
	s.cloud.attach(upload)
	if !s.launch(upload, func(context.Context, *Op) error {
		defer cancel()
		defer s.cloud.end()
		defer clear(d.credential)
		perform(s, work, d, run)
		return s.cloudRunOutcome(work, run.ID)
	}) {
		s.cloud.end()
		cancel()
		run.State, run.Error = "failed", "Pharos is stopping"
		_ = s.Catalog.saveCloudRun(ctx, &run, nil)
		_ = s.Catalog.settleCloudSchedule(ctx, run, at)
		return run, errors.New(run.Error)
	}
	started = true
	return run, nil
}

func (s *Server) cloudScheduleTick(ctx context.Context, at time.Time) error {
	if err := s.reconcileCloudRuns(ctx); err != nil {
		return err
	}
	schedules, err := s.Catalog.cloudSchedules(ctx)
	if err != nil {
		return err
	}
	for _, schedule := range schedules {
		if schedule.InFlight != "" {
			run, err := s.Catalog.cloudRun(ctx, schedule.InFlight)
			if err != nil {
				return err
			}
			if err := s.Catalog.settleCloudSchedule(ctx, run, at); err != nil {
				return err
			}
		}
	}
	schedules, err = s.Catalog.cloudSchedules(ctx)
	if err != nil {
		return err
	}
	// Earliest due destination first; do not let one retry starve another.
	sort.Slice(schedules, func(i, j int) bool { return schedules[i].NextRunAt < schedules[j].NextRunAt })
	for _, schedule := range schedules {
		if !schedule.Enabled || schedule.HostID != currentHost().ID || schedule.CatalogPath != canonical(s.Catalog.Path) || schedule.InFlight != "" {
			continue
		}
		due, err := time.Parse(time.RFC3339Nano, schedule.NextRunAt)
		if err != nil || due.After(at) {
			continue
		}
		_, err = s.startCloudRun(ctx, schedule.DestinationID, "full", "scheduled", at)
		if errors.Is(err, errCloudBusy) {
			return nil
		}
		if err != nil && !errors.Is(err, errCloudNotDue) {
			if e := s.Catalog.deferCloudSchedule(ctx, schedule, at, err); e != nil {
				return e
			}
		}
	}
	return nil
}

// Failures before admission must be visible and backed off too; they must not
// starve the other destinations waiting to run.
func (c *Catalog) deferCloudSchedule(ctx context.Context, observed cloudSchedule, at time.Time, problem error) error {
	return c.writeTransaction(ctx, "cloud-schedule-defer", func(tx *sql.Tx) error {
		s, err := cloudScheduleTx(ctx, tx, observed.DestinationID)
		if err != nil {
			return err
		}
		if !s.Enabled || s.InFlight != "" || s.UpdatedAt != observed.UpdatedAt {
			return nil
		}
		s.LastError = "Scheduled backup could not start: " + problem.Error()
		next, err := nextCloudTime(s, at)
		if err != nil {
			s.Enabled = false
			s.LastError = err.Error()
		} else {
			s.Failures++
			delays := []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour}
			if s.Failures <= len(delays) {
				next = at.Add(delays[s.Failures-1])
			} else {
				s.Failures = 0
			}
			s.NextRunAt = next.UTC().Format(time.RFC3339Nano)
		}
		return saveCloudScheduleTx(ctx, tx, s, at)
	})
}

func (s *Server) cloudScheduleLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := s.cloudScheduleTick(ctx, time.Now()); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "Pharos cloud scheduler:", strings.TrimSpace(err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// cloudRunOutcome is a finished upload's result for its operation, from the
// state it saved.
func (s *Server) cloudRunOutcome(ctx context.Context, id string) error {
	run, err := s.Catalog.cloudRun(context.Background(), id)
	if err != nil {
		return err
	}
	switch run.State {
	case "complete":
		return nil
	case "cancelled":
		return defaultError(ctx.Err(), errors.New("cloud upload cancelled"))
	}
	return errors.New(defaultString(run.Error, "cloud upload "+run.State))
}
