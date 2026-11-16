package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// pushChunking names how capture-push cuts members: the gear-v1
// content-defined chunking of the Claude export store (claudeSourceChunks),
// streamed. A transcript grows by appending, so all but its last chunk keep
// their content, and their names, from one push to the next.
const pushChunking = "gear-v1"

const (
	gearMinChunk = 32 << 10
	gearMaxChunk = 256 << 10
	gearMask     = 0xffff
)

var gearTable = sync.OnceValue(func() *[256]uint64 {
	var gear [256]uint64
	seed := uint64(0x706861726f732d31)
	for i := range gear {
		seed += 0x9e3779b97f4a7c15
		value := seed
		value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
		value = (value ^ (value >> 27)) * 0x94d049bb133111eb
		gear[i] = value ^ (value >> 31)
	}
	return &gear
})

// pushChunk is one piece of a member: the SHA-256 of its bytes, in hex, and
// their count.
type pushChunk struct {
	SHA256 string
	Size   int64
}

// gearChunker cuts the bytes written to it exactly where claudeSourceChunks
// would cut them as one slice, handing each chunk to emit as it completes.
// It holds at most one chunk. The slice emit receives is reused.
type gearChunker struct {
	emit    func(chunk pushChunk, data []byte) error
	buffer  []byte
	rolling uint64
	whole   interface{ Write([]byte) (int, error) }
	chunks  []pushChunk
	size    int64
	sum     string
}

func newGearChunker(emit func(chunk pushChunk, data []byte) error) *gearChunker {
	return &gearChunker{emit: emit, buffer: make([]byte, 0, gearMaxChunk), whole: sha256.New()}
}

func (c *gearChunker) Write(data []byte) (int, error) {
	gear := gearTable()
	c.whole.Write(data)
	for _, value := range data {
		c.buffer = append(c.buffer, value)
		c.rolling = (c.rolling << 1) + gear[value]
		if length := len(c.buffer); length >= gearMinChunk && (c.rolling&gearMask == 0 || length >= gearMaxChunk) {
			if err := c.cut(); err != nil {
				return 0, err
			}
		}
	}
	return len(data), nil
}

func (c *gearChunker) cut() error {
	sum := sha256.Sum256(c.buffer)
	chunk := pushChunk{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(c.buffer))}
	if err := c.emit(chunk, c.buffer); err != nil {
		return err
	}
	c.chunks = append(c.chunks, chunk)
	c.size += chunk.Size
	c.buffer, c.rolling = c.buffer[:0], 0
	return nil
}

// Close emits what remains and fixes the whole stream's digest.
func (c *gearChunker) Close() error {
	if len(c.buffer) > 0 {
		if err := c.cut(); err != nil {
			return err
		}
	}
	c.sum = hex.EncodeToString(c.whole.(interface{ Sum([]byte) []byte }).Sum(nil))
	return nil
}
