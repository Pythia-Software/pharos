package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

// teamsVersionAtLeast compares the explicit three-component capability version.
// Development builds must not invent a version: their caller must supply its
// actual supported Teams protocol capability.
func teamsVersionAtLeast(actual, required string) bool {
	if !teamsVersionPattern.MatchString(actual) || !teamsVersionPattern.MatchString(required) {
		return false
	}
	a, b := strings.Split(actual, "."), strings.Split(required, ".")
	for i := range a {
		x, e1 := strconv.ParseUint(a[i], 10, 64)
		y, e2 := strconv.ParseUint(b[i], 10, 64)
		if e1 != nil || e2 != nil {
			return false
		}
		if x != y {
			return x > y
		}
	}
	return true
}

// sendTeamsApproved is an internal building block, not an enabled worker. The
// caller must construct client from the protected credential bound to descriptor,
// org and installation and hold the service's library-use lease. It orders the
// final privacy/consent check and bounded send against local policy mutations.
func (c *Catalog) sendTeamsApproved(ctx context.Context, client *teams.DeviceClient, descriptor LibraryDescriptor, orgID, installationID, appVersion, workID string) error {
	if client == nil || !teamsVersionPattern.MatchString(appVersion) {
		return errTeamsConsentStale
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	staged, err := readTeamsOutbox(ctx, c.reader(), workID)
	if err != nil {
		return err
	}
	if staged.State != "approved" || staged.LibraryID != descriptor.ID || staged.InstanceID != descriptor.Instance || staged.OrgID != orgID || staged.InstallationID != installationID {
		return errTeamsConsentStale
	}
	poll, err := client.Poll(ctx, teams.PollRequest{AppVersion: appVersion, LibraryIDs: []string{descriptor.ID}})
	if err != nil {
		return err
	}
	for _, cancelled := range poll.Cancellations {
		if cancelled.WorkID == workID {
			return errTeamsConsentStale
		}
	}
	var work *teams.Work
	for i := range poll.Work {
		if poll.Work[i].ID == workID {
			if work != nil {
				return errTeamsConsentStale
			}
			work = &poll.Work[i]
		}
	}
	if work == nil || work.LibraryID != staged.LibraryID || work.VersionID != staged.VersionID || work.ConfigSHA256 != staged.ConfigHash || !bytes.Equal(work.Config, staged.Config) || work.EvaluationRevision != staged.EvaluationRevision || work.BaselineRevision != staged.BaselineRevision || work.RequiredLocalRevision > staged.LocalRevision {
		return errTeamsConsentStale
	}
	if _, err = teamsPlanFromWork(*work); err != nil {
		return err
	}
	var config struct {
		MinAppVersion string `json:"min_app_version"`
	}
	if json.Unmarshal(work.Config, &config) != nil || !teamsVersionAtLeast(appVersion, config.MinAppVersion) {
		return errTeamsQueryUnsupported
	}
	if _, err = c.inspectTeamsApproved(ctx, descriptor, workID, staged.SharedPrivacyRevision, staged.OwnershipRevision, poll.MetricsDefault); err != nil {
		return err
	}
	var manifest teams.Manifest
	if json.Unmarshal(staged.Manifest, &manifest) != nil {
		return errTeamsConsentStale
	}
	sources, err := client.RegisterSources(ctx, workID, manifest)
	if err != nil {
		return err
	}
	privacy, err := client.Privacy(ctx, workID)
	if err != nil {
		return err
	}
	registration, err := newTeamsRegistration(manifest, sources, privacy)
	if err != nil || registration.ManifestHash != staged.ManifestHash || registration.OwnershipRevision != staged.OwnershipRevision || registration.SharedPrivacyRevision != staged.SharedPrivacyRevision || teamsOwnedHash(registration.Owned) != staged.OwnedHash {
		return errTeamsConsentStale
	}
	reserved, err := c.reserveTeamsReviewed(ctx, descriptor, workID, privacy.PrivacyRevision, sources.OwnershipRevision, poll.MetricsDefault)
	if err != nil {
		return err
	}
	receipt, err := client.Submit(ctx, workID, reserved.Submission, reserved.PayloadHash)
	if err != nil {
		// Only an authenticated, recognized validation rejection proves that
		// this first emission was rejected. All other outcomes remain held.
		recordCtx, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		state, reason := teamsSubmitFailure(err)
		if stateErr := c.markTeamsSendOutcome(recordCtx, workID, state, reason); stateErr != nil {
			return errors.Join(err, stateErr)
		}
		return err
	}
	return c.recordTeamsReceipt(ctx, descriptor, orgID, installationID, workID, receipt)
}

// reconcileTeamsReceipt never re-emits a result. An authorized receipt lookup
// can resolve an interrupted attempt; absence or lost authorization leaves the
// immutable attempt unconfirmed until work and registration are reconciled.
func (c *Catalog) reconcileTeamsReceipt(ctx context.Context, client *teams.DeviceClient, descriptor LibraryDescriptor, orgID, installationID, workID string) error {
	if client == nil {
		return errTeamsConsentStale
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	staged, err := readTeamsOutbox(ctx, c.reader(), workID)
	if err != nil {
		return err
	}
	if (staged.State != "needs_reconcile" && staged.State != "unconfirmed") || staged.LibraryID != descriptor.ID || staged.InstanceID != descriptor.Instance || staged.OrgID != orgID || staged.InstallationID != installationID {
		return errTeamsConsentStale
	}
	receipt, err := client.Receipt(ctx, workID, staged.PayloadHash, staged.SubmissionHash, staged.IdempotencyKey)
	if err != nil {
		if errors.Is(err, teams.ErrReceiptMismatch) && staged.State == "needs_reconcile" {
			recordCtx, done := context.WithTimeout(context.Background(), 2*time.Second)
			defer done()
			if stateErr := c.markTeamsSendOutcome(recordCtx, workID, "unconfirmed", "receipt_mismatch"); stateErr != nil {
				return errors.Join(err, stateErr)
			}
		}
		return err
	}
	return c.recordTeamsReceipt(ctx, descriptor, orgID, installationID, workID, receipt)
}

func teamsSubmitFailure(err error) (string, string) {
	if errors.Is(err, teams.ErrReceiptMismatch) {
		return "unconfirmed", "receipt_mismatch"
	}
	var response *teams.HTTPError
	if errors.As(err, &response) {
		if response.Status == 409 {
			switch response.Code {
			case "work_cancelled", "work_expired", "work_not_eligible", "stale_config", "stale_evaluation", "stale_manifest", "stale_local_revision", "stale_privacy", "stale_ownership", "stale_baseline", "ownership_transition_pending", "ownership_snapshot_required", "privacy_withdrawal_pending", "paused", "superseded_version", "approval_required", "finalized", "unsupported_work":
				return "rejected", "stale_state"
			}
		}
		if response.Status == 422 {
			switch response.Code {
			case "coverage_incomplete", "manifest_too_large", "projection_too_large", "feed_batch_too_large", "invalid_input":
				return "rejected", "invalid_result"
			}
		}
		if response.Status >= 400 && response.Status < 500 && response.Status != 408 && response.Status != 429 {
			return "unconfirmed", "authorization"
		}
	}
	return "needs_reconcile", "transport"
}

// withTeamsLibraryClient binds a send or receipt lookup to the current local
// instance and protected installation credential. No worker calls it until
// recovery, capability declaration and the remaining consent UI are enabled.
func (s *Server) withTeamsLibraryClient(ctx context.Context, action func(*teams.DeviceClient, LibraryDescriptor, *teams.EnrollmentCredential) error) error {
	cfg := s.Config()
	if s.Catalog == nil || cfg.CatalogPath == "" || canonical(cfg.CatalogPath) != canonical(s.Catalog.Path) {
		return errTeamsNotEnrolled
	}
	use, err := fileLease(libraryUseLock(cfg), false)
	if err != nil {
		return err
	}
	defer use.Close()
	descriptor, err := readLibraryDescriptor(cfg)
	if err != nil {
		return err
	}
	credential, err := s.Catalog.loadTeamsCredential(cfg, descriptor)
	if err != nil {
		return err
	}
	defer credential.Destroy()
	key := credential.SigningKey()
	defer clear(key)
	client, err := teams.NewDeviceClient(credential.Origin, credential.InstallationID, key, false)
	if err != nil {
		return err
	}
	defer client.Destroy()
	if err := ctx.Err(); err != nil {
		return err
	}
	return action(client, descriptor, credential)
}

func (s *Server) sendTeamsLibraryApproved(ctx context.Context, workID, appVersion string) error {
	return s.withTeamsLibraryClient(ctx, func(client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential) error {
		return s.Catalog.sendTeamsApproved(ctx, client, d, credential.OrgID, credential.InstallationID, appVersion, workID)
	})
}

func (s *Server) reconcileTeamsLibraryReceipt(ctx context.Context, workID string) error {
	return s.withTeamsLibraryClient(ctx, func(client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential) error {
		return s.Catalog.reconcileTeamsReceipt(ctx, client, d, credential.OrgID, credential.InstallationID, workID)
	})
}

func (s *Server) retryTeamsLibraryUncertain(ctx context.Context, workID, appVersion string) error {
	return s.withTeamsLibraryClient(ctx, func(client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential) error {
		return s.Catalog.retryTeamsUncertain(ctx, client, d, credential.OrgID, credential.InstallationID, appVersion, workID)
	})
}

// registerTeamsLibraryManifest is the protected-library entry point for a
// future worker. The resolver remains explicit until provider identity is
// reviewed; no production fallback can silently bless the "local" account.
func (s *Server) registerTeamsLibraryManifest(ctx context.Context, work teams.Work, appVersion string, resolve teamsSourceResolver) (teamsSourceSnapshot, teamsPreparedManifest, teams.SourcesResponse, error) {
	var snapshot teamsSourceSnapshot
	var prepared teamsPreparedManifest
	var sources teams.SourcesResponse
	err := s.withTeamsLibraryClient(ctx, func(client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential) error {
		key := credential.SourceKey()
		defer clear(key)
		var err error
		snapshot, err = s.Catalog.teamsSourceSnapshot(ctx, key, resolve)
		if err != nil {
			return err
		}
		prepared, sources, err = s.Catalog.registerTeamsManifest(ctx, client, d, snapshot, work, appVersion)
		return err
	})
	return snapshot, prepared, sources, err
}

// registerTeamsManifest orders the first source disclosure against privacy and
// consent changes. The config, minimum capability, work status and current
// organization policy are refreshed before the local decision and PUT.
func (c *Catalog) registerTeamsManifest(ctx context.Context, client *teams.DeviceClient, descriptor LibraryDescriptor, snapshot teamsSourceSnapshot, requested teams.Work, appVersion string) (teamsPreparedManifest, teams.SourcesResponse, error) {
	var prepared teamsPreparedManifest
	var sources teams.SourcesResponse
	if client == nil || !teamsVersionPattern.MatchString(appVersion) {
		return prepared, sources, errTeamsConsentStale
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return prepared, sources, err
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	poll, err := client.Poll(ctx, teams.PollRequest{AppVersion: appVersion, LibraryIDs: []string{descriptor.ID}})
	if err != nil {
		return prepared, sources, err
	}
	for _, cancellation := range poll.Cancellations {
		if cancellation.WorkID == requested.ID {
			return prepared, sources, errTeamsConsentStale
		}
	}
	var current *teams.Work
	for i := range poll.Work {
		if poll.Work[i].ID == requested.ID {
			if current != nil {
				return prepared, sources, errTeamsConsentStale
			}
			current = &poll.Work[i]
		}
	}
	if current == nil || current.LibraryID != descriptor.ID || current.VersionID != requested.VersionID || current.ConfigSHA256 != requested.ConfigSHA256 || !bytes.Equal(current.Config, requested.Config) || current.EvaluationRevision != requested.EvaluationRevision || current.BaselineRevision != requested.BaselineRevision {
		return prepared, sources, errTeamsConsentStale
	}
	if _, err = teamsPlanFromWork(*current); err != nil {
		return prepared, sources, err
	}
	var config struct {
		MinAppVersion string `json:"min_app_version"`
	}
	if json.Unmarshal(current.Config, &config) != nil || !teamsVersionAtLeast(appVersion, config.MinAppVersion) {
		return prepared, sources, errTeamsQueryUnsupported
	}
	prepared, err = c.prepareTeamsManifest(ctx, *current, descriptor, snapshot, poll.MetricsDefault)
	if err != nil {
		return prepared, sources, err
	}
	sources, err = client.RegisterSources(ctx, current.ID, prepared.Manifest)
	return prepared, sources, err
}

// retryTeamsUncertain may re-emit only an identical attempt. An authorized
// receipt-not-found says nothing about an older POST still in flight, so it
// never permits a new body or idempotency key for this work item.
func (c *Catalog) retryTeamsUncertain(ctx context.Context, client *teams.DeviceClient, descriptor LibraryDescriptor, orgID, installationID, appVersion, workID string) error {
	if client == nil || !teamsVersionPattern.MatchString(appVersion) {
		return errTeamsConsentStale
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	staged, err := readTeamsOutbox(ctx, c.reader(), workID)
	if err != nil {
		return err
	}
	if staged.State != "needs_reconcile" || staged.LibraryID != descriptor.ID || staged.InstanceID != descriptor.Instance || staged.OrgID != orgID || staged.InstallationID != installationID || len(staged.Submission) == 0 || teamsPayloadHash(staged.Submission) != staged.SubmissionHash {
		return errTeamsConsentStale
	}
	receipt, err := client.Receipt(ctx, workID, staged.PayloadHash, staged.SubmissionHash, staged.IdempotencyKey)
	if err == nil {
		return c.recordTeamsReceipt(ctx, descriptor, orgID, installationID, workID, receipt)
	}
	if errors.Is(err, teams.ErrReceiptMismatch) {
		recordCtx, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		if e := c.markTeamsSendOutcome(recordCtx, workID, "unconfirmed", "receipt_mismatch"); e != nil {
			return errors.Join(err, e)
		}
		return err
	}
	var response *teams.HTTPError
	if !errors.As(err, &response) || response.Status != 404 || response.Code != "receipt_not_found" {
		return err
	}
	poll, err := client.Poll(ctx, teams.PollRequest{AppVersion: appVersion, LibraryIDs: []string{descriptor.ID}})
	if err != nil {
		return err
	}
	for _, cancelled := range poll.Cancellations {
		if cancelled.WorkID == workID {
			return errTeamsConsentStale
		}
	}
	var work *teams.Work
	for i := range poll.Work {
		if poll.Work[i].ID == workID {
			if work != nil {
				return errTeamsConsentStale
			}
			work = &poll.Work[i]
		}
	}
	if work == nil || work.LibraryID != staged.LibraryID || work.VersionID != staged.VersionID || work.ConfigSHA256 != staged.ConfigHash || !bytes.Equal(work.Config, staged.Config) || work.EvaluationRevision != staged.EvaluationRevision || work.BaselineRevision != staged.BaselineRevision || work.RequiredLocalRevision > staged.LocalRevision {
		return errTeamsConsentStale
	}
	if _, err = teamsPlanFromWork(*work); err != nil {
		return err
	}
	var config struct {
		MinAppVersion string `json:"min_app_version"`
	}
	if json.Unmarshal(work.Config, &config) != nil || !teamsVersionAtLeast(appVersion, config.MinAppVersion) {
		return errTeamsQueryUnsupported
	}
	// Recheck local consent before disclosing the manifest, not merely before
	// re-emitting the result. The sharing gate remains held through both.
	err = c.writeTransaction(ctx, "teams-retry-consent", func(tx *sql.Tx) error {
		current, e := readTeamsOutbox(ctx, tx, workID)
		if e != nil {
			return e
		}
		if current.State != "needs_reconcile" || current.SubmissionHash != staged.SubmissionHash {
			return errTeamsConsentStale
		}
		return teamsCheckStaged(ctx, tx, descriptor, current, staged.SharedPrivacyRevision, staged.OwnershipRevision, poll.MetricsDefault)
	})
	if err != nil {
		return err
	}
	var manifest teams.Manifest
	if json.Unmarshal(staged.Manifest, &manifest) != nil {
		return errTeamsConsentStale
	}
	sources, err := client.RegisterSources(ctx, workID, manifest)
	if err != nil {
		return err
	}
	privacy, err := client.Privacy(ctx, workID)
	if err != nil {
		return err
	}
	registration, err := newTeamsRegistration(manifest, sources, privacy)
	if err != nil || registration.ManifestHash != staged.ManifestHash || registration.OwnershipRevision != staged.OwnershipRevision || registration.SharedPrivacyRevision != staged.SharedPrivacyRevision || teamsOwnedHash(registration.Owned) != staged.OwnedHash {
		return errTeamsConsentStale
	}
	err = c.writeTransaction(ctx, "teams-retry-identical", func(tx *sql.Tx) error {
		current, e := readTeamsOutbox(ctx, tx, workID)
		if e != nil {
			return e
		}
		if current.State != "needs_reconcile" || current.SubmissionHash != staged.SubmissionHash || !bytes.Equal(current.Submission, staged.Submission) || current.IdempotencyKey != staged.IdempotencyKey {
			return errTeamsConsentStale
		}
		if e = teamsCheckStaged(ctx, tx, descriptor, current, privacy.PrivacyRevision, sources.OwnershipRevision, poll.MetricsDefault); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_outbox SET state='sending' WHERE work_id=?`, workID)
		return e
	})
	if err != nil {
		return err
	}
	receipt, err = client.Submit(ctx, workID, staged.Submission, staged.PayloadHash)
	if err != nil {
		recordCtx, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		state, reason := "needs_reconcile", "transport"
		if errors.Is(err, teams.ErrReceiptMismatch) {
			state, reason = "unconfirmed", "receipt_mismatch"
		}
		if e := c.markTeamsSendOutcome(recordCtx, workID, state, reason); e != nil {
			return errors.Join(err, e)
		}
		return err
	}
	return c.recordTeamsReceipt(ctx, descriptor, orgID, installationID, workID, receipt)
}
