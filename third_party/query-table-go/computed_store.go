package querytable

// Reusable computed definitions are metadata, never SQL expressions. Formula
// execution requires the separately negotiated server profile. This optional PostgreSQL repository uses
// database/sql; applications supply their driver, connection, and authorization.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const ComputedColumnsDDL = `CREATE TABLE IF NOT EXISTS query_table_computed_columns (
 scope text NOT NULL,
 dataset text NOT NULL,
 id text NOT NULL,
 label text NOT NULL,
 language text NOT NULL,
 language_version integer NOT NULL,
 source text NOT NULL,
 revision bigint NOT NULL DEFAULT 1,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (scope, dataset, id)
)`

type ComputedExpression struct {
	Language string `json:"language"`
	Version  int    `json:"version"`
	Source   string `json:"source"`
}
type ComputedColumn struct {
	ID         string             `json:"id"`
	Label      string             `json:"label"`
	Expression ComputedExpression `json:"expression"`
	Revision   string             `json:"revision"`
}
type SaveComputedColumnRequest struct {
	Column           ComputedColumn `json:"column"`
	ExpectedRevision *string        `json:"expectedRevision"`
}

var ErrComputedConflict = errors.New("computed definition changed; reload before saving")
var computedID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// Match JavaScript String.length, including surrogate pairs for non-BMP text.
func utf16Length(value string) int {
	length := 0
	for _, r := range value {
		length++
		if r > 0xffff {
			length++
		}
	}
	return length
}

func validateComputed(c ComputedColumn) error {
	if !computedID.MatchString(c.ID) || strings.TrimSpace(c.Label) == "" || utf16Length(c.Label) > 200 || c.Expression.Language != "qt-expr" || c.Expression.Version != 1 || strings.TrimSpace(c.Expression.Source) == "" || utf16Length(c.Expression.Source) > 10000 {
		return errors.New("invalid computed column definition")
	}
	return nil
}

type ComputedColumnRepository interface {
	List(ctx context.Context, scope, dataset string) ([]ComputedColumn, error)
	Save(ctx context.Context, scope, dataset string, request SaveComputedColumnRequest) (ComputedColumn, error)
}
type SQLComputedColumnStore struct{ DB *sql.DB }

func (s SQLComputedColumnStore) List(ctx context.Context, scope, dataset string) ([]ComputedColumn, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,label,language,language_version,source,revision FROM query_table_computed_columns WHERE scope=$1 AND dataset=$2 ORDER BY label,id`, scope, dataset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ComputedColumn{}
	for rows.Next() {
		var c ComputedColumn
		var revision int64
		if err = rows.Scan(&c.ID, &c.Label, &c.Expression.Language, &c.Expression.Version, &c.Expression.Source, &revision); err != nil {
			return nil, err
		}
		c.Revision = strconv.FormatInt(revision, 10)
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s SQLComputedColumnStore) Save(ctx context.Context, scope, dataset string, request SaveComputedColumnRequest) (ComputedColumn, error) {
	c := request.Column
	if err := validateComputed(c); err != nil {
		return ComputedColumn{}, err
	}
	var row *sql.Row
	if request.ExpectedRevision == nil {
		row = s.DB.QueryRowContext(ctx, `INSERT INTO query_table_computed_columns (scope,dataset,id,label,language,language_version,source) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING RETURNING revision`, scope, dataset, c.ID, c.Label, c.Expression.Language, c.Expression.Version, c.Expression.Source)
	} else {
		revision, err := strconv.ParseInt(*request.ExpectedRevision, 10, 64)
		if err != nil || revision < 1 {
			return ComputedColumn{}, ErrComputedConflict
		}
		row = s.DB.QueryRowContext(ctx, `UPDATE query_table_computed_columns SET label=$4,language=$5,language_version=$6,source=$7,revision=revision+1,updated_at=now() WHERE scope=$1 AND dataset=$2 AND id=$3 AND revision=$8 RETURNING revision`, scope, dataset, c.ID, c.Label, c.Expression.Language, c.Expression.Version, c.Expression.Source, revision)
	}
	var revision int64
	if err := row.Scan(&revision); errors.Is(err, sql.ErrNoRows) {
		return ComputedColumn{}, ErrComputedConflict
	} else if err != nil {
		return ComputedColumn{}, err
	}
	c.Revision = strconv.FormatInt(revision, 10)
	return c, nil
}

// NewComputedColumnsHandler implements the TypeScript httpComputedColumnStore
// protocol. Authorize MUST validate the authenticated caller's dataset access,
// write permission and (for cookie-authenticated writes) CSRF protection. Return
// a stable tenant/user scope; it must never come directly from request input.
// Empty scope or nil authorization always denies access.
func NewComputedColumnsHandler(repo ComputedColumnRepository, authorize func(*http.Request, string, bool) (string, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodPut {
			w.Header().Set("Allow", "GET, PUT")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		dataset := r.URL.Query().Get("dataset")
		if strings.TrimSpace(dataset) == "" || len(dataset) > 256 {
			http.Error(w, "invalid dataset", http.StatusBadRequest)
			return
		}
		if authorize == nil || repo == nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		scope, err := authorize(r, dataset, r.Method == http.MethodPut)
		if err != nil || scope == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodGet {
			columns, err := repo.List(r.Context(), scope, dataset)
			if err != nil {
				http.Error(w, "could not load definitions", http.StatusInternalServerError)
				return
			}
			if columns == nil {
				columns = []ComputedColumn{}
			}
			_ = json.NewEncoder(w).Encode(columns)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
		var request SaveComputedColumnRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := validateComputed(request.Column); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		column, err := repo.Save(r.Context(), scope, dataset, request)
		if errors.Is(err, ErrComputedConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		var invalid *PlanDiagnostic
		if errors.As(err, &invalid) {
			http.Error(w, invalid.Error(), http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "could not save definition", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(column)
	})
}
