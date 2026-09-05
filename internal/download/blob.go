package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/4q4r/teleparse/internal/store"
)

// blobDirName is the hidden directory under the downloads root holding the
// canonical blob store.
const blobDirName = ".teleparse"

// LinkFunc creates a hard link from oldname to newname. The seam exists so
// tests can simulate filesystems without hardlink support (EXDEV).
type LinkFunc func(oldname, newname string) error

// BlobRoot returns the blob-store root under the downloads root. Blobs live
// inside the downloads root so hardlinks never cross filesystems.
func BlobRoot(root string) string {
	return filepath.Join(root, blobDirName, "blobs")
}

// BlobPath derives the canonical blob path of a unique media file. The
// extension comes from the manifest filename, so every sighting of the same
// (media_class, media_id) agrees on one path.
func BlobPath(root, mediaClass string, mediaID int64, ext string) string {
	return filepath.Join(BlobRoot(root), mediaClass, strconv.FormatInt(mediaID, 10)+ext)
}

// BlobStatsReport summarizes the blob store for the dedupe command.
type BlobStatsReport struct {
	Blobs    int64
	Bytes    int64
	Tracked  int64
	Orphaned int64
}

// BlobStats walks the blob store and reports blob counts, total bytes and
// how many blobs still have a manifest row (tracked) versus none (orphaned).
func BlobStats(ctx context.Context, state *store.Store, root string) (BlobStatsReport, error) {
	report := BlobStatsReport{}

	blobRoot := BlobRoot(root)
	if !fileExists(blobRoot) {
		return report, nil
	}

	err := filepath.WalkDir(blobRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("visit %s: %w", path, err)
		}

		if entry.IsDir() {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat blob %s: %w", path, err)
		}

		tracked, err := blobTracked(ctx, state, filepath.Base(filepath.Dir(path)), entry.Name())
		if err != nil {
			return err
		}

		report.Blobs++
		report.Bytes += info.Size()

		if tracked {
			report.Tracked++
		} else {
			report.Orphaned++
		}

		return nil
	})
	if err != nil {
		return BlobStatsReport{}, fmt.Errorf("walk blob store: %w", err)
	}

	return report, nil
}

// BlobGC unlinks blobs whose data no longer has any consumer: a link count
// of one means only the blob's own directory entry references the data, so
// removing it frees the bytes. Blobs with surviving links — or an
// unknowable link count — are never touched. Returns the removed blob count
// and freed bytes.
func BlobGC(root string) (int64, int64, error) {
	blobRoot := BlobRoot(root)
	if !fileExists(blobRoot) {
		return 0, 0, nil
	}

	var removed, freed int64

	err := filepath.WalkDir(blobRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("visit %s: %w", path, err)
		}

		if entry.IsDir() {
			return nil
		}

		links, ok := hardlinkCount(path)
		if !ok || links > blobOnlyLinks {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat blob %s: %w", path, err)
		}

		if err := os.Remove(path); err != nil { //nolint:gosec // the blob root is the user's own downloads tree
			return fmt.Errorf("remove orphaned blob %s: %w", path, err)
		}

		removed++
		freed += info.Size()

		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("walk blob store: %w", err)
	}

	return removed, freed, nil
}

// blobOnlyLinks marks a blob whose inode is referenced by its own directory
// entry alone; nothing else can reach the data, so gc may unlink it.
const blobOnlyLinks = 1

// blobTracked reports whether a manifest row exists for the blob's
// (class, media id).
func blobTracked(ctx context.Context, state *store.Store, class, name string) (bool, error) {
	mediaID, ok := blobIdentity(name)
	if !ok {
		return false, nil
	}

	_, exists, err := state.MediaByFile(ctx, class, mediaID)
	if err != nil {
		return false, fmt.Errorf("lookup blob row %s/%d: %w", class, mediaID, err)
	}

	return exists, nil
}

// blobIdentity splits a blob filename into its media id; ok is false when
// the name carries no leading digits.
func blobIdentity(name string) (int64, bool) {
	digits := name
	if dot := strings.IndexByte(name, '.'); dot >= 0 {
		digits = name[:dot]
	}

	mediaID, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || mediaID <= 0 {
		return 0, false
	}

	return mediaID, true
}

// hardlinkMode reports whether the manager dedupes through the blob store.
func (m *Manager) hardlinkMode() bool {
	return m.cfg.Dedupe == "hardlink"
}

// blobPathOf derives one media row's blob path from its manifest filename
// extension.
func blobPathOf(root string, item store.MediaItem) string {
	return BlobPath(root, item.MediaClass, item.MediaID, extOfFilename(item.Filename))
}

// extOfFilename returns the extension of an optional manifest filename.
func extOfFilename(name *string) string {
	if name == nil {
		return ""
	}

	return filepath.Ext(*name)
}

// rememberOccurrence records a duplicate sighting for later linking; only
// hardlink mode owes every chat a file, other modes drop the sighting.
func (m *Manager) rememberOccurrence(item store.MediaItem) {
	if m.hardlinkMode() {
		m.pendingLinks = append(m.pendingLinks, item)
	}
}

