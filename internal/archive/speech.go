package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// A voice conversation's export holds recordings of the person's side only.
// To play the whole conversation, the reader asks for the agent's lines
// read aloud: the system voice reads a message when it is first played, far
// faster than the line takes to hear, and the recording is kept beside the
// library's other disposable files, never in the catalog.

var errSpeechUnsupported = errors.New("speech synthesis is not supported on this platform")

// speechSynthesize is the platform's synthesizer, and speechAvailable
// whether it has one; tests replace both.
var (
	speechSynthesize = speechNative
	speechAvailable  = speechSupported
)

// speechVoiceVersion names the voice and settings, so a change of either
// does not replay recordings made with another.
const speechVoiceVersion = "system-aac-v1"

const speechTextLimit = 40_000

var (
	speechLocks      sync.Map
	speechFence      = regexp.MustCompile("(?s)```.*?```")
	speechLink       = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	speechAddress    = regexp.MustCompile(`https?://\S+`)
	speechMarks      = regexp.MustCompile("[*_`#>|\\x{E000}-\\x{F8FF}]+")
	speechWhitespace = regexp.MustCompile(`[ \t]+`)
	// What ChatGPT's export writes for something its app drew inside a
	// reply: U+E200 type U+E202 payload U+E201 (exportMarkerText in
	// reader-model.js shows the same runs as words).
	speechExportMarker = regexp.MustCompile(`\x{E200}([^\x{E200}-\x{E202}]*)(?:\x{E202}([^\x{E201}]*))?\x{E201}`)
)

// speechMarkerWords is what is said for one of an export's markers: a named
// thing's name, and nothing for a citation, a group of images or a widget.
func speechMarkerWords(marker string) string {
	parts := speechExportMarker.FindStringSubmatch(marker)
	var named []any
	if len(parts) == 3 && parts[1] == "entity" && json.Unmarshal([]byte(parts[2]), &named) == nil && len(named) > 1 {
		return firstString(named[1])
	}
	return " "
}

// speechText is a message as it is spoken: its prose, without code blocks,
// link addresses, Markdown's marks or an export's markers.
func speechText(markdown string) string {
	text := speechExportMarker.ReplaceAllStringFunc(markdown, speechMarkerWords)
	text = speechFence.ReplaceAllString(text, " ")
	text = speechLink.ReplaceAllString(text, "$1")
	text = speechAddress.ReplaceAllString(text, " ")
	text = speechMarks.ReplaceAllString(text, " ")
	text = strings.TrimSpace(speechWhitespace.ReplaceAllString(text, " "))
	if len(text) > speechTextLimit {
		text = strings.ToValidUTF8(text[:speechTextLimit], "")
	}
	return text
}

