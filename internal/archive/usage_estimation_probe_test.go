//go:build probe

package archive

// The scalar replay opens no catalog. The opt-in hot-path probe at the end
// uses disposable test catalogs only.
// PHAROS_ESTIMATION_REPLAY_INPUT="$PWD/.context/cases.jsonl" \
// PHAROS_ESTIMATION_REPLAY_OUTPUT="$PWD/.context/predictions.jsonl" \
// go test -tags probe -run '^TestUsageEstimationScalarReplay$' ./internal/archive

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

type estimationReplayBlock struct {
	Kind               string `json:"kind"`
	Call               string `json:"call"`
	Tool               string `json:"tool"`
	Start              string `json:"start"`
	Stop               string `json:"stop"`
	Characters         int    `json:"characters"`
	ThinkingHidden     *bool  `json:"thinking_hidden"`
	Summaries          int    `json:"summaries"`
	ArgumentBytes      int    `json:"argument_bytes"`
	ArgumentCharacters int    `json:"argument_characters"`
	InputKnown         bool   `json:"input_known"`
	Delegation         bool   `json:"delegation"`
}

// Payloads are synthetic strings preserving scalar character/byte counts.
// This deliberately cannot consume truth labels or tool-result text.
func replayArgument(characters, bytes int) (string, error) {
	if characters == 0 {
		characters = bytes
	}
	if bytes < characters || characters < 2 || bytes > 4*(characters-2)+2 || bytes > 4000000 {
		return "", fmt.Errorf("unrepresentable scalar argument dimensions %d/%d", characters, bytes)
	}
	var b strings.Builder
	extra := bytes - characters
	for i := 0; i < characters-2; i++ {
		switch {
		case extra >= 3:
			b.WriteRune('🙂')
			extra -= 3
		case extra == 2:
			b.WriteRune('界')
			extra -= 2
		case extra == 1:
			b.WriteRune('é')
			extra--
		default:
			b.WriteByte('x')
		}
	}
	return b.String(), nil
}

func TestUsageEstimationScalarReplay(t *testing.T) {
	inPath, outPath := os.Getenv("PHAROS_ESTIMATION_REPLAY_INPUT"), os.Getenv("PHAROS_ESTIMATION_REPLAY_OUTPUT")
	if inPath == "" || outPath == "" {
		t.Skip("set scalar replay input and output paths")
	}
	in, err := os.Open(inPath)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	writer := bufio.NewWriter(out)
	defer writer.Flush()
	encoder := json.NewEncoder(writer)
	manifests := map[string]estimationManifest{}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 65536), 16000000)
	count := 0
	for scanner.Scan() {
		var row struct {
			ID            string                  `json:"id"`
			Manifest      string                  `json:"manifest"`
			Model         string                  `json:"model"`
			Effort        string                  `json:"effort"`
			Speed         string                  `json:"speed"`
			SpeedRecorded bool                    `json:"speed_recorded"`
			SenderCreated string                  `json:"sender_created"`
			Blocks        []estimationReplayBlock `json:"blocks"`
		}
		decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&row); err != nil {
			t.Fatal(err)
		}
		manifest, exists := manifests[row.Manifest]
		if !exists {
			data, err := os.ReadFile(row.Manifest)
			if err != nil {
				t.Fatal(err)
			}
			// Baseline v3 is replayed under the current schema with its feature
			// disabled, preserving every v3 point parameter and route.
			var header map[string]any
			if err := json.Unmarshal(data, &header); err != nil {
				t.Fatal(err)
			}
			if header["version"] == float64(3) {
				header["version"] = 4
				data = []byte(jsonText(header))
			}
			manifest, err = parseEstimationManifest(data)
			if err != nil {
				t.Fatal(err)
			}
			manifests[row.Manifest] = manifest
		}
		builder := newUsageEstimationBuilder("claude-ai-export")
		if row.SenderCreated != "" {
			builder.observe(map[string]any{"id": "sender", "native_id": "prompt", "role": "user", "kind": "message", "source_order": -1, "raw_text": jsonText(map[string]any{"text": "Sender", "claude_ai_message": map[string]any{"uuid": "prompt", "sender": "human", "created_at": row.SenderCreated}})})
		}
		for i, block := range row.Blocks {
			if block.Characters < 0 || block.Characters > 2000000 || block.Summaries < 0 || block.Summaries > 1000 {
				t.Fatal("invalid scalar replay size")
			}
			raw := map[string]any{"type": block.Kind}
			if block.Start != "" {
				raw["start_timestamp"] = block.Start
			}
			if block.Stop != "" {
				raw["stop_timestamp"] = block.Stop
			}
			role, kind := "assistant", "message"
			switch block.Kind {
			case "text":
				raw["text"] = strings.Repeat("x", block.Characters)
			case "thinking":
				kind = "reasoning"
				raw["thinking"] = strings.Repeat("x", block.Characters)
				if block.ThinkingHidden != nil {
					raw["thinking_hidden"] = *block.ThinkingHidden
				}
				summaries := make([]any, block.Summaries)
				for j := range summaries {
					summaries[j] = map[string]any{"summary": "Summary"}
				}
				raw["summaries"] = summaries
			case "tool_use":
				kind = "tool_call"
				if block.Delegation {
					kind = "delegation"
				}
				raw["id"], raw["name"] = block.Call, block.Tool
				if block.InputKnown {
					argument, err := replayArgument(block.ArgumentCharacters, block.ArgumentBytes)
					if err != nil {
						t.Fatal(row.ID, err)
					}
					raw["input"] = argument
				}
			case "tool_result":
				role, kind = "tool", "tool_result"
				raw["tool_use_id"], raw["content"] = block.Call, "Result"
			default:
				t.Fatal("unsupported replay block", block.Kind)
			}
			if i == 0 {
				envelope := map[string]any{"uuid": "reply", "sender": "assistant", "parent_message_uuid": "prompt", "model": row.Model, "effort": row.Effort}
				if row.SpeedRecorded {
					envelope["speed"] = row.Speed
				}
				raw["claude_ai_message"] = envelope
			}
			builder.observe(map[string]any{"id": fmt.Sprintf("block-%d", i), "native_id": fmt.Sprintf("reply:block:%d", i), "role": role, "kind": kind, "source_order": i, "raw_text": jsonText(raw)})
		}
		report := builder.estimate(manifest, estimationCalibration{}, hashBytes([]byte(row.Manifest)))
		if len(report.Responses) != 1 || !report.Responses[0].Output.valid() {
			t.Fatal("invalid replay result", row.ID, report)
		}
		if err := encoder.Encode(map[string]any{"id": row.ID, "estimate": report.Responses[0]}); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	t.Logf("Replayed %d scalar responses through the production estimator", count)
}