// processHardlinkItem serves one claimed row in hardlink mode. A live blob
// links onto the final path without network; a final path that already IS
// the blob's data counts as served (never re-linked); everything else falls
// through to the download ladder, which fetches into the blob and links the
// final path from it.
func (m *Manager) processHardlinkItem(runCtx, bookCtx context.Context, runID string,
	item store.MediaItem, resolved Resolved, state *runState,
) {
	blob := blobPathOf(m.cfg.Root, item)

	if fileExists(blob) {
		if sameFile(resolved.Path, blob) {
			m.skipItem(bookCtx, state, item, resolved.Path)

			return
		}

		finalPath, skip, err := m.finalPathFor(resolved.Path)
		if err != nil {
			m.failItem(bookCtx, state, item, err, m.cfg.RetryMax)

			return
		}

		if skip {
			m.skipItem(bookCtx, state, item, finalPath)

			return
		}

		if err := m.linkBlobInto(bookCtx, state, item, resolved, blob, finalPath); err != nil {
			m.failItem(bookCtx, state, item, err, m.cfg.RetryMax)
		}

		return
	}

	finalPath, skip, err := m.finalPathFor(resolved.Path)
	if err != nil {
		m.failItem(bookCtx, state, item, err, m.cfg.RetryMax)

		return
	}

	if skip {
		m.skipItem(bookCtx, state, item, finalPath)

		return
	}

	m.downloadWithRetries(runCtx, bookCtx, runID, item, resolved, finalPath, state)
}

// linkBlobInto hardlinks (or copies) the blob onto one occurrence's final
// path, then writes the sidecar, marks the row done and runs the hooks.
// Used for claimed rows and enqueued duplicate sightings alike; MarkDone is
// a no-op for sightings without a row of their own.
func (m *Manager) linkBlobInto(ctx context.Context, state *runState,
	item store.MediaItem, resolved Resolved, blob, finalPath string,
) error {
	if err := os.MkdirAll(filepath.Dir(finalPath), dirPermDownload); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	if err := m.linkOrCopy(state, blob, finalPath); err != nil {
		return err
	}

	if m.cfg.Output.Sidecar && resolved.Meta != nil {
		if err := writeSidecar(finalPath+".json", resolved.Meta); err != nil {
			m.reporter.Inc("sidecar_errors", 1)
		}
	}

	if err := m.store.MarkDone(ctx, item.ChatID, item.MessageID, item.MediaIndex, finalPath, nil); err != nil {
		m.reporter.Inc("store_errors", 1)
	}

	m.runHooks(ctx, finalPath)

	state.mutate(func(res *Result) {
		res.Linked++
	})

	m.reporter.Inc("linked", reportEveryAttempts)

	return nil
}

// drainPendingLinks serves the duplicate sightings Enqueue recorded: in
// hardlink mode every occurrence of a known file still owes its chat a
// link. It runs after the workers drain, so blobs this run downloaded
// already exist; an occurrence whose every copy (blob included) was
// deleted re-downloads through the ladder — network cost exactly once.
func (m *Manager) drainPendingLinks(runCtx, bookCtx context.Context, runID string, state *runState) {
	pending := m.pendingLinks
	m.pendingLinks = nil

	for _, item := range pending {
		if runCtx.Err() != nil {
			return
		}

		resolved, err := m.resolve.Resolve(item)
		if err != nil {
			m.countOccurrenceFailed(state)

			continue
		}

		if fileExists(resolved.Path) {
			continue // this chat already holds its copy
		}

		blob := blobPathOf(m.cfg.Root, item)

		if fileExists(blob) {
			if err := m.linkBlobInto(bookCtx, state, item, resolved, blob, resolved.Path); err != nil {
				m.countOccurrenceFailed(state)
			}

			continue
		}

		m.downloadWithRetries(runCtx, bookCtx, runID, item, resolved, resolved.Path, state)
	}
}

// countOccurrenceFailed books a duplicate sighting that could not be served.
func (m *Manager) countOccurrenceFailed(state *runState) {
	state.mutate(func(res *Result) {
		res.Failed++
	})

	m.reporter.Inc("failed", reportEveryAttempts)
}

// linkOrCopy hardlinks src onto dst, falling back to a byte copy when the
// filesystem refuses links (EXDEV, unsupported). A dst that appeared
// meanwhile counts as served; fallback copies are counted and warned once
// per run through the link_fallback reporter counter.
func (m *Manager) linkOrCopy(state *runState, src, dst string) error {
	err := m.Link(src, dst)

	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrExist):
		return nil // a concurrent occurrence won the race: the file is there
	}

	if copyErr := copyFile(src, dst); copyErr != nil {
		return fmt.Errorf("link %s -> %s: %w; copy fallback: %w", src, dst, err, copyErr)
	}

	state.mutate(func(res *Result) {
		res.LinkCopies++
	})

	m.linkFallbackOnce.Do(func() { m.reporter.Inc("link_fallback", 1) })

	return nil
}

// copyFile duplicates the src bytes onto dst with download file
// permissions.
func copyFile(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}

	defer func() { _ = source.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePermDownload)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}

	if _, err := io.Copy(out, source); err != nil {
		_ = out.Close()

		return fmt.Errorf("copy bytes: %w", err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("close destination: %w", err)
	}

	return nil
}

// sameFile reports whether two paths resolve to one inode; missing paths
// simply differ.
func sameFile(first, second string) bool {
	firstInfo, err := os.Stat(first)
	if err != nil {
		return false
	}

	secondInfo, err := os.Stat(second)
	if err != nil {
		return false
	}

	return os.SameFile(firstInfo, secondInfo)
}
