package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"
)

// The catalog may live on an external drive that is unplugged mid-write, so
// every pooled connection runs with:
//
//   - synchronous=FULL with fullfsync: each commit ends in F_FULLFSYNC, which
//     makes the drive flush its write cache (a plain fsync on macOS does not),
//     so a commit that has returned survives the drive losing power. This costs
//     ~0.4 ms per commit on a Thunderbolt SSD and ~4.5 ms on the internal one.
//   - checkpoint_fullfsync: checkpoints flush the drive before the WAL is
//     reused. Without it a yank can corrupt the database rather than only lose
//     recent commits; modernc's build leaves it off.
//   - journal_size_limit: SQLite never shrinks the -wal file by itself. Once
//     the log restarts, the next commit truncates it to this size, which is far
//     above the ~4 MB between automatic checkpoints, so routine restarts never
//     truncate and regrow the file.
const (
	walSizeLimit  = 64 << 20
	busyTimeoutMS = 5000
	walRetryDelay = 30 * time.Second
)

// checkpointWaitMS bounds how long Checkpoint waits for a connection and for
// readers. Only tests shorten it.
var checkpointWaitMS = 1000

// catalogPragmas are the durability settings above.
type catalogPragmas struct {
	synchronous         string // FULL
	fullfsync           bool
	checkpointFullfsync bool
}

// durableCatalog is what every catalog connection runs with.
var durableCatalog = catalogPragmas{synchronous: "FULL", fullfsync: true, checkpointFullfsync: true}

// catalogDurability is durableCatalog except in tests, whose many tiny
// commits would each wait ~4.5 ms for the internal SSD to flush. The tests of
// durability itself restore durableCatalog.
var catalogDurability = durableCatalog

func catalogDSN(path string) string {
	pragmas := catalogDurability
	fullfsync, checkpointFullfsync := 0, 0
	if pragmas.fullfsync {
		fullfsync = 1
	}
	if pragmas.checkpointFullfsync {
		checkpointFullfsync = 1
	}
	return (&url.URL{Scheme: "file", Path: path}).String() + fmt.Sprintf("?_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)"+
		"&_pragma=synchronous(%s)&_pragma=fullfsync(%d)&_pragma=checkpoint_fullfsync(%d)&_pragma=journal_size_limit(%d)"+
		"&_pragma=cache_size(-32768)&_pragma=mmap_size(268435456)",
		busyTimeoutMS, pragmas.synchronous, fullfsync, checkpointFullfsync, walSizeLimit)
}

// Request reads use a separate read-only pool (see Catalog.reader), so the
// connections background work keeps checked out during a sync (the tool
// rollup, the authorship scan, the findings snapshot) cannot leave a page
// waiting for one. In WAL a reader never waits on the writer; it reads the last
// commit. The page cache is per connection, so it is smaller than the writer
// pool's; the shared mmap does most of the caching.
const readerConns = 8

func readerDSN(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String() + fmt.Sprintf("?mode=ro&_pragma=busy_timeout(%d)"+
		"&_pragma=cache_size(-16384)&_pragma=mmap_size(268435456)", busyTimeoutMS)
}

// openReader opens c's read pool. It is lazy: no connection opens until a
// read, by which time the writer pool has put the catalog in WAL mode.
func (c *Catalog) openReader() error {
	db, err := sql.Open("sqlite", readerDSN(c.Path))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(readerConns)
	db.SetMaxIdleConns(readerConns)
	c.reads = db
	return nil
}

// reader is the pool for reads that serve requests. A catalog opened without
// one (a findings run, an audit's reference copy) reads through DB. Writes
// through it fail, and so would a read that needs this connection's own
// uncommitted writes; TEMP tables still work.
func (c *Catalog) reader() *sql.DB {
	if c.reads != nil {
		return c.reads
	}
	return c.DB
}

var errCheckpointBusy = errors.New("catalog checkpoint incomplete: the WAL is still in use")

// Checkpoint copies the WAL into the database and truncates it to zero bytes.
// It is best effort: rather than hold up writers, it gives up after about a
// second if a reader of an older snapshot or another writer is still active.
// Readers are never blocked.
func (c *Catalog) Checkpoint() error {
	cliWriteCheck("checkpoint")
	// Don't wait indefinitely for a pooled connection either, e.g. in Close
	// while long queries still hold every connection.
	acquire, cancel := context.WithTimeout(context.Background(), time.Duration(checkpointWaitMS)*time.Millisecond)
	conn, err := c.DB.Conn(acquire)
	cancel()
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx := context.Background()
	// A passive pass copies what it can without the write lock, so the
	// truncating pass below holds writers off only briefly.
	if _, err := conn.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", checkpointWaitMS)); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMS))
	var busy, logFrames, checkpointed int
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return errCheckpointBusy
	}
	return nil
}

// walBound lets one goroutine at a time checkpoint an oversized WAL and backs
// off after readers kept a checkpoint from finishing.
type walBound struct {
	mu      sync.Mutex
	retryAt time.Time
}

// boundWAL checkpoints once the WAL outgrows limit. An automatic checkpoint
// cannot restart the log while any reader still uses it, and the Library's
// overlapping reads during a sync rarely leave such a gap, so without this the
// log grows by everything a sync writes: reads slow down searching it, and an
// unclean exit leaves all of it to replay on the next open. Syncs call this
// between transactions, where the writer does not contend for its own lock.
func (c *Catalog) boundWAL(limit int64) {
	if info, err := os.Stat(c.Path + "-wal"); err != nil || info.Size() <= limit {
		return
	}
	if !c.wal.mu.TryLock() {
		return
	}
	defer c.wal.mu.Unlock()
	if time.Now().Before(c.wal.retryAt) {
		return
	}
	// Only a check that runs a checkpoint is an operation (§3.4).
	_, op := c.beginOp(context.Background(), OperationSpec{Kind: "maintenance.checkpoint", Variant: "bound", Trigger: triggerSchedule})
	err := c.Checkpoint()
	if err != nil {
		c.wal.retryAt = time.Now().Add(walRetryDelay)
	}
	if errors.Is(err, errCheckpointBusy) {
		op.Skip("catalog-busy")
		return
	}
	op.Finish(err)
}

// keepWALSmall bounds the WAL between syncs and while other processes write.
func (c *Catalog) keepWALSmall(ctx context.Context, every time.Duration, limit int64) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.boundWAL(limit)
		}
	}
}
