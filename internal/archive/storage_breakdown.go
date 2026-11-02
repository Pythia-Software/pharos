package archive

import (
	"context"
	"math"
	"sort"
	"time"
)

// The Settings storage breakdown says where the library's space goes: the
// captured originals (each Mac's raw copy of its sources) against Pharos's own
// catalog, and both by Mac, source and repository.
//
// Directory sizes are measured with the drive checks (see measureLibrary), so
// originals by Mac and source are exact. Nothing records how many bytes of the
// catalog or of a capture belong to a repository, and measuring it reads the
// whole catalog (minutes on a large library), so those shares are estimated:
// each Mac's capture of a source is divided among the conversations that Mac
// saw in that source, and the catalog among all conversations, in proportion to
// their message counts. The drive checks read the shares when they measure, so
// the breakdown answers from memory and a sync does not make it wait.

// storageShare is the message count of the conversations with one Mac, source
// and repository (empty when the workspace has none).
type storageShare struct {
	Host, Source, Repository string
	Messages                 float64
}

type storageShares struct {
	// Originals are sightings: a conversation every Mac captured counts in
	// each of those Macs' captures. Catalog counts each conversation once,
	// under the Mac and source whose copy the catalog holds.
	Originals, Catalog []storageShare
	Hosts              map[string]string // host ID to label
	Repositories       map[string]string // repository ID to display name
	// CatalogFree is the catalog's free pages: space deleted data left in
	// the file, which only compacting gives back to the drive.
	CatalogFree int64
}

func (c *Catalog) storageShares(ctx context.Context) (storageShares, error) {
	shares := storageShares{Hosts: map[string]string{}, Repositories: map[string]string{}}
	read := func(statement string, into *[]storageShare) error {
		rows, err := c.reader().QueryContext(ctx, statement)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var share storageShare
			if err := rows.Scan(&share.Host, &share.Source, &share.Repository, &share.Messages); err != nil {
				return err
			}
			*into = append(*into, share)
		}
		return rows.Err()
	}
	// A sighting without a message count still holds something.
	if err := read(`SELECT s.host_id,s.source_name,COALESCE(w.repository_id,''),SUM(MAX(COALESCE(s.messages,0),1))
		FROM conversation_sightings s JOIN conversations c ON c.id=s.conversation_id JOIN workspaces w ON w.id=c.workspace_id
		GROUP BY 1,2,3`, &shares.Originals); err != nil {
		return shares, err
	}
	if err := read(storageCatalogSharesSQL, &shares.Catalog); err != nil {
		return shares, err
	}
	var pages, pageSize int64
	if err := c.reader().QueryRowContext(ctx, "SELECT freelist_count,page_size FROM pragma_freelist_count,pragma_page_size").Scan(&pages, &pageSize); err != nil {
		return shares, err
	}
	shares.CatalogFree = pages * pageSize
	for statement, into := range map[string]map[string]string{
		"SELECT id,label FROM hosts":               shares.Hosts,
		"SELECT id,display_name FROM repositories": shares.Repositories,
	} {
		rows, err := queryMapsContext(ctx, c.reader(), statement)
		if err != nil {
			return shares, err
		}
		for _, row := range rows {
			into[firstString(row["id"])] = firstString(row["label"], row["display_name"])
		}
	}
	return shares, nil
}

