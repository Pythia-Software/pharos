package archive

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type cloudJobs struct {
	mu     sync.Mutex
	active string
	cancel context.CancelFunc
	// current is what libraryActivities lists. It has its own lock because
	// reconcileCloudRuns holds mu across catalog writes, which status
	// requests must not wait for.
	currentMu sync.Mutex
	current   *cloudJob
}

// cloudJob is the cloud operation in progress: a destination connection test
// ("connect"), an upload ("upload") or a restore verification ("verify").
type cloudJob struct {
	id, operation, kind, startedAt string
	phase                          string
	done, total                    int64
}

func (j *cloudJobs) begin(id, operation, kind string, cancel context.CancelFunc) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.active != "" {
		return errCloudBusy
	}
	j.active = id
	j.cancel = cancel
	j.currentMu.Lock()
	j.current = &cloudJob{id: id, operation: operation, kind: kind, startedAt: now()}
	j.currentMu.Unlock()
	return nil
}
func (j *cloudJobs) end() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.active = ""
	j.cancel = nil
	j.currentMu.Lock()
	j.current = nil
	j.currentMu.Unlock()
}

// report records the running job's phase and bytes, for status only.
func (j *cloudJobs) report(id, phase string, done, total int64) {
	j.currentMu.Lock()
	defer j.currentMu.Unlock()
	if j.current != nil && j.current.id == id {
		j.current.phase, j.current.done, j.current.total = phase, done, total
	}
}

func (j *cloudJobs) job() *cloudJob {
	j.currentMu.Lock()
	defer j.currentMu.Unlock()
	if j.current == nil {
		return nil
	}
	copy := *j.current
	return &copy
}

// cloudActivity lists the cloud job as work that holds the drive: each one
// stages files under staging_root, which is on the library's drive, so an
// eject stops it. Its kind is backup, which every client already renders.
func cloudActivity(job *cloudJob) libraryActivity {
	activity := libraryActivity{ID: "cloud:" + job.id, Kind: "backup", StartedAt: job.startedAt, Writes: true}
	switch job.operation {
	case "connect":
		activity.Label = "Testing a cloud backup destination"
		activity.Detail = "Uploading a small test file"
		activity.OnEject = "Stops the test; connect the destination again afterwards."
	case "verify":
		activity.Label = "Verifying a cloud backup"
		activity.Detail = "Checking the downloaded archive and recovering it to staging"
		activity.OnEject = "Stops the verification and removes the partly recovered copy; run the verification again from Settings → Backups."
	default:
		activity.Label = "Uploading a cloud backup"
		if job.kind == "sample" {
			activity.Label = "Uploading a cloud test backup"
		}
		phase := defaultString(job.phase, "Preparing archive")
		activity.Detail = phase
		work := &activityWork{Phase: phase}
		if phase == "Uploading archive" {
			work.Unit, work.Done, work.Total = "byte", float64(job.done), float64(job.total)
			activity.Detail = fmt.Sprintf("%s · %s of %s", phase, humanBytes(job.done), humanBytes(job.total))
		}
		activity.Work = work
		activity.OnEject = "Stops the upload and deletes its staged archive; the last completed cloud backup is unchanged. Start it again from Settings → Backups, or a schedule runs it at its next time."
	}
	return activity
}
func (j *cloudJobs) stop(id string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.active != id || j.cancel == nil {
		return false
	}
	j.cancel()
	return true
}

