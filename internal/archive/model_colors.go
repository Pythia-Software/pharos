package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

const modelColorPrefix = "pharos-model-color:"

type modelColorPalette struct {
	Overrides map[string]string `json:"overrides"`
	Models    []string          `json:"models"`
	Sources   []string          `json:"sources"`
}

func (c *Catalog) modelColors(ctx context.Context, inventory bool) (modelColorPalette, error) {
	palette := modelColorPalette{Overrides: map[string]string{}}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT key,value FROM ui_preferences WHERE key LIKE 'pharos-model-color:%'`)
	if err != nil {
		return palette, err
	}
	for _, row := range rows {
		var color string
		if json.Unmarshal([]byte(firstString(row["value"])), &color) == nil && validProjectColor(color) {
			palette.Overrides[strings.TrimPrefix(firstString(row["key"]), modelColorPrefix)] = color
		}
	}
	if !inventory {
		return palette, nil
	}
	palette.Models, palette.Sources = []string{}, []string{}
	// Include unpriced and locally named models, including those used only by
	// helpers. Reads never allocate or rewrite saved choices.
	rows, err = queryMapsContext(ctx, c.reader(), `SELECT model,provider FROM conversations
      UNION SELECT model,provider FROM agent_sessions
      UNION SELECT u.model,a.provider FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id
      UNION SELECT r.model,c.provider FROM model_requests r JOIN conversations c ON c.id=r.conversation_id`)
	if err != nil {
		return palette, err
	}
	models, sources := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		if model := modelFamily(firstString(row["model"])); model != "" && !models[model] {
			models[model] = true
			palette.Models = append(palette.Models, model)
		}
		if source := firstString(row["provider"]); source != "" && !sources[source] {
			sources[source] = true
			palette.Sources = append(palette.Sources, source)
		}
	}
	return palette, nil
}

func (c *Catalog) setModelColor(ctx context.Context, id, color string) error {
	kind, key, ok := strings.Cut(id, ":")
	if !ok || key == "" || len(key) > 256 || strings.ContainsAny(key, "\x00\r\n") || (kind != "provider" && kind != "model" && kind != "source") {
		return fmt.Errorf("choose a provider or model color")
	}
	if kind == "provider" && key != "anthropic" && key != "google" && key != "openai" && key != "other" {
		return fmt.Errorf("unknown model provider")
	}
	if kind == "model" && modelFamily(key) != key {
		return fmt.Errorf("use the model family identity")
	}
	if color != "" && !validProjectColor(color) {
		return fmt.Errorf("choose a six-digit hex color")
	}
	return c.writeTransaction(ctx, "model color", func(tx *sql.Tx) error {
		if color == "" {
			_, err := tx.ExecContext(ctx, `DELETE FROM ui_preferences WHERE key=?`, modelColorPrefix+id)
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO ui_preferences(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, modelColorPrefix+id, jsonText(strings.ToLower(color)))
		return err
	})
}