// storageCatalogSharesSQL gives each conversation's catalog copy to the
// sighting it was written from: origin_host_id, or the legacy host when that
// is NULL, with its origin. A merged copy (see mergedWriter), or one whose
// writer no sighting matches, is the union of what its Macs saw, so it is
// spread evenly across all its sightings. Its weight is the largest sighting's
// message count, a lower bound for the union. A conversation never sighted
// stays with its writer and its workspace's source kind.
const storageCatalogSharesSQL = `WITH
conversation AS (
	SELECT c.id,c.origin,COALESCE(w.repository_id,'') repository,w.source_kind,
		CASE WHEN c.origin_host_id IS NULL THEN COALESCE((SELECT value FROM meta WHERE key='` + legacyHostKey + `'),'')
			WHEN c.origin_host_id='` + mergedWriter + `' THEN '' ELSE c.origin_host_id END host
	FROM conversations c JOIN workspaces w ON w.id=c.workspace_id),
weight AS (
	SELECT conversation_id id,MAX(MAX(COALESCE(messages,0),1)) messages FROM conversation_sightings GROUP BY conversation_id),
writer AS (
	SELECT c.id,s.host_id,s.source_name FROM conversation c
	JOIN conversation_sightings s ON s.conversation_id=c.id AND s.host_id=c.host AND s.origin=c.origin),
owner AS (
	SELECT id,host_id,source_name FROM writer
	UNION ALL
	SELECT c.id,s.host_id,s.source_name FROM conversation c JOIN conversation_sightings s ON s.conversation_id=c.id
	WHERE NOT EXISTS(SELECT 1 FROM writer WHERE writer.id=c.id)
	UNION ALL
	SELECT c.id,c.host,c.source_kind FROM conversation c
	WHERE NOT EXISTS(SELECT 1 FROM conversation_sightings s WHERE s.conversation_id=c.id)),
owners AS (SELECT id,COUNT(*) n FROM owner GROUP BY id)
SELECT o.host_id,o.source_name,c.repository,SUM(COALESCE(weight.messages,1)*1.0/owners.n)
FROM owner o JOIN conversation c ON c.id=o.id JOIN owners ON owners.id=o.id LEFT JOIN weight ON weight.id=o.id
GROUP BY 1,2,3`

// storageRow is one Mac, source or repository: its share of the originals and
// of the catalog.
type storageRow struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Originals int64  `json:"originals_bytes"`
	Catalog   int64  `json:"catalog_bytes"`
}

type storagePart struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Bytes int64  `json:"bytes"`
}

// storageRepositoryLimit is how many repositories the breakdown names; the
// rest are one row.
var storageRepositoryLimit = 12

// storageBreakdown divides sizes by shares. current is this Mac's host ID.
func storageBreakdown(sizes librarySizes, shares storageShares, current string) map[string]any {
	captureLatest, captureHistory, captureOther := int64(0), int64(0), int64(0)
	for _, capture := range sizes.Captures {
		captureLatest += capture.Latest
		captureHistory += capture.History
		captureOther += capture.Other
	}
	catalogTotal := sizes.CatalogBytes + sizes.CatalogSidecarBytes
	// Free pages are read from the catalog after its file was measured, and
	// compaction can shrink the file in between.
	catalogFree := min(shares.CatalogFree, max(sizes.CatalogBytes-sizes.CatalogLogBytes, 0))
	parts := []storagePart{
		{"originals_latest", "Originals: latest copies", captureLatest},
		{"originals_history", "Originals: earlier versions", captureHistory},
		{"originals_other", "Originals: manifests and indexes", captureOther},
		{"catalog_database", "Pharos catalog", sizes.CatalogBytes - sizes.CatalogLogBytes - catalogFree},
		{"catalog_free", "Free space in the catalog", catalogFree},
		{"catalog_log", "Catalog write-ahead log", sizes.CatalogLogBytes},
		{"catalog_sidecar", "Search and analysis caches", sizes.CatalogSidecarBytes},
		{"preserved", "Preserved packages", sizes.PreservedBytes},
		{"other", "App, settings and staging", sizes.OtherBytes},
	}
	var total int64
	for _, part := range parts {
		total += part.Bytes
	}

	machines, sources, repositories := storageRows{}, storageRows{}, storageRows{}
	// Originals: a capture's bytes go to its Mac and source exactly, and to
	// repositories by the messages that Mac saw in that source.
	weights := map[[2]string][]storageShare{}
	for _, share := range shares.Originals {
		key := [2]string{share.Host, share.Source}
		weights[key] = append(weights[key], share)
	}
	for _, capture := range sizes.Captures {
		bytes := capture.Latest + capture.History + capture.Other
		machines.add(capture.Host).Originals += bytes
		if capture.Source != "" {
			sources.add(capture.Source).Originals += bytes
		}
		for repository, part := range divide(bytes, weights[[2]string{capture.Host, capture.Source}]) {
			repositories.add(repository).Originals += part
		}
	}
	// The catalog has no per-Mac or per-source parts, so it is divided by
	// messages along every dimension.
	for index, part := range divideShares(catalogTotal, shares.Catalog) {
		share := shares.Catalog[index]
		machines.add(share.Host).Catalog += part
		sources.add(share.Source).Catalog += part
		repositories.add(share.Repository).Catalog += part
	}

	label := func(names map[string]string, empty string, mark func(string) string) func(*storageRow) {
		return func(row *storageRow) {
			switch {
			case row.ID == "":
				row.Label = empty
			case names[row.ID] != "":
				row.Label = names[row.ID]
			default:
				row.Label = row.ID
			}
			if mark != nil {
				row.Label += mark(row.ID)
			}
		}
	}
	return map[string]any{
		"total_bytes":        total,
		"catalog_free_bytes": catalogFree,
		// What compacting checks for before it starts (catalog_compact.go).
		"compact_needs_bytes": compactSpaceNeeded(sizes.CatalogBytes - sizes.CatalogLogBytes - catalogFree),
		"parts":               parts,
		"machines": machines.list(label(shares.Hosts, "Unknown Mac", func(id string) string {
			if id != "" && id == current {
				return " (this Mac)"
			}
			return ""
		}), 0, ""),
		"sources":      sources.list(label(nil, "Unknown source", nil), 0, ""),
		"repositories": repositories.list(label(shares.Repositories, "No repository", nil), storageRepositoryLimit, "Other repositories"),
		"estimated":    "Repository shares, and the catalog's division by Mac and source, are estimated from message counts.",
	}
}