// Manual, disposable-catalog comparison of native coding writes. This measures
// trigger overhead, not production sync throughput or a real-library backfill.
// PHAROS_ESTIMATION_HOTPATH_OUTPUT="$PWD/.context/estimation-hotpath.json" go test -tags probe -run '^TestUsageEstimationHotpathProbe$' ./internal/archive
func TestUsageEstimationHotpathProbe(t *testing.T) {
	out := os.Getenv("PHAROS_ESTIMATION_HOTPATH_OUTPUT")
	if out == "" {
		t.Skip("manual hot-path probe")
	}
	const count = 500
	type result struct {
		Triggers bool      `json:"triggers"`
		Samples  []float64 `json:"milliseconds"`
		Median   float64   `json:"median_ms"`
	}
	results := []result{}
	for _, enabled := range []bool{true, false} {
		c, _ := testCatalog(t)
		seedReaderWorkspace(t, c)
		execTx(t, c.DB, "UPDATE conversations SET provider='claude',harness='claude-code' WHERE id='main'")
		if !enabled {
			for _, row := range queryRows(t, c, "SELECT name FROM sqlite_master WHERE type='trigger' AND name LIKE 'usage_estimation_%'") {
				execTx(t, c.DB, "DROP TRIGGER "+sqlIdentifier(firstString(row["name"])))
			}
		}
		sample := result{Triggers: enabled}
		for trial := 0; trial < 3; trial++ {
			raw := jsonText(map[string]any{"type": "assistant", "text": strings.Repeat("x", 2048)})
			start := time.Now()
			tx, err := c.DB.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			messages, err := tx.Prepare(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,raw_text,content_hash) VALUES(?,'main',?,'assistant','message',?,?,?)`)
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := tx.Prepare(`INSERT INTO model_request_evidence(message_id,conversation_id,native_id,thinking_ms) VALUES(?,'main',?,5000)`)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < count; i++ {
				id := fmt.Sprintf("native-probe-%d-%d", trial, i)
				if _, err := messages.Exec(id, id, raw, raw, id); err != nil {
					t.Fatal(err)
				}
				if _, err := evidence.Exec(id, id); err != nil {
					t.Fatal(err)
				}
			}
			messages.Close()
			evidence.Close()
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			sample.Samples = append(sample.Samples, float64(time.Since(start))/float64(time.Millisecond))
		}
		sorted := append([]float64{}, sample.Samples...)
		sort.Float64s(sorted)
		sample.Median = sorted[len(sorted)/2]
		results = append(results, sample)
	}
	data, err := json.MarshalIndent(map[string]any{"rows_per_sample": count, "payload_bytes": 2048, "includes": "message plus native thinking timer insert", "results": results, "scope": "synthetic_disposable_catalog_not_production_throughput"}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0600); err != nil {
		t.Fatal(err)
	}
}
