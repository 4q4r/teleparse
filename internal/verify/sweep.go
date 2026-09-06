package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/store"
)

// Class is one manifest-row or blob-store integrity classification.
type Class string

// Row classifications: ClassOK needs no action; ClassMissing means both
// the final path and the blob are gone; ClassFinalMissingBlobAlive means
// only the blob survives (repairable by relinking); ClassSizeMismatch and
// ClassHashMismatch mean the file on disk drifted from the manifest;
// ClassFormatError means native validation failed; ClassUncheckedFormat
// means the extension has no native validator (a coverage note, not
// damage); ClassOrphanBlob marks blob-store bytes no manifest row
// references; ClassRowWithoutPath counts queued or failed rows verify
// deliberately skips.
const (
	ClassOK                    Class = "ok"
	ClassMissing               Class = "missing"
	ClassFinalMissingBlobAlive Class = "final-missing-blob-alive"
	ClassSizeMismatch          Class = "size-mismatch"
	ClassHashMismatch          Class = "hash-mismatch"
	ClassFormatError           Class = "format-error"
	ClassUncheckedFormat       Class = "unchecked-format"
	ClassOrphanBlob            Class = "orphan-blob"
	ClassRowWithoutPath        Class = "row-without-path"
)

// verifyDirPerm and verifyFilePerm mirror the download tree's permissions.
const (
	verifyDirPerm  = 0o700
	verifyFilePerm = 0o600
)

// Finding describes one problem: a damaged manifest row or an orphaned
// blob. Fixed records the repair action --fix applied, if any.
type Finding struct {
	ChatID     int64  `json:"chat_id,omitempty"`
	MessageID  int64  `json:"message_id,omitempty"`
	MediaIndex int    `json:"media_index,omitempty"`
	MediaClass string `json:"media_class,omitempty"`
	MediaID    int64  `json:"media_id,omitempty"`
	Filename   string `json:"filename,omitempty"`
	Path       string `json:"path"`
	Class      Class  `json:"class"`
	Detail     string `json:"detail,omitempty"`
	Fixed      string `json:"fixed,omitempty"`
}

// Options steer one sweep: Root is the downloads root, Deep adds sha256
// re-hashing plus native format validation, Fix repairs what it can,
// Chats scopes the manifest walk (empty walks every chat), and OnProblem
// streams each problem finding as it settles.
type Options struct {
	Root      string
	Deep      bool
	Fix       bool
	Chats     []int64
	OnProblem func(Finding)
}

// Repair counts the actions --fix took.
type Repair struct {
	Relinked     int   `json:"relinked"`
	Requeued     int   `json:"requeued"`
	GcRemoved    int64 `json:"gc_removed"`
	GcFreedBytes int64 `json:"gc_freed_bytes"`
}

// Report is the full sweep outcome: classification counts, the problem
// findings, and — after --fix — what was repaired and what still remains.
// Classes always counts the pre-repair classification; a repaired finding
// keeps its original class with the action recorded in Fixed.
type Report struct {
	Rows          int           `json:"rows"`
	SkippedNoPath int           `json:"skipped_no_path"`
	Blobs         int           `json:"blobs"`
	Orphaned      int           `json:"orphaned"`
	Classes       map[Class]int `json:"classes"`
	Findings      []Finding     `json:"findings"`
	Repair        Repair        `json:"repair"`
	Problems      int           `json:"problems"`
	Remaining     int           `json:"remaining"`
	Deep          bool          `json:"deep"`
	Fix           bool          `json:"fix"`
}

// Sweep walks every manifest row (filtered by opts.Chats), classifies the
// state of its bytes on disk, optionally repairs damage, then walks the
// blob store for orphans. Exit-grade problems are missing copies, size or
// hash drift, format errors and orphaned blobs; unchecked formats and
// pathless rows are reported but never fail.
func Sweep(ctx context.Context, state *store.Store, opts Options) (*Report, error) {
	report := &Report{Classes: map[Class]int{}, Deep: opts.Deep, Fix: opts.Fix}

	rows, err := state.ListMediaRows(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("load manifest rows: %w", err)
	}

	inScope := chatScope(opts.Chats)

	for _, row := range rows {
		if !inScope(row.ChatID) {
			continue
		}

		report.Rows++

		if err := sweepRow(ctx, state, opts, row, report); err != nil {
			return nil, err
		}

		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("verify sweep cancelled: %w", err)
		}
	}

	if err := sweepBlobs(ctx, state, opts, report); err != nil {
		return nil, err
	}

	report.Problems = len(report.Findings)

	if opts.Fix && report.Orphaned > 0 {
		removed, freed, err := download.BlobGC(opts.Root)
		if err != nil {
			return nil, fmt.Errorf("gc orphaned blobs: %w", err)
		}

		report.Repair.GcRemoved = removed
		report.Repair.GcFreedBytes = freed
	}

	report.Remaining = report.Problems -
		report.Repair.Relinked -
		report.Repair.Requeued -
		min(int(report.Repair.GcRemoved), report.Orphaned)

	return report, nil
}

