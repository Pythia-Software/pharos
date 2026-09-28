package archive

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// Antigravity records each model request's token usage, and the model that
// served it, in the conversation's database, conversations/<id>.db. Its
// gen_metadata table has a row per request, a protobuf
// exa.cortex_pb.CortexStepGeneratorMetadata:
//
//	1 chat_model   ChatModelMetadata
//	    4  usage          exa.codeium_common_pb.ModelUsageStats
//	    5  model_cost     float (not seen set)
//	    13 credit_cost    int32 (not seen set)
//	    17 retry_infos    repeated RetryInfo, each repeating a usage
//	    19 response_model string, such as "gemini-3.8-flash"
//	2 step_indices repeated uint32: the transcript steps it generated
//	4 execution_id string
//
// ModelUsageStats counts input without cached input, like Anthropic's usage,
// and output with thinking: output_tokens = thinking_output_tokens +
// response_output_tokens. The field numbers are those of the descriptors in
// Antigravity's language server. Each step's metadata repeats its request's
// usage; only gen_metadata is read, and only a request's final usage counts,
// since its retry_infos repeat it.

// antigravityUsage names ModelUsageStats fields by number.
var antigravityUsage = map[int]string{1: "model_enum", 2: "input_tokens", 3: "output_tokens", 4: "cache_write_tokens", 5: "cache_read_tokens", 6: "api_provider",
	7: "message_id", 9: "thinking_output_tokens", 10: "response_output_tokens", 11: "response_id", 12: "provider_assigned_message_id", 13: "service_tier"}

// antigravityRequest is one model request, keyed by the step it answered.
type antigravityRequest struct {
	index int64
	model string
	// usage is ModelUsageStats with named fields, as retained evidence.
	usage map[string]any
}

// tokenUsage is the request's usage in the shape token accounting reads.
func (r antigravityRequest) tokenUsage() map[string]any {
	count := func(name string) int64 { value, _ := r.usage[name].(int64); return value }
	return map[string]any{"input_tokens": count("input_tokens"), "cache_read_input_tokens": count("cache_read_tokens"), "cache_creation_input_tokens": count("cache_write_tokens"),
		"output_tokens": count("output_tokens"), "reasoning_output_tokens": count("thinking_output_tokens")}
}

func (a *antigravityAdapter) requestsDatabase(id string) string {
	return filepath.Join(a.config.Path, "conversations", id+".db")
}

// requests reads a conversation's model requests by the index of the first
// step each generated. A missing database has none.
func (a *antigravityAdapter) requests(id string) (map[int64]antigravityRequest, error) {
	path := a.requestsDatabase(id)
	if _, err := os.Stat(path); err != nil {
		return nil, nil
	}
	db, err := openReadOnlySQLite(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT idx, data FROM gen_metadata ORDER BY idx")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer rows.Close()
	requests := map[int64]antigravityRequest{}
	for rows.Next() {
		var index int64
		var data []byte
		if err := rows.Scan(&index, &data); err != nil {
			return nil, err
		}
		generator := protoFields(data)
		chat := protoFields(protoBytes(generator[1]))
		steps := protoPacked(generator[2])
		if len(steps) == 0 {
			continue
		}
		stats := protoFields(protoBytes(chat[4]))
		if len(stats) == 0 {
			continue
		}
		usage := map[string]any{}
		for number, name := range antigravityUsage {
			switch value := protoLast(stats[number]).(type) {
			case uint64:
				usage[name] = int64(value)
			case []byte:
				usage[name] = string(value)
			}
		}
		requests[int64(steps[0])] = antigravityRequest{index: index, model: string(protoBytes(chat[19])), usage: usage}
	}
	return requests, rows.Err()
}

// protoFields decodes one protobuf message's fields by number: varints as
// uint64, length-delimited values as []byte, fixed-width ones as their bits.
// Decoding stops at the first malformed field.
func protoFields(data []byte) map[int][]any {
	fields := map[int][]any{}
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 || key>>3 == 0 || key>>3 > math.MaxInt32 {
			return fields
		}
		data = data[n:]
		number := int(key >> 3)
		switch key & 7 {
		case 0:
			value, n := binary.Uvarint(data)
			if n <= 0 {
				return fields
			}
			fields[number], data = append(fields[number], value), data[n:]
		case 1:
			if len(data) < 8 {
				return fields
			}
			fields[number], data = append(fields[number], binary.LittleEndian.Uint64(data)), data[8:]
		case 2:
			length, n := binary.Uvarint(data)
			if n <= 0 || uint64(len(data)-n) < length {
				return fields
			}
			fields[number], data = append(fields[number], data[n:n+int(length)]), data[n+int(length):]
		case 5:
			if len(data) < 4 {
				return fields
			}
			fields[number], data = append(fields[number], uint64(binary.LittleEndian.Uint32(data))), data[4:]
		default:
			return fields
		}
	}
	return fields
}

// protoLast is a field's last value: the one a singular field takes.
func protoLast(values []any) any {
	if len(values) == 0 {
		return nil
	}
	return values[len(values)-1]
}

func protoBytes(values []any) []byte {
	value, _ := protoLast(values).([]byte)
	return value
}

// protoPacked reads a repeated varint field, packed or not.
func protoPacked(values []any) []uint64 {
	out := []uint64{}
	for _, value := range values {
		switch item := value.(type) {
		case uint64:
			out = append(out, item)
		case []byte:
			for len(item) > 0 {
				number, n := binary.Uvarint(item)
				if n <= 0 {
					return out
				}
				out, item = append(out, number), item[n:]
			}
		}
	}
	return out
}
