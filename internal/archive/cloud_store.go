package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
)

type cloudConfig struct {
	Cadence           int    `json:"cadence"`
	Keep              int    `json:"keep"`
	MinimumAge        int    `json:"minimumAge"`
	RetentionMode     string `json:"retentionMode"`
	RetentionDays     int    `json:"retentionDays"`
	Time              string `json:"time"`
	Weekday           int    `json:"weekday"`
	Timezone          string `json:"timezone"`
	Provider          string `json:"provider"`
	Region            string `json:"region"`
	Name              string `json:"name"`
	Bucket            string `json:"bucket"`
	Object            string `json:"object"`
	Identity          string `json:"identity"`
	Project           string `json:"project"`
	Profile           string `json:"profile"`
	Account           string `json:"account"`
	Container         string `json:"container"`
	StorageClass      string `json:"storage_class"`
	PasswordProtected bool   `json:"passwordProtected"`
	PublicRecipient   string `json:"public_recipient,omitempty"`
	EncryptionSetup   string `json:"encryption_setup,omitempty"`
}

type cloudRecovery struct {
	Version           int    `json:"version"`
	Format            string `json:"format"`
	Recipient         string `json:"recipient"`
	EncryptedIdentity string `json:"encrypted_identity"`
}

type cloudDestination struct {
	ID              string          `json:"id"`
	Connected       bool            `json:"connected"`
	Config          json.RawMessage `json:"config"`
	ImportedAt      string          `json:"imported_at"`
	SampleVerified  bool            `json:"sample_verified"`
	RestoreVerified bool            `json:"restore_verified"`
	plan            cloudConfig
	credential      []byte
	recovery        *cloudRecovery
}

type cloudRun struct {
	OwnerPID          int    `json:"owner_pid,omitempty"`
	OwnerHost         string `json:"owner_host,omitempty"`
	VerificationPID   int    `json:"verification_pid,omitempty"`
	VerificationHost  string `json:"verification_host,omitempty"`
	ID                string `json:"id"`
	DestinationID     string `json:"destination_id"`
	Kind              string `json:"kind"`
	Trigger           string `json:"trigger,omitempty"`
	ScheduledFor      string `json:"scheduled_for,omitempty"`
	State             string `json:"state"`
	Phase             string `json:"phase"`
	Object            string `json:"object"`
	Version           string `json:"version,omitempty"`
	SHA256            string `json:"sha256,omitempty"`
	Bytes             int64  `json:"bytes"`
	TotalBytes        int64  `json:"total_bytes"`
	FileBytes         int64  `json:"file_bytes"`
	Files             int    `json:"files"`
	StartedAt         string `json:"started_at"`
	UpdatedAt         string `json:"updated_at"`
	Error             string `json:"error,omitempty"`
	Verified          bool   `json:"verified"`
	VerificationState string `json:"verification_state,omitempty"`
	VerificationError string `json:"verification_error,omitempty"`
	RestoredDirectory string `json:"restored_directory,omitempty"`
}