// sweepRow classifies one manifest row and records its outcome; rows
// without a path (queued, failed or mid-download) are counted, not
// inspected.
func sweepRow(ctx context.Context, state *store.Store, opts Options, row store.MediaRow, report *Report) error {
	if row.Path == nil || *row.Path == "" || row.Status != store.StatusDone {
		report.SkippedNoPath++

		return nil
	}

	finalPath := *row.Path

	finalInfo, finalErr := os.Stat(finalPath)
	if finalErr != nil && !errors.Is(finalErr, fs.ErrNotExist) {
		return fmt.Errorf("stat final %s: %w", finalPath, finalErr)
	}

	blob := download.BlobPath(opts.Root, row.MediaClass, row.MediaID, blobExt(row.Filename))

	_, blobErr := os.Stat(blob)
	if blobErr != nil && !errors.Is(blobErr, fs.ErrNotExist) {
		return fmt.Errorf("stat blob %s: %w", blob, blobErr)
	}

	class, detail := classifyRow(opts, row, finalPath, finalErr, blobErr, finalInfo)
	report.Classes[class]++

	if class == ClassOK || class == ClassUncheckedFormat {
		return nil
	}

	finding := rowFinding(row, finalPath, class, detail)

	if opts.Fix {
		action, repaired, fixErr := repairRow(ctx, state, class, row, blob, finalPath)
		if repaired {
			finding.Fixed = action

			if class == ClassFinalMissingBlobAlive {
				report.Repair.Relinked++
			} else {
				report.Repair.Requeued++
			}
		} else if fixErr != "" {
			finding.Detail = finding.Detail + "; fix failed: " + fixErr
		}
	}

	if isProblem(class) {
		report.Findings = append(report.Findings, finding)
		emitProblem(opts, finding)
	}

	return nil
}

// classifyRow resolves the row's class from filesystem state alone (fast
// mode) or with deep checks: sha256 re-hashing when the manifest recorded
// a digest, then native format validation by extension.
func classifyRow(
	opts Options,
	row store.MediaRow,
	finalPath string,
	finalErr, blobErr error,
	finalInfo os.FileInfo,
) (Class, string) {
	switch {
	case finalErr != nil && blobErr == nil:
		return ClassFinalMissingBlobAlive, "final path gone, blob alive"
	case finalErr != nil:
		return ClassMissing, "final path and blob both gone"
	}

	if row.Size != nil && finalInfo.Size() != *row.Size {
		return ClassSizeMismatch, fmt.Sprintf("size %d, manifest records %d", finalInfo.Size(), *row.Size)
	}

	if !opts.Deep {
		return ClassOK, ""
	}

	if row.Sha256 != nil && *row.Sha256 != "" {
		sum, err := hashFile(finalPath)
		if err != nil {
			return ClassFormatError, fmt.Sprintf("hash: %v", err)
		}

		if sum != *row.Sha256 {
			return ClassHashMismatch, "sha256 mismatch"
		}
	}

	verdict, _, err := ValidateFileDetailed(finalPath, blobExt(row.Filename))
	if err != nil {
		return ClassFormatError, err.Error()
	}

	if verdict == VerdictUnsupported {
		return ClassUncheckedFormat, "no native validator for this extension"
	}

	return ClassOK, ""
}

// repairRow applies one --fix action for the row's class: relinking the
// surviving blob onto the final path, or requeueing the row with its retry
// budget restored. Format errors are never auto-repaired — the data on
// disk is wrong in place and re-downloading may not fix the source. The
// third return carries the failure note when a repair attempt failed.
func repairRow(ctx context.Context, state *store.Store, class Class, row store.MediaRow, blob, finalPath string,
) (string, bool, string) {
	switch class {
	case ClassFinalMissingBlobAlive:
		if err := relinkBlob(blob, finalPath); err != nil {
			return "", false, err.Error()
		}

		return "relinked from blob", true, ""
	case ClassMissing, ClassSizeMismatch, ClassHashMismatch:
		if err := state.RequeueMedia(ctx, row.ChatID, row.MessageID, row.MediaIndex); err != nil {
			return "", false, err.Error()
		}

		return "requeued for re-download", true, ""
	default:
		return "", false, ""
	}
}

