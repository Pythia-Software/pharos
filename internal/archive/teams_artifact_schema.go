// Package artifacts validates the bounded structured result contract shared
// with local model execution. It grants no authority to read or publish data.
package archive

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gbdubs/pharos/internal/teams"
)

var errTeamsArtifactInvalid = errors.New("invalid artifact schema or value")

const teamsArtifactMaxSchemaBytes = 32768
const teamsArtifactMaxAnswerBytes = 131072

// teamsArtifactSchema is a closed subset. Unsupported JSON teamsArtifactSchema features are rejected.
type teamsArtifactSchema struct {
	Type                 string                     `json:"type"`
	Properties           map[string]json.RawMessage `json:"properties,omitempty"`
	Required             []string                   `json:"required,omitempty"`
	AdditionalProperties *bool                      `json:"additionalProperties,omitempty"`
	Items                json.RawMessage            `json:"items,omitempty"`
	MaxItems             *int                       `json:"maxItems,omitempty"`
	MaxLength            *int                       `json:"maxLength,omitempty"`
	Nullable             bool                       `json:"nullable,omitempty"`
	Enum                 []json.RawMessage          `json:"enum,omitempty"`
}
type teamsArtifactCompiled struct {
	schema     teamsArtifactSchema
	properties map[string]*teamsArtifactCompiled
	items      *teamsArtifactCompiled
}

func teamsCompileArtifactSchema(raw []byte) (*teamsArtifactCompiled, error) {
	if len(raw) == 0 || len(raw) > teamsArtifactMaxSchemaBytes {
		return nil, errTeamsArtifactInvalid
	}
	return teamsCompileArtifactNode(raw, 0)
}
func teamsCompileArtifactNode(raw []byte, depth int) (*teamsArtifactCompiled, error) {
	if depth > 5 {
		return nil, errTeamsArtifactInvalid
	}
	var s teamsArtifactSchema
	if teamsDecodeArtifactSchema(raw, &s) != nil {
		return nil, errTeamsArtifactInvalid
	}
	c := &teamsArtifactCompiled{schema: s}
	if len(s.Enum) > 64 {
		return nil, errTeamsArtifactInvalid
	}
	switch s.Type {
	case "object":
		if s.Properties == nil || len(s.Properties) > 64 || s.AdditionalProperties == nil || *s.AdditionalProperties || len(s.Items) > 0 || s.MaxItems != nil || s.MaxLength != nil || len(s.Enum) > 0 {
			return nil, errTeamsArtifactInvalid
		}
		c.properties = map[string]*teamsArtifactCompiled{}
		folded := map[string]bool{}
		for name, child := range s.Properties {
			if name == "" || len(name) > 128 || !utf8.ValidString(name) || folded[strings.ToLower(name)] {
				return nil, errTeamsArtifactInvalid
			}
			folded[strings.ToLower(name)] = true
			v, err := teamsCompileArtifactNode(child, depth+1)
			if err != nil {
				return nil, err
			}
			c.properties[name] = v
		}
		seen := map[string]bool{}
		for _, name := range s.Required {
			if seen[name] || c.properties[name] == nil {
				return nil, errTeamsArtifactInvalid
			}
			seen[name] = true
		}
	case "array":
		if len(s.Items) == 0 || s.MaxItems == nil || *s.MaxItems < 0 || *s.MaxItems > 100 || s.Properties != nil || s.Required != nil || s.AdditionalProperties != nil || s.MaxLength != nil || len(s.Enum) > 0 {
			return nil, errTeamsArtifactInvalid
		}
		v, err := teamsCompileArtifactNode(s.Items, depth+1)
		if err != nil {
			return nil, err
		}
		c.items = v
	case "string":
		if s.MaxLength == nil || *s.MaxLength < 0 || *s.MaxLength > 8192 || s.Properties != nil || s.Required != nil || s.AdditionalProperties != nil || len(s.Items) > 0 || s.MaxItems != nil {
			return nil, errTeamsArtifactInvalid
		}
	case "integer", "number", "boolean":
		if s.Properties != nil || s.Required != nil || s.AdditionalProperties != nil || len(s.Items) > 0 || s.MaxItems != nil || s.MaxLength != nil {
			return nil, errTeamsArtifactInvalid
		}
	default:
		return nil, errTeamsArtifactInvalid
	}
	seenEnum := []any{}
	for _, v := range s.Enum {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) || c.validate(v, false) != nil {
			return nil, errTeamsArtifactInvalid
		}
		var parsed any
		if teamsArtifactDecodeValue(v, &parsed) != nil {
			return nil, errTeamsArtifactInvalid
		}
		canonical, err := json.Marshal(parsed)
		if err != nil {
			return nil, errTeamsArtifactInvalid
		}
		for _, prior := range seenEnum {
			encoded, _ := json.Marshal(prior)
			if teamsArtifactEqualValues(prior, parsed, encoded, canonical) {
				return nil, errTeamsArtifactInvalid
			}
		}
		seenEnum = append(seenEnum, parsed)
	}
	return c, nil
}
func teamsValidateArtifact(schema, answer []byte) error {
	if len(answer) == 0 || len(answer) > teamsArtifactMaxAnswerBytes {
		return errTeamsArtifactInvalid
	}
	c, err := teamsCompileArtifactSchema(schema)
	if err != nil {
		return err
	}
	return c.validate(answer, true)
}
func (c *teamsArtifactCompiled) validate(raw []byte, checkEnum bool) error {
	var value any
	if teamsArtifactDecodeValue(raw, &value) != nil {
		return errTeamsArtifactInvalid
	}
	if value == nil {
		if c.schema.Nullable {
			return nil
		}
		return errTeamsArtifactInvalid
	}
	switch c.schema.Type {
	case "object":
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil || obj == nil {
			return errTeamsArtifactInvalid
		}
		for _, name := range c.schema.Required {
			if _, ok := obj[name]; !ok {
				return errTeamsArtifactInvalid
			}
		}
		for name, v := range obj {
			child, ok := c.properties[name]
			if !ok || child.validate(v, true) != nil {
				return errTeamsArtifactInvalid
			}
		}
	case "array":
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) != nil || arr == nil || len(arr) > *c.schema.MaxItems {
			return errTeamsArtifactInvalid
		}
		for _, v := range arr {
			if c.items.validate(v, true) != nil {
				return errTeamsArtifactInvalid
			}
		}
	case "string":
		v, ok := value.(string)
		if !ok || !utf8.ValidString(v) || utf8.RuneCountInString(v) > *c.schema.MaxLength {
			return errTeamsArtifactInvalid
		}
	case "integer":
		_, ok := value.(json.Number)
		if !ok {
			return errTeamsArtifactInvalid
		}
		if _, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64); err != nil {
			return errTeamsArtifactInvalid
		}
	case "number":
		number, ok := value.(json.Number)
		if !ok || !boundedNumber(number.String()) {
			return errTeamsArtifactInvalid
		}
		n, err := number.Float64()
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return errTeamsArtifactInvalid
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return errTeamsArtifactInvalid
		}
	default:
		return errTeamsArtifactInvalid
	}
	if checkEnum && len(c.schema.Enum) > 0 {
		found := false
		canonical, err := json.Marshal(value)
		if err != nil {
			return errTeamsArtifactInvalid
		}
		for _, entry := range c.schema.Enum {
			var e any
			if teamsArtifactDecodeValue(entry, &e) != nil {
				return errTeamsArtifactInvalid
			}
			encoded, _ := json.Marshal(e)
			if teamsArtifactEqualValues(e, value, encoded, canonical) {
				found = true
				break
			}
		}
		if !found {
			return errTeamsArtifactInvalid
		}
	}
	return nil
}

