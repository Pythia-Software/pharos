package archive

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const claudeSourceTransformation = "claude-json-values-v1+exact-members"

// capturedEvidence owns its reader lease until Close. Transformation is empty
// for an original captured file; a reconstructed Claude input is explicitly
// source-faithful JSON plus exact opaque members, never the original ZIP bytes.
// NativeSHA256 identifies the filtered native input before retirement, not Path.
type capturedEvidence struct {
	Path           string
	Original       string
	NativeSHA256   string
	Transformation string
	Close          func()
}

// leaseCapturedEvidence resolves original paths retained in evidence locators.
// Freeze under the ordinary capture reader lock, then reconstruct outside it.
// The leased graph remains readable across capture rotation and managed cleanup.
func leaseCapturedEvidence(ctx context.Context, target captureTarget, original string) (capturedEvidence, error) {
	result := capturedEvidence{Original: original, Close: func() {}}
	lock, err := lockCaptureReader(ctx, target)
	if err != nil {
		return result, err
	}
	input, err := freezeCaptureInput(ctx, target, SourceConfig{}, false)
	lock.Close()
	if err != nil {
		return result, err
	}
	result.Close = input.Close
	fail := func(err error) (capturedEvidence, error) { input.Close(); return capturedEvidence{}, err }
	view := captureViewOf(input.Adapter)
	if view == nil {
		return fail(errors.New("captured evidence has no reader view"))
	}
	mapped, ok := view.captured(original)
	if !ok {
		return fail(errors.New("evidence path is outside the captured source"))
	}
	files := sourceFiles{ctx: ctx, view: view}
	if info, err := files.Stat(mapped); err == nil && info.Mode().IsRegular() {
		result.Path, err = files.Database(mapped)
		if err != nil {
			return fail(err)
		}
		return result, nil
	}
	if input.Adapter.Config().Kind != "claude-ai" || !view.manifest.ClaudeExportObjects {
		return fail(errors.New("original evidence file is unavailable"))
	}
	inputs, err := claudeExportInputs(files, input.Adapter.Config().Path)
	if err != nil {
		return fail(err)
	}
	for _, source := range inputs {
		if source.path != mapped || !source.retired || source.units == nil {
			continue
		}
		destination := filepath.Join(view.dir, "reconstructed"+strings.ToLower(filepath.Ext(mapped)))
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return fail(err)
		}
		err = source.units.reconstruct(files, source.objectRoot, strings.EqualFold(filepath.Ext(mapped), ".zip"), output)
		err = errors.Join(err, output.Close())
		if err != nil {
			return fail(err)
		}
		result.Path, result.NativeSHA256, result.Transformation = destination, source.sha256, claudeSourceTransformation
		return result, nil
	}
	return fail(errors.New("original evidence has no verified retained replacement"))
}

func (units *claudeSourceUnits) reconstruct(files sourceFiles, root string, zipped bool, output io.Writer) error {
	conversations := map[string][]claudeConversationUnit{}
	for _, unit := range units.Conversations {
		conversations[unit.Member] = append(conversations[unit.Member], unit)
	}
	writeConversations := func(writer io.Writer, member string) error {
		if _, err := io.WriteString(writer, "["); err != nil {
			return err
		}
		for index, unit := range conversations[member] {
			if err := files.context().Err(); err != nil {
				return err
			}
			if index > 0 {
				if _, err := io.WriteString(writer, ","); err != nil {
					return err
				}
			}
			raw, err := units.conversationJSON(files, root, unit)
			if err != nil {
				return err
			}
			if !json.Valid(raw) {
				return errors.New("invalid reconstructed conversation")
			}
			if _, err := writer.Write(raw); err != nil {
				return err
			}
		}
		_, err := io.WriteString(writer, "]")
		return err
	}
	if !zipped {
		return writeConversations(output, "")
	}
	archive := zip.NewWriter(output)
	for _, name := range units.Directories {
		if _, err := archive.Create(name); err != nil {
			return err
		}
	}
	for _, name := range units.ConversationMembers {
		writer, err := archive.Create(name)
		if err != nil {
			return err
		}
		if err := writeConversations(writer, name); err != nil {
			return err
		}
	}
	for _, member := range units.Members {
		if err := files.context().Err(); err != nil {
			return err
		}
		data, err := units.member(files, root, member)
		if err != nil {
			return err
		}
		writer, err := archive.Create(member.Name)
		if err != nil {
			return err
		}
		if _, err := writer.Write(data); err != nil {
			return err
		}
	}
	return archive.Close()
}