// relinkBlob hardlinks the blob back onto the final path, falling back to
// a byte copy on filesystems without links; a final path that reappeared
// meanwhile counts as repaired.
func relinkBlob(blob, finalPath string) error {
	if err := os.MkdirAll(filepath.Dir(finalPath), verifyDirPerm); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	err := os.Link(blob, finalPath)

	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrExist):
		return nil // the file is back; nothing to do
	}

	if copyErr := copyBytes(blob, finalPath); copyErr != nil {
		return fmt.Errorf("link %s -> %s: %w; copy fallback: %w", blob, finalPath, err, copyErr)
	}

	return nil
}

// copyBytes duplicates src onto dst with download-tree permissions.
func copyBytes(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}

	defer func() { _ = source.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, verifyFilePerm)
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

// sweepBlobs walks the blob store and records every blob no manifest row
// references as an orphan finding.
func sweepBlobs(ctx context.Context, state *store.Store, opts Options, report *Report) error {
	blobRoot := download.BlobRoot(opts.Root)

	if _, err := os.Stat(blobRoot); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}

		return fmt.Errorf("stat blob root: %w", err)
	}

	walkErr := filepath.WalkDir(blobRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("visit %s: %w", path, err)
		}

		if entry.IsDir() {
			return nil
		}

		report.Blobs++

		mediaClass := filepath.Base(filepath.Dir(path))

		mediaID, ok := blobIdentity(entry.Name())
		if ok {
			_, exists, err := state.MediaByFile(ctx, mediaClass, mediaID)
			if err != nil {
				return fmt.Errorf("lookup blob row %s/%d: %w", mediaClass, mediaID, err)
			}

			if exists {
				return nil
			}
		}

		report.Orphaned++

		finding := Finding{Path: path, Class: ClassOrphanBlob, Detail: "no manifest row references this blob"}
		report.Findings = append(report.Findings, finding)
		emitProblem(opts, finding)

		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("walk blob store: %w", walkErr)
	}

	return nil
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

// blobExt returns the extension of an optional manifest filename.
func blobExt(name *string) string {
	if name == nil {
		return ""
	}

	return filepath.Ext(*name)
}

// chatScope resolves the chat filter into a membership test; an empty
// filter admits every chat.
func chatScope(chats []int64) func(int64) bool {
	if len(chats) == 0 {
		return func(int64) bool { return true }
	}

	allowed := make(map[int64]bool, len(chats))

	for _, chatID := range chats {
		allowed[chatID] = true
	}

	return func(chatID int64) bool { return allowed[chatID] }
}

// rowFinding builds the finding record for one manifest row.
func rowFinding(row store.MediaRow, path string, class Class, detail string) Finding {
	finding := Finding{
		ChatID:     row.ChatID,
		MessageID:  row.MessageID,
		MediaIndex: row.MediaIndex,
		MediaClass: row.MediaClass,
		MediaID:    row.MediaID,
		Path:       path,
		Class:      class,
		Detail:     detail,
	}

	if row.Filename != nil {
		finding.Filename = *row.Filename
	}

	return finding
}

// isProblem reports whether a class fails the sweep: damage or orphans,
// never coverage notes or pathless rows.
func isProblem(class Class) bool {
	switch class {
	case ClassMissing, ClassFinalMissingBlobAlive, ClassSizeMismatch,
		ClassHashMismatch, ClassFormatError, ClassOrphanBlob:
		return true
	default:
		return false
	}
}

// emitProblem hands one settled finding to the progress callback.
func emitProblem(opts Options, finding Finding) {
	if opts.OnProblem != nil {
		opts.OnProblem(finding)
	}
}

// hashFile streams the file through sha256 and returns the lowercase hex
// digest in the manifest's recording format.
func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open for hashing: %w", err)
	}

	defer func() { _ = file.Close() }()

	digest := sha256.New()

	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("hash bytes: %w", err)
	}

	return hex.EncodeToString(digest.Sum(nil)), nil
}
