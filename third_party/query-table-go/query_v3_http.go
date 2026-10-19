package querytable

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

func DecodeQueryRequestV3(data []byte) (QueryRequestV3, error) {
	var q QueryRequestV3
	if len(data) > 2<<20 {
		return q, diagnostic("resource_limit", "query request exceeds byte budget")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&q); err != nil {
		return q, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return q, diagnostic("query", "expected one query object")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return q, diagnostic("query", "expected a query object")
	}
	if clock, ok := object["now"]; !ok || bytes.Equal(bytes.TrimSpace(clock), []byte("null")) {
		return q, diagnostic("datetime_value", "one evaluation clock is required")
	}
	for _, name := range []string{"source", "result"} {
		var stage map[string]json.RawMessage
		if json.Unmarshal(object[name], &stage) != nil || stage == nil {
			return q, diagnostic("query", "source and result must be query objects")
		}
		for key := range stage {
			if !containsString([]string{"select", "s", "c", "where", "w", "orderBy", "o", "limit", "l", "offset", "f"}, key) {
				return q, diagnostic("query", "unsupported query stage property: "+key)
			}
		}
	}
	if err := q.Validate(); err != nil {
		return q, err
	}
	return q, nil
}

// Authorization chooses the complete dataset/options from trusted host state,
// including read access and same-origin/CSRF policy. Nil authorization denies.
// Permanent query snapshots are distinct from retained data snapshot bindings.
func NewQueryV3Handler(db *sql.DB, authorize func(*http.Request) (QueryDatasetV3, PlanOptions, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", 405)
			return
		}
		if db == nil || authorize == nil {
			http.Error(w, "forbidden", 403)
			return
		}
		dataset, options, err := authorize(r)
		if err != nil {
			http.Error(w, "forbidden", 403)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
		if err != nil {
			http.Error(w, "query request exceeds byte budget", 400)
			return
		}
		q, err := DecodeQueryRequestV3(body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		result, err := dataset.Execute(ctx, db, q, options)
		if err != nil {
			var issue *PlanDiagnostic
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				http.Error(w, "query timed out", http.StatusGatewayTimeout)
			case errors.Is(err, context.Canceled):
				http.Error(w, "query cancelled", http.StatusRequestTimeout)
			case errors.As(err, &issue):
				http.Error(w, issue.Error(), http.StatusBadRequest)
			default:
				http.Error(w, "could not execute query", http.StatusInternalServerError)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}
