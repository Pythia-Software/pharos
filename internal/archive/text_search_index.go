package archive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"
)

func (c *Catalog) textSearchReady(ctx context.Context) (bool, error) {
	var version string
	err := c.reader().QueryRowContext(ctx, `SELECT value FROM meta WHERE key='text_search_documents_version'`).Scan(&version)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return version == "1", err
}
func (c *Catalog) indexTextSearchBatch(ctx context.Context) (bool, error) {
	tx, finish, err := c.beginTrackedWrite(ctx, "text-search-index")
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(); finish() }()
	var version string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='text_search_documents_version'`).Scan(&version); err == nil {
		return false, nil
	} else if err != sql.ErrNoRows {
		return false, err
	}
	var cursor, last int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta WHERE key='text_search_documents_cursor'),0)`).Scan(&cursor); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM (SELECT rowid FROM messages WHERE rowid>? ORDER BY rowid LIMIT 2000)`, cursor).Scan(&last); err != nil {
		return false, err
	}
	if last == 0 {
		if _, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES('text_search_documents_version','1')`); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO text_search_documents(message_rowid,message_id,conversation_id,surface,byte_length)
 SELECT rowid,id,conversation_id,CASE WHEN kind IN ('thinking','reasoning','analysis') THEN 'reasoning' ELSE role END,length(CAST(text AS BLOB))
 FROM messages WHERE rowid>? AND rowid<=? AND kind IN ('message','thinking','reasoning','analysis')
 AND (role IN ('user','assistant') OR kind IN ('thinking','reasoning','analysis'))`, cursor, last)
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES('text_search_documents_cursor',?)`, fmt.Sprint(last)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
func (c *Catalog) maintainTextSearch(ctx context.Context) {
	// One operation for the whole build, forgotten if there was nothing to build.
	pass := c.beginPass(ctx, "maintenance.search.text", triggerVersion)
	defer func() { pass.end(nil) }()
	for {
		var more bool
		var err error
		pass.runIO(func() { more, err = c.indexTextSearchBatch(ctx) })
		if more {
			pass.worked(1)
		}
		delay := 20 * time.Millisecond
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintf(os.Stderr, "Text search index: %v\n", err)
			delay = 5 * time.Second
		} else if !more {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