var cloudName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
var cloudObject = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_./-]{0,199}$`)
var cloudRegion = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)

func (p cloudConfig) validate() error {
	if !cloudRegion.MatchString(p.Region) || !cloudObject.MatchString(p.Object) || !filepath.IsLocal(p.Object) || strings.HasSuffix(p.Object, "/") || strings.Contains(p.Object, "//") {
		return errors.New("invalid cloud region or archive object")
	}
	for _, part := range strings.Split(p.Object, "/") {
		if part == ".." || part == "." {
			return errors.New("invalid archive object path")
		}
	}
	allowed := map[string][]string{"aws": {"STANDARD", "STANDARD_IA", "GLACIER_IR", "DEEP_ARCHIVE"}, "gcp": {"STANDARD", "NEARLINE", "COLDLINE", "ARCHIVE"}, "azure": {"Hot", "Cool", "Cold"}}
	good := false
	for _, class := range allowed[p.Provider] {
		if class == p.StorageClass {
			good = true
		}
	}
	if !good {
		return errors.New("unsupported cloud provider or storage class")
	}
	if p.Provider == "azure" {
		if !regexp.MustCompile(`^[a-z0-9]{3,24}$`).MatchString(p.Account) || (!cloudName.MatchString(p.Container) || strings.Contains(p.Container, "--")) {
			return errors.New("invalid Azure account or container")
		}
	} else if !cloudName.MatchString(p.Bucket) {
		return errors.New("invalid cloud bucket")
	}
	if p.Provider == "gcp" && (!regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`).MatchString(p.Project) || !regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`).MatchString(p.Identity)) {
		return errors.New("invalid Google project or service-account ID")
	}
	if p.PasswordProtected {
		if _, err := age.ParseX25519Recipient(p.PublicRecipient); err != nil {
			return errors.New("complete password setup in Terminal before connecting this destination")
		}
	}
	return nil
}

func cloudID(p cloudConfig) string {
	name := p.Provider + "/" + p.Bucket + "/" + p.Object
	if p.Provider == "azure" {
		name = p.Provider + "/" + p.Account + "/" + p.Container + "/" + p.Object
	}
	h := sha256.Sum256([]byte(name))
	return "cloud-" + hex.EncodeToString(h[:12])
}
func cloudEqual(a, b cloudConfig) bool {
	a.PublicRecipient = ""
	b.PublicRecipient = ""
	a.EncryptionSetup = ""
	b.EncryptionSetup = ""
	return a == b
}

// Only declared nonsecret configuration fields may be returned to a browser.
func cloudPublicConfig(raw []byte) (json.RawMessage, error) {
	var input map[string]json.RawMessage
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	allowed := strings.Fields("provider region tier name bucket object identity project projectMode billingAccount projectParent profile subscription account group container sizeGB compression cadence keep minimumAge retentionMode retentionDays passwordProtected time weekday timezone egress tokenDays growthBasis historyDays growthGBPerMonth credential_storage encryption restore_auth interactive_upload encryption_setup recovery_bundle_file include_recovery_bundle format storage_class versioning test_object upload_bandwidth_limit_mbps effective_part_mib credential_file schedule_enabled prototype pricing_reviewed public_recipient")
	output := map[string]json.RawMessage{}
	for _, key := range allowed {
		if value, ok := input[key]; ok {
			output[key] = value
		}
	}
	return json.Marshal(output)
}

func readCloudSetup(dir, name string) ([]byte, error) {
	root, err := os.OpenRoot(expandPath(dir))
	if err != nil {
		return nil, errors.New("setup folder is unavailable")
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("%s must be a regular setup file under 1 MiB", name)
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("setup file is not a regular file")
	}
	return io.ReadAll(io.LimitReader(file, 1<<20+1))
}

func (c *Catalog) cloudDestination(ctx context.Context, id string, secret bool) (cloudDestination, error) {
	var d cloudDestination
	var raw, credentials, recovery string
	err := c.reader().QueryRowContext(ctx, "SELECT id,config_json,credential_json,recovery_json,imported_at FROM cloud_destinations WHERE id=?", id).Scan(&d.ID, &raw, &credentials, &recovery, &d.ImportedAt)
	if err != nil {
		return d, err
	}
	d.Connected = credentials != ""
	d.Config = json.RawMessage(raw)
	if err = json.Unmarshal(d.Config, &d.plan); err != nil {
		return d, err
	}
	if secret {
		d.credential = []byte(credentials)
		if recovery != "" {
			d.recovery = &cloudRecovery{}
			err = json.Unmarshal([]byte(recovery), d.recovery)
		}
	}
	return d, err
}
func (c *Catalog) cloudDestinations(ctx context.Context) ([]cloudDestination, error) {
	rows, err := c.reader().QueryContext(ctx, `SELECT d.id,d.config_json,d.imported_at,d.credential_json<>'',
      EXISTS(SELECT 1 FROM cloud_backup_runs r WHERE r.destination_id=d.id AND json_extract(r.status_json,'$.kind')='sample' AND json_extract(r.status_json,'$.verified')=1),
      EXISTS(SELECT 1 FROM cloud_backup_runs r WHERE r.destination_id=d.id AND json_extract(r.status_json,'$.kind')='full' AND json_extract(r.status_json,'$.verified')=1)
      FROM cloud_destinations d ORDER BY d.imported_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []cloudDestination{}
	for rows.Next() {
		var d cloudDestination
		var raw string
		if err := rows.Scan(&d.ID, &raw, &d.ImportedAt, &d.Connected, &d.SampleVerified, &d.RestoreVerified); err != nil {
			return nil, err
		}
		d.Config = json.RawMessage(raw)
		out = append(out, d)
	}
	return out, rows.Err()
}
func (c *Catalog) saveCloudDestination(ctx context.Context, d cloudDestination) error {
	recovery := ""
	if d.recovery != nil {
		b, _ := json.Marshal(d.recovery)
		recovery = string(b)
	}
	return c.writeTransaction(ctx, "cloud-connect", func(tx *sql.Tx) error {
		var existing, oldRecovery string
		err := tx.QueryRowContext(ctx, "SELECT config_json,recovery_json FROM cloud_destinations WHERE id=?", d.ID).Scan(&existing, &oldRecovery)
		if err == nil {
			var old cloudConfig
			_ = json.Unmarshal([]byte(existing), &old)
			if oldRecovery != recovery {
				return errors.New("recovery bundle differs from this destination; keep the original bundle or choose a new archive object")
			}
			if old != d.plan {
				return errors.New("this bucket/object is already connected with different settings; use its existing configuration or choose a new object")
			}
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO cloud_destinations(id,config_json,credential_json,recovery_json,imported_at) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET credential_json=excluded.credential_json, imported_at=excluded.imported_at", d.ID, string(d.Config), string(d.credential), recovery, d.ImportedAt)
		return err
	})
}
func (c *Catalog) cloudRun(ctx context.Context, id string) (cloudRun, error) {
	var r cloudRun
	var raw string
	err := c.reader().QueryRowContext(ctx, "SELECT status_json FROM cloud_backup_runs WHERE id=?", id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &r)
	}
	return r, err
}
func (c *Catalog) cloudRuns(ctx context.Context) ([]cloudRun, error) {
	rows, err := c.reader().QueryContext(ctx, "SELECT status_json FROM cloud_backup_runs ORDER BY created_at DESC LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []cloudRun{}
	for rows.Next() {
		var r cloudRun
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (c *Catalog) saveCloudRun(ctx context.Context, r *cloudRun, manifest *cloudManifest) error {
	r.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var m any
	if manifest != nil {
		b, e := json.Marshal(manifest)
		if e != nil {
			return e
		}
		m = string(b)
	}
	return c.writeTransaction(ctx, "cloud-run", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO cloud_backup_runs(id,destination_id,status_json,manifest_json,created_at) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status_json=excluded.status_json,manifest_json=COALESCE(excluded.manifest_json,cloud_backup_runs.manifest_json)", r.ID, r.DestinationID, string(raw), m, r.StartedAt)
		return err
	})
}

func (c *Catalog) forgetCloudCredential(ctx context.Context, id string) error {
	return c.writeTransaction(ctx, "cloud-disconnect", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE cloud_destinations SET credential_json='' WHERE id=?", id)
		if err == nil {
			_, err = tx.ExecContext(ctx, "UPDATE cloud_backup_schedules SET state_json=json_set(state_json,'$.enabled',json('false')) WHERE destination_id=?", id)
		}
		return err
	})
}

func (c *Catalog) cloudHasVerified(ctx context.Context, id, kind string) (bool, error) {
	var verified bool
	err := c.reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM cloud_backup_runs WHERE destination_id=? AND json_extract(status_json,'$.kind')=? AND json_extract(status_json,'$.verified')=1)`, id, kind).Scan(&verified)
	return verified, err
}