// speechFile is the recording of text, made now if it was not kept.
func speechFile(ctx context.Context, text string) (string, error) {
	if !speechAvailable {
		return "", errSpeechUnsupported
	}
	dir, name := speechPlace(text)
	file := filepath.Join(dir, name+".m4a")
	lock, _ := speechLocks.LoadOrStore(name, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()
	if info, err := os.Stat(file); err == nil && info.Size() > 0 {
		return file, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(dir, "making-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	input, output := filepath.Join(work, "text.txt"), filepath.Join(work, "speech.m4a")
	if err := os.WriteFile(input, []byte(text), 0o600); err != nil {
		return "", err
	}
	if err := speechSynthesize(ctx, input, output); err != nil {
		return "", err
	}
	if err := os.Rename(output, file); err != nil {
		return "", err
	}
	return file, nil
}

// speechPlace is where the recording of text is kept.
func speechPlace(text string) (dir, name string) {
	sum := sha256.Sum256([]byte(speechVoiceVersion + "\x00" + text))
	return filepath.Join(pharosSupportDir(), "speech"), hex.EncodeToString(sum[:])
}

// speechDuration is a kept recording's length in milliseconds, read from
// its movie header; zero when it does not say.
func speechDuration(file string) int64 {
	data, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	at := bytes.Index(data, []byte("mvhd"))
	if at < 0 || len(data) < at+4+20 {
		return 0
	}
	header := data[at+4:]
	var scale, length uint64
	if header[0] == 1 {
		if len(header) < 32 {
			return 0
		}
		scale, length = uint64(binary.BigEndian.Uint32(header[20:])), binary.BigEndian.Uint64(header[24:])
	} else {
		scale, length = uint64(binary.BigEndian.Uint32(header[12:])), uint64(binary.BigEndian.Uint32(header[16:]))
	}
	if scale == 0 {
		return 0
	}
	return int64(length * 1000 / scale)
}

// speechWorkers is how many lines of one conversation are read at a time.
var speechWorkers = 3

var speechPreparing sync.Map

// speechFailed is when each line that could not be read aloud last failed,
// by where its recording would be kept. A line is not read again, nor
// counted as on its way, until speechRetryAfter has passed: a reader asking
// every second or two would otherwise have the same line that cannot be
// read (too long for the time a line is given, or refused by the voice)
// read again for as long as its conversation stays open.
var (
	speechFailed     sync.Map
	speechRetryAfter = 10 * time.Minute
)

var errSpeechFailed = errors.New("the line could not be read aloud a moment ago")

// speechRecentlyFailed is whether the recording kept at file failed to be
// made within speechRetryAfter.
func speechRecentlyFailed(file string) bool {
	at, failed := speechFailed.Load(file)
	return failed && time.Since(at.(time.Time)) < speechRetryAfter
}

// speechLine is the recording of text for one request or pass: made unless
// it failed a moment ago, and remembered when it fails. A request or pass
// that was itself stopped did not fail the line.
func speechLine(ctx context.Context, text string) (string, error) {
	dir, name := speechPlace(text)
	place := filepath.Join(dir, name+".m4a")
	if speechRecentlyFailed(place) {
		return "", errSpeechFailed
	}
	lineCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	file, err := speechFile(lineCtx, text)
	switch {
	case err == nil:
		speechFailed.Delete(place)
	case !errors.Is(err, errSpeechUnsupported) && ctx.Err() == nil:
		speechFailed.Store(place, time.Now())
	}
	return file, err
}

// speechConversation reports, for a conversation, which of the lines the
// source kept no recording of have been read aloud and how long each
// runs, and starts reading the rest: all of them, beginning with the line
// playback starts from, so that playing never waits on a line and the
// reader can lay the conversation out by its real lengths.
func (s *Server) speechConversation(w http.ResponseWriter, r *http.Request, conversation string) {
	rows, err := s.Catalog.reader().QueryContext(r.Context(), `SELECT m.id,m.text FROM messages m WHERE m.conversation_id=? AND m.kind='message' AND m.role IN('user','assistant')
		AND COALESCE(m.selected,1)<>0 AND NOT EXISTS(SELECT 1 FROM message_assets a WHERE a.message_id=m.id AND a.relation='recording') ORDER BY m.source_order,m.id`, conversation)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	type line struct{ id, text, file string }
	var lines []line
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			rows.Close()
			http.NotFound(w, r)
			return
		}
		if spoken := speechText(text); spoken != "" {
			dir, name := speechPlace(spoken)
			lines = append(lines, line{id, spoken, filepath.Join(dir, name+".m4a")})
		}
	}
	rows.Close()
	from := 0
	for index, item := range lines {
		if item.id == r.URL.Query().Get("from") {
			from = index
		}
	}
	state := make([]map[string]any, 0, len(lines))
	var missing []string
	for index := range lines {
		item := lines[(from+index)%len(lines)]
		if info, err := os.Stat(item.file); err == nil && info.Size() > 0 {
			state = append(state, map[string]any{"id": item.id, "ready": true, "duration_ms": speechDuration(item.file)})
		} else if speechRecentlyFailed(item.file) {
			state = append(state, map[string]any{"id": item.id, "ready": false, "failed": true})
		} else {
			state = append(state, map[string]any{"id": item.id, "ready": false})
			missing = append(missing, item.text)
		}
	}
	if len(missing) > 0 && speechAvailable {
		if _, running := speechPreparing.LoadOrStore(conversation, true); !running {
			started := s.spawn(func(ctx context.Context) {
				defer speechPreparing.Delete(conversation)
				work := make(chan string)
				var workers sync.WaitGroup
				for range speechWorkers {
					workers.Add(1)
					go func() {
						defer workers.Done()
						for text := range work {
							_, _ = speechLine(ctx, text)
						}
					}()
				}
				for _, text := range missing {
					if ctx.Err() != nil {
						break
					}
					work <- text
				}
				close(work)
				workers.Wait()
			})
			if !started {
				speechPreparing.Delete(conversation)
			}
		}
	}
	writeJSON(w, map[string]any{"lines": state, "pending": len(missing), "unsupported": !speechAvailable}, http.StatusOK)
}

// speech serves a stored message read aloud. The URL names a message, never
// text: a request cannot have arbitrary words synthesized.
func (s *Server) speech(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/speech/")
	if conversation, ok := strings.CutPrefix(id, "conversation/"); ok {
		s.speechConversation(w, r, conversation)
		return
	}
	var text string
	if err := s.Catalog.reader().QueryRowContext(r.Context(), "SELECT text FROM messages WHERE id=? AND kind='message'", id).Scan(&text); err != nil {
		http.NotFound(w, r)
		return
	}
	spoken := speechText(text)
	if spoken == "" {
		http.NotFound(w, r)
		return
	}
	file, err := speechLine(r.Context(), spoken)
	if errors.Is(err, errSpeechUnsupported) {
		writeJSON(w, map[string]any{"error": "speech synthesis is not available on this computer", "unsupported": true}, http.StatusNotImplemented)
		return
	}
	if err != nil {
		writeJSON(w, map[string]any{"error": "the line could not be read aloud"}, http.StatusInternalServerError)
		return
	}
	input, err := os.Open(file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer input.Close()
	w.Header().Set("Content-Type", "audio/mp4")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "", time.Time{}, input)
}
