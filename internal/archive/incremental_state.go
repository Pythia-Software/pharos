package archive

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const conversationDerivationVersion = "sessions-ledger-instructions-v1"

func physicalPartCount(parts []sourcePart) int {
	count := 0
	for _, part := range parts {
		if !strings.HasPrefix(part.item, "group:") {
			count++
		}
	}
	return count
}

type ingestMode struct {
	automatic  bool
	force      bool
	discovered func([]sourcePart, bool)
	written    func(string)
}

type ingestModeKey struct{}

func withIngestMode(ctx context.Context, mode ingestMode) context.Context {
	return context.WithValue(ctx, ingestModeKey{}, mode)
}

func modeOf(ctx context.Context) ingestMode {
	mode, _ := ctx.Value(ingestModeKey{}).(ingestMode)
	return mode
}

func (c *Catalog) effectiveIndexVersion(host, source string, adapter Adapter) (string, error) {
	var generation int64
	err := c.DB.QueryRow("SELECT generation FROM sync_generations WHERE host_id=? AND source_name=?", host, source).Scan(&generation)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	version := sourceIndexVersion(adapter)
	if generation != 0 {
		version += fmt.Sprintf("@generation:%d", generation)
	}
	return version, nil
}

func (c *Catalog) advanceSyncGeneration(ctx context.Context, host, source string) error {
	_, err := c.DB.ExecContext(ctx, `INSERT INTO sync_generations(host_id,source_name,generation) VALUES(?,?,1)
		ON CONFLICT(host_id,source_name) DO UPDATE SET generation=generation+1`, host, source)
	return err
}