type storageRows map[string]*storageRow

func (rows storageRows) add(id string) *storageRow {
	row := rows[id]
	if row == nil {
		row = &storageRow{ID: id}
		rows[id] = row
	}
	return row
}

// list sorts the rows largest first. With a limit, rows past it are summed
// into one named rest.
func (rows storageRows) list(label func(*storageRow), limit int, rest string) []storageRow {
	list := make([]storageRow, 0, len(rows))
	for _, row := range rows {
		if row.Originals == 0 && row.Catalog == 0 {
			continue
		}
		label(row)
		list = append(list, *row)
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i].Originals+list[i].Catalog, list[j].Originals+list[j].Catalog
		return a > b || a == b && list[i].Label < list[j].Label
	})
	if limit > 0 && len(list) > limit+1 {
		other := storageRow{ID: "other", Label: rest}
		for _, row := range list[limit:] {
			other.Originals += row.Originals
			other.Catalog += row.Catalog
		}
		list = append(list[:limit], other)
	}
	return list
}

// divide splits bytes among repositories by message count. With no
// conversations to divide by, it all goes to no repository.
func divide(bytes int64, shares []storageShare) map[string]int64 {
	if bytes == 0 {
		return nil
	}
	parts := map[string]int64{}
	if len(shares) == 0 {
		parts[""] = bytes
		return parts
	}
	for index, part := range divideShares(bytes, shares) {
		parts[shares[index].Repository] += part
	}
	return parts
}

// divideShares splits bytes in proportion to each share's messages, giving
// the rounding remainder to the largest shares so the parts add up to bytes.
func divideShares(bytes int64, shares []storageShare) []int64 {
	parts := make([]int64, len(shares))
	var weight float64
	for _, share := range shares {
		weight += share.Messages
	}
	if bytes <= 0 || weight <= 0 {
		return parts
	}
	type remainder struct {
		index int
		value float64
	}
	remainders := make([]remainder, len(shares))
	left := bytes
	for index, share := range shares {
		exact := float64(bytes) * share.Messages / weight
		parts[index] = int64(math.Floor(exact))
		left -= parts[index]
		remainders[index] = remainder{index, exact - math.Floor(exact)}
	}
	sort.SliceStable(remainders, func(i, j int) bool { return remainders[i].value > remainders[j].value })
	for i := 0; left > 0 && len(remainders) > 0; i, left = (i+1)%len(remainders), left-1 {
		parts[remainders[i].index]++
	}
	return parts
}

// storageBreakdown answers from the drive checks' cached sizes and shares;
// until they are first measured it reports that it is measuring.
func (s *Server) storageBreakdown() map[string]any {
	s.driveHealth()
	config := s.Config()
	state := drives.peek(config)
	if state == nil {
		return map[string]any{"status": "measuring"}
	}
	result := storageBreakdown(state.sizes, state.shares, currentHost().ID)
	_, path := driveScope(config)
	total, free := diskUsage(path)
	result["status"] = "measured"
	result["measured_at"] = state.checked.UTC().Format(time.RFC3339)
	result["volume_name"] = volumeName(config.ArchiveRoot)
	result["volume_total_bytes"], result["volume_free_bytes"] = total, free
	return result
}