// teamsArtifactDecodeValue applies the shared duplicate/shape check before a second decode
// that preserves exact numeric literals rather than rounding through float64.
func teamsArtifactDecodeValue(raw []byte, out *any) error {
	if !teams.ValidUniqueJSON(raw) {
		return errTeamsArtifactInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return d.Decode(out)
}
func teamsArtifactEqualValues(a, b any, ea, eb []byte) bool {
	an, aok := a.(json.Number)
	bn, bok := b.(json.Number)
	if aok && bok {
		if !boundedNumber(an.String()) || !boundedNumber(bn.String()) {
			return false
		}
		ar, ok := new(big.Rat).SetString(an.String())
		if !ok {
			return false
		}
		br, ok := new(big.Rat).SetString(bn.String())
		return ok && ar.Cmp(br) == 0
	}
	return bytes.Equal(ea, eb)
}

// Bound exponents before exact rational comparison to avoid allocating an
// attacker-controlled number of digits for otherwise tiny JSON literals.
func boundedNumber(s string) bool {
	if len(s) > 128 {
		return false
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		n, err := strconv.Atoi(s[i+1:])
		if err != nil || n < -400 || n > 400 {
			return false
		}
	}
	return true
}

// Exact schema keys prevent Go's case-insensitive field matching from
// silently changing an immutable artifact contract.
func teamsDecodeArtifactSchema(raw []byte, out *teamsArtifactSchema) error {
	if !teams.ValidUniqueJSON(raw) {
		return errTeamsArtifactInvalid
	}
	allowed := map[string]bool{"type": true, "properties": true, "required": true, "additionalProperties": true, "items": true, "maxItems": true, "maxLength": true, "nullable": true, "enum": true}
	if _, err := teamsStrictObject(raw, allowed); err != nil {
		return errTeamsArtifactInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errTeamsArtifactInvalid
	}
	return nil
}
