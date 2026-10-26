package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path"
	"sort"
)

// Match the original inputs, not the now-removed captured payload. Reading
// original bytes is necessary only when checking an existing delivery receipt.
// After capture, use the manifest's hashes and snapshot states instead: they
// describe exactly the retained evidence and require no transcript reads.
func captureDeliveryFingerprint(ctx context.Context, source SourceConfig, plan capturePlan, captured *sourceCapture) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !plan.complete || len(plan.warnings) > 0 {
		return "", nil
	}
	states := []string{jsonText(source)}
	for _, item := range plan.files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if captured != nil {
			file := captured.files[path.Join(captureFilesDir, item.rel)]
			if file == nil || file.SHA256 == "" {
				return "", nil
			}
			states = append(states, jsonText([]any{item.path, file.Size, file.MTimeNS, file.SHA256}))
			continue
		}
		info, err := os.Stat(item.path)
		if err != nil {
			return "", nil
		}
		digest, err := captureDeliveryDigest(ctx, item.path)
		if err != nil {
			return "", ctx.Err()
		}
		states = append(states, jsonText([]any{item.path, info.Size(), info.ModTime().UnixNano(), digest}))
	}
	for _, item := range plan.databases {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var state sqliteSourceState
		if captured != nil {
			snapshot := captured.snapshots[path.Join(captureFilesDir, item.rel)]
			if snapshot == nil {
				return "", nil
			}
			state = snapshot.Source
		} else {
			var err error
			state, err = sqliteDependencyState(item.path)
			if err != nil {
				return "", nil
			}
		}
		states = append(states, item.path+jsonText(state))
	}
	sort.Strings(states)
	return hashBytes([]byte(jsonText(states))), nil
}

func captureDeliveryDigest(ctx context.Context, file string) (string, error) {
	in, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer in.Close()
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	for {
		if captureFault != nil {
			if err := captureFault("delivery-read", file); err != nil {
				return "", err
			}
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := in.Read(buffer)
		if n > 0 {
			hash.Write(buffer[:n])
		}
		if err == io.EOF {
			return hex.EncodeToString(hash.Sum(nil)), nil
		}
		if err != nil {
			return "", err
		}
	}
}
