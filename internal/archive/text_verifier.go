package archive

import (
	"context"
	"database/sql"
	"strings"
)

// textVerifier uses the catalog's exact unicode61 tokenizer on bounded original
// text. This matters for NOT, authorship spans, accents, and ordered proximity:
// Go's Unicode word regex is not an equivalent tokenizer.
type textVerifier struct {
	db    *sql.DB
	terms map[*textQuery][]string
}

func newTextVerifier(ctx context.Context) (*textVerifier, error) {
	db, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	if _, e = db.ExecContext(ctx, `CREATE VIRTUAL TABLE verify_text USING fts5(text,tokenize='unicode61');
 CREATE VIRTUAL TABLE verify_vocab USING fts5vocab(verify_text,'instance');
 CREATE VIRTUAL TABLE query_text USING fts5(text,tokenize='unicode61');
 CREATE VIRTUAL TABLE query_vocab USING fts5vocab(query_text,'instance');`); e != nil {
		db.Close()
		return nil, e
	}
	return &textVerifier{db: db, terms: map[*textQuery][]string{}}, nil
}
func (v *textVerifier) match(ctx context.Context, node *textQuery, body string, caseSensitive, separators bool) (int, int, bool, error) {
	if _, e := v.db.ExecContext(ctx, `DELETE FROM verify_text;`); e != nil {
		return 0, 0, false, e
	}
	if _, e := v.db.ExecContext(ctx, `INSERT INTO verify_text(text) VALUES(?)`, body); e != nil {
		return 0, 0, false, e
	}
	return v.eval(ctx, node, body, caseSensitive, separators)
}
func (v *textVerifier) eval(ctx context.Context, n *textQuery, body string, caseSensitive, separators bool) (int, int, bool, error) {
	if n.Op == "and" || n.Op == "or" || n.Op == "not" {
		lo, hi, yes, e := v.eval(ctx, n.Children[0], body, caseSensitive, separators)
		if e != nil {
			return 0, 0, false, e
		}
		if n.Op == "not" {
			_, _, exclude, e := v.eval(ctx, n.Children[1], body, caseSensitive, separators)
			return lo, hi, yes && !exclude, e
		}
		for _, child := range n.Children[1:] {
			a, b, match, e := v.eval(ctx, child, body, caseSensitive, separators)
			if e != nil {
				return 0, 0, false, e
			}
			if n.Op == "and" {
				yes = yes && match
			} else {
				if !yes && match {
					lo, hi = a, b
				}
				yes = yes || match
			}
		}
		return lo, hi, yes, nil
	}
	if n.Op == "pre" {
		return v.ordered(ctx, n, body, caseSensitive, separators)
	}
	var count int
	if e := v.db.QueryRowContext(ctx, `SELECT count(*) FROM verify_text WHERE verify_text MATCH ?`, n.fts(false)).Scan(&count); e != nil {
		return 0, 0, false, e
	}
	if count == 0 {
		return 0, 0, false, nil
	}
	if caseSensitive || (separators && (n.Op == "phrase" || n.Op == "near")) {
		lo, hi, yes := n.strictMatch(body, caseSensitive, separators)
		return lo, hi, yes, nil
	}
	lo, hi := lexicalExcerpt(body, n)
	return lo, hi, true, nil
}
func (v *textVerifier) queryTerms(ctx context.Context, n *textQuery) ([]string, error) {
	if terms, ok := v.terms[n]; ok {
		return terms, nil
	}
	if _, e := v.db.ExecContext(ctx, `DELETE FROM query_text`); e != nil {
		return nil, e
	}
	if _, e := v.db.ExecContext(ctx, `INSERT INTO query_text(text) VALUES(?)`, n.Text); e != nil {
		return nil, e
	}
	rows, e := v.db.QueryContext(ctx, `SELECT term FROM query_vocab ORDER BY offset`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	terms := []string{}
	for rows.Next() {
		var term string
		if e = rows.Scan(&term); e != nil {
			return nil, e
		}
		terms = append(terms, term)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	v.terms[n] = terms
	return terms, nil
}
func (v *textVerifier) ordered(ctx context.Context, n *textQuery, body string, caseSensitive, separators bool) (int, int, bool, error) {
	left, e := v.queryTerms(ctx, n.Children[0])
	if e != nil {
		return 0, 0, false, e
	}
	right, e := v.queryTerms(ctx, n.Children[1])
	if e != nil {
		return 0, 0, false, e
	}
	rows, e := v.db.QueryContext(ctx, `SELECT term FROM verify_vocab ORDER BY offset`)
	if e != nil {
		return 0, 0, false, e
	}
	tokens := []string{}
	for rows.Next() {
		var token string
		if e = rows.Scan(&token); e != nil {
			rows.Close()
			return 0, 0, false, e
		}
		tokens = append(tokens, token)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, 0, false, e
	}
	locations := func(terms []string, prefix bool) []int {
		found := []int{}
		for i := 0; i+len(terms) <= len(tokens); i++ {
			ok := true
			for j, term := range terms {
				if prefix && j == len(terms)-1 {
					ok = ok && strings.HasPrefix(tokens[i+j], term)
				} else {
					ok = ok && tokens[i+j] == term
				}
			}
			if ok {
				found = append(found, i)
			}
		}
		return found
	}
	a, b := locations(left, n.Children[0].Op == "prefix"), locations(right, n.Children[1].Op == "prefix")
	for i, j := 0, 0; i < len(a) && j < len(b); {
		gap := b[j] - (a[i] + len(left))
		if gap < 0 {
			j++
			continue
		}
		if gap > n.Distance {
			i++
			continue
		}
		if caseSensitive || separators {
			lo, hi, yes := n.strictMatch(body, caseSensitive, separators)
			return lo, hi, yes, nil
		}
		lo, hi := lexicalExcerpt(body, n)
		return lo, hi, true, nil
	}
	return 0, 0, false, nil
}
func (n *textQuery) needsOrderedVerifier() bool {
	if n.Op == "pre" {
		return true
	}
	for _, child := range n.Children {
		if child.needsOrderedVerifier() {
			return true
		}
	}
	return false
}
