package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

const pendingCaptureInputsKey = "pending_capture_inputs"

// A successful capture/index can wait for a disconnected destination without
// repeating capture. This receipt is local progress, never delivery acceptance.
type pendingCaptureInputs struct {
	Source, Target, Host, Build string
	Sources                     map[string]string
}

type pendingDeliveryPlan struct {
	Capture, Transfer, Resume bool
}

// pendingHistory checks every transferable history table except local state
// retained by a completed move and standalone host/machine observations.
// Workspace-only metadata and evidence without catalog rows are work too.
func pendingHistory(ctx context.Context, config Config) (bool, pendingCaptureInputs, error) {
	var captured pendingCaptureInputs
	db, err := openReadOnlySQLite(config.CatalogPath)
	if err != nil {
		return false, captured, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, captured, err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", pendingCaptureInputsKey).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, captured, err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &captured); err != nil {
			return false, captured, fmt.Errorf("read pending capture receipt: %w", err)
		}
	}
	tables, err := historyTables(ctx, tx)
	if err != nil {
		return false, captured, err
	}
	for _, table := range tables {
		if slices.Contains(moveRetainedTables, table) || table == "machines" || table == "host_machines" {
			continue
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+quotedIdentifier(table)+")").Scan(&exists); err != nil {
			return false, captured, err
		}
		if exists {
			return true, captured, nil
		}
	}
	_ = tx.Rollback()
	for _, root := range []string{config.CaptureRoot, config.ArchiveRoot} {
		found := false
		err := walkRetainedTree(ctx, root, false, func(path string, entry fs.DirEntry) error {
			// The capture host's last-check record alone carries no history.
			if root == config.CaptureRoot && entry.Name() == captureHostName && filepath.Dir(filepath.Dir(path)) == filepath.Clean(root) {
				return nil
			}
			found = true
			return fs.SkipAll
		})
		if err != nil || found {
			return found, captured, err
		}
	}
	return false, captured, nil
}

// planPendingDelivery only observes inputs. It runs before registering work or
// stopping either service. An interrupted cleanup takes precedence over an
// empty source catalog and must return to the existing recovery protocol.
func planPendingDelivery(ctx context.Context, config Config) (pendingDeliveryPlan, error) {
	var plan pendingDeliveryPlan
	var err error
	// An idle probe registers no operation, but its source-content reads still
	// belong at the same utility disk priority as background capture/delivery.
	withCaptureIOPolicy(func() { plan, err = readPendingDeliveryPlan(ctx, config) })
	return plan, err
}

func readPendingDeliveryPlan(ctx context.Context, config Config) (pendingDeliveryPlan, error) {
	plan := pendingDeliveryPlan{}
	if receipt, err := readTransferReceipt(pendingDeliveryPath(config)); err == nil {
		descriptor, err := readLibraryDescriptor(config)
		if err != nil {
			return plan, err
		}
		if receipt.Source != descriptor.Instance || receipt.Target != config.DeliveryTargetInstance {
			return plan, fmt.Errorf("pending cleanup belongs to a different library instance; resume to its original destination")
		}
		plan.Resume, plan.Transfer = true, true
		return plan, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return plan, err
	}
	pending, captured, err := pendingHistory(ctx, config)
	if err != nil {
		return plan, err
	}
	plan.Transfer = pending
	descriptor, err := readLibraryDescriptor(config)
	if err != nil {
		return plan, err
	}
	host := currentHost()
	for _, source := range config.Sources {
		if !source.Enabled {
			continue
		}
		if host.Fallback || !validCaptureName(host.ID) || !validCaptureName(source.Name) {
			return plan, fmt.Errorf("pending capture needs a stable host and source name")
		}
		dir := filepath.Join(config.CaptureRoot, host.ID, source.Name)
		var delivered struct {
			Fingerprint string `json:"fingerprint"`
			Target      string `json:"destination_instance"`
		}
		data, err := os.ReadFile(filepath.Join(dir, ".delivered.json"))
		if err == nil {
			if err := json.Unmarshal(data, &delivered); err != nil {
				return plan, fmt.Errorf("read %s delivery receipt: %w", source.Name, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return plan, err
		}
		prior := ""
		if captured.Source == descriptor.Instance && captured.Target == config.DeliveryTargetInstance && captured.Host == host.ID && captured.Build != "" && captured.Build == buildDigest() {
			prior = captured.Sources[source.Name]
		}
		if prior == "" && delivered.Target == config.DeliveryTargetInstance {
			prior = delivered.Fingerprint
		}
		if prior == "" {
			plan.Capture = true
			return plan, nil
		}
		inventory, err := planCapture(ctx, source)
		if err != nil {
			return plan, err
		}
		if isConductorSource(source.Kind) {
			manifest, err := loadCaptureManifest(dir)
			if err != nil {
				return plan, err
			}
			includeKnownCaptureDatabases(&inventory, manifest, source)
		}
		fingerprint, err := captureDeliveryFingerprint(ctx, source, inventory, nil)
		if err != nil {
			return plan, err
		}
		if fingerprint == "" {
			return plan, fmt.Errorf("%s inputs could not be verified; pending history retained", source.Name)
		}
		if fingerprint != prior {
			plan.Capture = true
			return plan, nil
		}
	}
	return plan, nil
}

// Snapshot successful capture observations before indexing. A later acquisition
// must not be acknowledged merely because its manifest appeared during the index.
func pendingCaptureReceipt(config Config, observed []CaptureSourceResult) (pendingCaptureInputs, error) {
	descriptor, err := readLibraryDescriptor(config)
	if err != nil {
		return pendingCaptureInputs{}, err
	}
	receipt := pendingCaptureInputs{Source: descriptor.Instance, Target: config.DeliveryTargetInstance, Host: currentHost().ID, Build: buildDigest(), Sources: map[string]string{}}
	for _, source := range observed {
		if source.State == "complete" && source.inputFingerprint != "" {
			receipt.Sources[source.Name] = source.inputFingerprint
		}
	}
	return receipt, nil
}

func recordPendingCapture(ctx context.Context, catalog *Catalog, receipt pendingCaptureInputs) error {
	return catalog.writeTransaction(ctx, "record pending capture inputs", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE value<>excluded.value`, pendingCaptureInputsKey, jsonText(receipt))
		return err
	})
}
