package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ChatGPT shows a reply's web images from their addresses, and its export
// holds only those addresses. Pharos fetches each image once, after the
// conversations that name it are indexed, and keeps it by its address, so
// that reading never touches the network and reindexing a conversation
// keeps what was fetched. An address that does not answer with an image is
// remembered as such and shown as not fetched.

const remoteAssetLimit = 8 << 20

// remoteAssetBatch bounds one pass, and remoteAssetAttempts how many passes
// try an address that did not answer with an image.
var remoteAssetBatch, remoteAssetAttempts = 200, 3

// remoteAssetBudget is how long one pass may take: it runs at the end of a
// sync, which is not finished until it returns.
var remoteAssetBudget = 30 * time.Second

// remoteAssetRefusal is an image host's own answer that there is no image
// to keep: an error status, something that is not an image, or one too
// large. Asking again will not change it, unlike a network that did not
// reach the host.
type remoteAssetRefusal string

func (r remoteAssetRefusal) Error() string { return string(r) }

// remoteAssetClient reaches public HTTPS hosts only, as the favicon client
// does, and follows a few redirects, each checked the same way.
var remoteAssetClient = &http.Client{
	Timeout:   10 * time.Second,
	Transport: markdownFaviconClient.Transport,
	CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 || request.URL.Scheme != "https" {
			return http.ErrUseLastResponse
		}
		return nil
	},
}

// remoteAssetsEnabled is whether a pass may use the network; tests leave it
// off unless they point the client at their own server.
var remoteAssetsEnabled = true

func remoteAssetAddress(raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", false
	}
	return parsed.String(), true
}

// remoteAssetFetch reads one image, or says why there is none to keep.
func remoteAssetFetch(ctx context.Context, address string) ([]byte, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Accept", "image/*")
	response, err := remoteAssetClient.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", remoteAssetRefusal(fmt.Sprintf("HTTP %d", response.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, remoteAssetLimit+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > remoteAssetLimit {
		return nil, "", remoteAssetRefusal(fmt.Sprintf("larger than %d MB", remoteAssetLimit>>20))
	}
	kind := http.DetectContentType(data)
	if kind != "image/png" && kind != "image/jpeg" && kind != "image/gif" && kind != "image/webp" {
		return nil, "", remoteAssetRefusal(fmt.Sprintf("not an image (%s)", kind))
	}
	return data, kind, nil
}

// fetchRemoteAssets fetches the web images indexed conversations name and
// no earlier pass settled, those never tried first, for at most
// remoteAssetBudget. Nothing is held open while the network is used.
//
// Only a host's own refusal uses up one of an address's attempts. A request
// that reached no host says nothing about the image when nothing else was
// reached either, as on a computer that is offline: those are left as they
// were, and the pass stops after a few in a row. Once any host has answered
// in the pass, a host that could not be reached counts as having failed.
func (c *Catalog) fetchRemoteAssets(ctx context.Context) (int, error) {
	if !remoteAssetsEnabled {
		return 0, nil
	}
	rows, err := c.reader().QueryContext(ctx, `SELECT url FROM (SELECT DISTINCT json_extract(a.details,'$.url') url,COALESCE(r.fetched_at,'') tried FROM message_assets a
		LEFT JOIN remote_assets r ON r.url=json_extract(a.details,'$.url')
		WHERE a.availability=? AND json_extract(a.details,'$.url') IS NOT NULL AND (r.url IS NULL OR r.content IS NULL AND r.attempts<?)) ORDER BY tried,url LIMIT ?`, assetRemote, remoteAssetAttempts, remoteAssetBatch)
	if err != nil {
		return 0, err
	}
	var addresses []string
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			rows.Close()
			return 0, err
		}
		addresses = append(addresses, address)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	record := func(stored, kind string, data []byte, failure string, attempts int) error {
		return c.writeTransaction(ctx, "remote asset", func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO remote_assets(url,mime_type,content,fetched_at,error,attempts) VALUES(?,?,?,?,?,?)
				ON CONFLICT(url) DO UPDATE SET mime_type=excluded.mime_type,content=excluded.content,fetched_at=excluded.fetched_at,error=excluded.error,attempts=attempts+excluded.attempts`,
				stored, nullIfEmpty(kind), data, now(), nullIfEmpty(strings.ToValidUTF8(failure, "")), attempts)
			return err
		})
	}
	pass, cancel := context.WithTimeout(ctx, remoteAssetBudget)
	defer cancel()
	fetched, answered := 0, false
	// Addresses no host was reached for, and why.
	unreached := map[string]string{}
	for _, stored := range addresses {
		if pass.Err() != nil || !answered && len(unreached) >= 3 {
			break
		}
		address, ok := remoteAssetAddress(stored)
		if !ok {
			// Nothing a later pass could fetch.
			if err := record(stored, "", nil, "not a public HTTPS address", remoteAssetAttempts); err != nil {
				return fetched, err
			}
			continue
		}
		data, kind, err := remoteAssetFetch(pass, address)
		var refusal remoteAssetRefusal
		switch {
		case err == nil:
			answered = true
			fetched++
			err = record(stored, kind, data, "", 1)
		case errors.As(err, &refusal):
			answered = true
			err = record(stored, "", nil, refusal.Error(), 1)
		case ctx.Err() != nil:
			return fetched, ctx.Err()
		case pass.Err() != nil:
			// The pass ran out of time on this one: it is no one's failure.
			err = nil
		default:
			unreached[stored], err = err.Error(), nil
		}
		if err != nil {
			return fetched, err
		}
	}
	for stored, failure := range unreached {
		attempts := 0
		if answered {
			attempts = 1
		}
		if err := record(stored, "", nil, failure, attempts); err != nil {
			return fetched, err
		}
	}
	return fetched, nil
}

// fetchedRemoteAssets is which of a conversation's web images are kept.
func fetchedRemoteAssets(ctx context.Context, db *sql.DB, conversationID string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT r.url FROM remote_assets r WHERE r.content IS NOT NULL AND r.url IN
		(SELECT json_extract(details,'$.url') FROM message_assets WHERE conversation_id=? AND availability=?)`, conversationID, assetRemote)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	kept := map[string]bool{}
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			return nil, err
		}
		kept[address] = true
	}
	return kept, rows.Err()
}