// Reconcile without requiring an open settings page. Holding cloud.mu while
// deciding which runs were interrupted keeps a just-admitted job from being
// mistaken for one by a stale poll. It is released before storing them, which
// can wait for a sync holding the writer, so the Cloud page does not wait too;
// the update applies only to a run unchanged since it was read.
func (s *Server) reconcileCloudRuns(ctx context.Context) error {
	interrupted, err := s.interruptedCloudRuns(ctx)
	if err != nil {
		return err
	}
	for _, change := range interrupted {
		run := change.run
		data, _ := json.Marshal(run)
		var updated int64
		err = s.Catalog.writeTransaction(ctx, "cloud-interrupted", func(tx *sql.Tx) error {
			result, e := tx.ExecContext(ctx, "UPDATE cloud_backup_runs SET status_json=? WHERE id=? AND json_extract(status_json,'$.updated_at')=?", string(data), run.ID, change.previousUpdated)
			if e != nil {
				return e
			}
			updated, e = result.RowsAffected()
			return e
		})
		if err != nil {
			return err
		}
		if updated > 0 && run.State == "failed" {
			if err := cleanInterruptedCloudStaging(s.Config(), run.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

type interruptedCloudRun struct {
	run             cloudRun
	previousUpdated string
}

// interruptedCloudRuns reads the running runs and returns those this Mac was
// running and has lost, marked failed.
func (s *Server) interruptedCloudRuns(ctx context.Context) ([]interruptedCloudRun, error) {
	s.cloud.mu.Lock()
	defer s.cloud.mu.Unlock()
	rows, err := s.Catalog.reader().QueryContext(ctx, "SELECT status_json FROM cloud_backup_runs WHERE json_extract(status_json,'$.state')='running' OR json_extract(status_json,'$.verification_state')='running'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	interrupted := []interruptedCloudRun{}
	for rows.Next() {
		var raw string
		var run cloudRun
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &run); err != nil {
			return nil, err
		}
		previousUpdated := run.UpdatedAt
		if s.interruptCloudRun(&run) {
			run.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			interrupted = append(interrupted, interruptedCloudRun{run, previousUpdated})
		}
	}
	return interrupted, rows.Err()
}

// interruptCloudRun marks run failed where this Mac was running it and its
// process is gone, or is this one without the job, and reports whether it
// changed. Callers hold cloud.mu.
func (s *Server) interruptCloudRun(run *cloudRun) bool {
	host := currentHost().ID
	changed := false
	if run.State == "running" && run.OwnerHost == host && run.OwnerPID > 0 && (errors.Is(syscall.Kill(run.OwnerPID, 0), syscall.ESRCH) || (run.OwnerPID == os.Getpid() && s.cloud.active != run.ID)) {
		run.State, run.Phase, run.Error = "failed", "interrupted", "Upload was interrupted before completion could be confirmed. No restore was verified."
		changed = true
	}
	if run.VerificationState == "running" && run.VerificationHost == host && run.VerificationPID > 0 && (errors.Is(syscall.Kill(run.VerificationPID, 0), syscall.ESRCH) || (run.VerificationPID == os.Getpid() && s.cloud.active != run.ID)) {
		run.VerificationState, run.VerificationError, run.Verified = "failed", "Verification was interrupted. Run verification again.", false
		changed = true
	}
	return changed
}

// getCloud shows interrupted runs as failed and leaves storing that to
// reconcileCloudRuns in the background, so the page never waits for a sync
// holding the writer.
func (s *Server) getCloud(w http.ResponseWriter, r *http.Request) {
	destinations, err := s.Catalog.cloudDestinations(r.Context())
	if err != nil {
		writeError(w, err, 500)
		return
	}
	schedules, err := s.Catalog.cloudSchedules(r.Context())
	if err != nil {
		writeError(w, err, 500)
		return
	}
	s.cloud.mu.Lock()
	runs, err := s.Catalog.cloudRuns(r.Context())
	interrupted := false
	for i := range runs {
		interrupted = s.interruptCloudRun(&runs[i]) || interrupted
	}
	active := s.cloud.active
	s.cloud.mu.Unlock()
	if err != nil {
		writeError(w, err, 500)
		return
	}
	if interrupted {
		s.spawn(func(ctx context.Context) {
			if err := s.reconcileCloudRuns(ctx); err != nil && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "Pharos cloud runs:", err)
			}
		})
	}
	writeJSON(w, map[string]any{"current_host_id": currentHost().ID, "schedule_catalog": canonical(s.Catalog.Path), "library_catalog": s.Config().CatalogPath, "library_config": s.Config().Path, "active_run_id": active, "destinations": destinations, "runs": runs, "schedules": schedules, "setup_root": filepath.Join(userHome(), "Library", "Application Support", "Pharos", "cloud-setup"), "executable": executablePath()}, 200)
}
func userHome() string { home, _ := os.UserHomeDir(); return home }
func (s *Server) postCloud(w http.ResponseWriter, r *http.Request, body map[string]any) {
	switch r.URL.Path {
	case "/api/cloud-backup/import":
		dir, _ := body["setup_directory"].(string)
		raw, err := readCloudSetup(dir, "destination-draft.json")
		if err != nil {
			writeError(w, err, 400)
			return
		}
		var p, expected cloudConfig
		input, _ := json.Marshal(body["expected"])
		if json.Unmarshal(raw, &p) != nil || json.Unmarshal(input, &expected) != nil || !cloudEqual(p, expected) {
			writeError(w, errors.New("setup files do not match these wizard settings; return to Commands and use the matching destination"), 400)
			return
		}
		if err = p.validate(); err != nil {
			writeError(w, err, 400)
			return
		}
		credential, err := readCloudSetup(dir, "upload-credential.json")
		if err != nil {
			writeError(w, err, 400)
			return
		}
		defer clear(credential)
		public, e := cloudPublicConfig(raw)
		if e != nil {
			writeError(w, e, 400)
			return
		}
		d := cloudDestination{ID: cloudID(p), Config: public, ImportedAt: now(), plan: p, credential: credential}
		if p.PasswordProtected {
			bundle, e := readCloudSetup(dir, "recovery-bundle.json")
			if e != nil {
				writeError(w, e, 400)
				return
			}
			d.recovery = &cloudRecovery{}
			if json.Unmarshal(bundle, d.recovery) != nil || p.EncryptionSetup != "verified" || d.recovery.Version != 1 || d.recovery.Format != "age-password-recovery-v1" || d.recovery.Recipient != p.PublicRecipient || !strings.HasPrefix(d.recovery.EncryptedIdentity, "-----BEGIN AGE ENCRYPTED FILE-----") {
				writeError(w, errors.New("password setup has not been verified"), 400)
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		if err = s.cloud.begin("connect", "connect", "", cancel); err != nil {
			writeError(w, err, 409)
			return
		}
		defer s.cloud.end()
		uploader, err := newCloudUploader(ctx, d)
		if err == nil {
			dir, e := cloudWorkDir(s.Config(), "cloud-connect-")
			if e != nil {
				err = e
			} else {
				defer os.RemoveAll(dir)
				probe := make([]byte, 32)
				_, err = rand.Read(probe)
				if err == nil {
					err = os.WriteFile(filepath.Join(dir, "probe"), probe, 0600)
				}
				if err == nil {
					_, err = uploader.upload(ctx, p.Object+".roundtrip-test", filepath.Join(dir, "probe"), func(int64) {})
				}
				if err == nil {
					err = uploader.checkRestricted(ctx, p.Object+".roundtrip-test")
				}
			}
		}
		if err != nil {
			writeError(w, err, 400)
			return
		}
		if err = s.Catalog.saveCloudDestination(ctx, d); err != nil {
			writeError(w, err, 400)
			return
		}
		d.Connected = true
		d.credential = nil
		d.recovery = nil
		writeJSON(w, map[string]any{"destination": d}, 200)
	case "/api/cloud-backup/disconnect":
		id, _ := body["destination_id"].(string)
		s.cloud.mu.Lock()
		active := s.cloud.active
		s.cloud.mu.Unlock()
		if active != "" {
			writeError(w, errors.New("finish or cancel the current operation first"), 409)
			return
		}
		err := s.Catalog.forgetCloudCredential(r.Context(), id)
		if err != nil {
			writeError(w, err, 500)
			return
		}
		writeJSON(w, map[string]any{"disconnected": true}, 200)
	case "/api/cloud-backup/start":
		id, _ := body["destination_id"].(string)
		kind, _ := body["kind"].(string)
		if kind != "sample" && kind != "full" {
			writeError(w, errors.New("kind must be sample or full"), 400)
			return
		}
		run, err := s.startCloudRun(r.Context(), id, kind, "manual", time.Now())
		if err != nil {
			writeError(w, err, 409)
			return
		}
		writeJSON(w, run, 202)
	case "/api/cloud-backup/schedule":
		id, _ := body["destination_id"].(string)
		enabled, ok := body["enabled"].(bool)
		if !ok {
			writeError(w, errors.New("enabled must be true or false"), 400)
			return
		}
		var spec cloudScheduleSpec
		raw, _ := json.Marshal(body["schedule"])
		if enabled && json.Unmarshal(raw, &spec) != nil {
			writeError(w, errors.New("invalid backup schedule"), 400)
			return
		}
		if err := s.Catalog.setCloudSchedule(r.Context(), id, enabled, spec, time.Now()); err != nil {
			writeError(w, err, 400)
			return
		}
		writeJSON(w, map[string]any{"saved": true}, 200)
	case "/api/cloud-backup/cancel":
		id, _ := body["run_id"].(string)
		writeJSON(w, map[string]any{"cancelled": s.cloud.stop(id)}, 200)
	case "/api/cloud-backup/verify", "/api/cloud-backup/verification-command":
		id, _ := body["run_id"].(string)
		path, _ := body["archive_path"].(string)
		run, err := s.Catalog.cloudRun(r.Context(), id)
		if err != nil || run.State != "complete" {
			writeError(w, errors.New("choose a completed upload to verify"), 400)
			return
		}
		if run.VerificationState == "running" && run.VerificationHost == currentHost().ID && strings.HasSuffix(r.URL.Path, "/verify") {
			writeError(w, errors.New("verification is already running for this revision"), 409)
			return
		}
		path = expandPath(path)
		if !filepath.IsAbs(path) {
			writeError(w, errors.New("use an absolute archive file path or ~/"), 400)
			return
		}
		d, err := s.Catalog.cloudDestination(r.Context(), run.DestinationID, false)
		if err != nil {
			writeError(w, err, 400)
			return
		}
		command := shellWord(executablePath()) + " --config " + shellWord(s.Config().Path) + " cloud-backup verify " + shellWord(id) + " " + shellWord(path) + " </dev/tty\n"
		if d.plan.PasswordProtected || strings.HasSuffix(r.URL.Path, "verification-command") {
			writeJSON(w, map[string]any{"terminal_required": true, "command": command}, 200)
			return
		}
		ctx, cancel := context.WithCancel(s.life.ctx)
		if err = s.cloud.begin(run.ID, "verify", run.Kind, cancel); err != nil {
			cancel()
			writeError(w, err, 409)
			return
		}
		run.Verified = false
		run.VerificationState = "running"
		run.VerificationPID = os.Getpid()
		run.VerificationHost = currentHost().ID
		run.VerificationError = ""
		if err = s.Catalog.saveCloudRun(r.Context(), &run, nil); err != nil {
			s.cloud.end()
			cancel()
			writeError(w, err, 500)
			return
		}
		if !s.spawn(func(context.Context) {
			defer s.cloud.end()
			defer cancel()
			_ = s.Catalog.finishCloudVerification(ctx, s.Config(), &run, path, nil)
		}) {
			s.cloud.end()
			cancel()
			writeError(w, errors.New("Pharos is stopping"), 503)
			return
		}
		writeJSON(w, map[string]any{"started": true}, 202)
	default:
		writeError(w, errors.New("unknown cloud operation"), 404)
	}
}
func (s *Server) uploadCloudRun(ctx context.Context, d cloudDestination, run cloudRun) {
	var result error
	var manifest *cloudManifest
	var last time.Time
	save := func(phase string, force bool) {
		run.Phase = phase
		s.cloud.report(run.ID, phase, run.Bytes, run.TotalBytes)
		if force || time.Since(last) > time.Second {
			last = time.Now()
			_ = s.Catalog.saveCloudRun(ctx, &run, nil)
		}
	}
	defer func() {
		if value := recover(); value != nil {
			s.exitIfFault(value)
			result = errors.New("cloud backup stopped unexpectedly")
		}
		run.State = "complete"
		if result == nil {
			run.Bytes = run.TotalBytes
		}
		if result != nil {
			run.State = "failed"
			run.Error = result.Error()
		}
		if ctx.Err() != nil && run.Version == "" {
			run.State = "cancelled"
			run.Error = "Cloud operation cancelled"
		}
		run.Phase = run.State
		final, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Catalog.saveCloudRun(final, &run, manifest); err == nil {
			_ = s.Catalog.settleCloudSchedule(final, run, time.Now())
		}
	}()
	uploader, err := newCloudUploader(ctx, d)
	if err != nil {
		result = err
		return
	}
	if err = uploader.checkRestricted(ctx, d.plan.Object+".roundtrip-test"); err != nil {
		if errors.Is(err, errCloudReadable) {
			forget, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if e := s.Catalog.forgetCloudCredential(forget, d.ID); e != nil {
				result = e
				return
			}
		}
		result = err
		return
	}
	dir, err := cloudWorkDir(s.Config(), "cloud-upload-"+run.ID+"-")
	if err != nil {
		result = err
		return
	}
	defer os.RemoveAll(dir)
	path, m, err := buildCloudArchive(ctx, d, &run, s.Config(), dir, func(phase string) { save(phase, false) })
	if err != nil {
		result = err
		return
	}
	manifest = &m
	if err = s.Catalog.saveCloudRun(ctx, &run, manifest); err != nil {
		result = err
		return
	}
	manifest = nil // file manifest is immutable; progress updates only write status
	save("Uploading archive", true)
	version, err := uploader.upload(ctx, run.Object, path, func(bytes int64) { run.Bytes = bytes; save("Uploading archive", false) })
	if err != nil {
		result = err
		return
	}
	run.Version = version
}
