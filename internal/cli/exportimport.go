package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/4q4r/teleparse/internal/download"
	"github.com/4q4r/teleparse/internal/filters"
	"github.com/4q4r/teleparse/internal/scan"
	"github.com/4q4r/teleparse/internal/store"

	"github.com/spf13/cobra"
)

// errExportBad marks unusable Telegram Desktop exports: unreadable files,
// malformed JSON or a chat identity the scope resolver cannot address.
var errExportBad = errors.New("bad telegram desktop export")

// exportRow is one file-bearing export message: the manifest row plus the
// export-relative source path adoption links from.
type exportRow struct {
	item store.MediaItem
	src  string
}

// exportJob is a parsed desktop-export manifest ready for adoption.
type exportJob struct {
	rows     []exportRow
	mediaDir string
	chatSpec string
	label    string
}

// desktopExport mirrors the Telegram Desktop result.json top level: the chat
// identity plus the flat message list (see tdesktop's export_output_json.cpp).
type desktopExport struct {
	Type     string           `json:"type"`
	ID       int64            `json:"id"`
	Name     string           `json:"name"`
	Messages []desktopMessage `json:"messages"`
}

// desktopMessage mirrors one entry of the export's messages array. Photos
// and files appear as flat relative paths ("photos/photo_1@...jpg",
// "documents/document_1.mp4"); media_type distinguishes document flavors.
type desktopMessage struct {
	Type         string `json:"type"`
	ID           int64  `json:"id"`
	Date         string `json:"date"`
	DateUnixtime string `json:"date_unixtime"`
	GroupedID    int64  `json:"grouped_id"`
	Photo        string `json:"photo"`
	File         string `json:"file"`
	MediaType    string `json:"media_type"`
}

// Export chat identity vocabulary, mapped onto scope-resolver specs; saved
// messages address the self peer, everything else its bare numeric id.
const (
	exportTypeSaved = "saved_messages"
	exportSpecSaved = "saved"
)

// exportDateLayout is the export's zone-less timestamp layout.
const exportDateLayout = "2006-01-02T15:04:05"

// exportIdxFactor keeps one message's pseudo media ids apart; albums hold at
// most ten items, so three digits never collide.
const exportIdxFactor = 1000

// parseDesktopExport reads a Telegram Desktop export result.json into a
// manifest job: every file-bearing message becomes a media row, the chat
// spec comes from the export's own identity unless chatOverride replaces it
// and the media directory defaults to the export file's directory.
func parseDesktopExport(path, mediaDirOverride, chatOverride string) (*exportJob, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w: %w", path, errExportBad, err)
	}

	var doc desktopExport

	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode %s: %w: %w", path, errExportBad, err)
	}

	chatSpec := chatOverride
	if chatSpec == "" {
		chatSpec, err = exportChatSpec(doc)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}

	mediaDir := mediaDirOverride
	if mediaDir == "" {
		mediaDir = filepath.Dir(path)
	}

	job := &exportJob{chatSpec: chatSpec, mediaDir: mediaDir, label: doc.Name}

	for _, msg := range doc.Messages {
		row, ok := exportRowOf(msg)
		if !ok {
			continue
		}

		job.rows = append(job.rows, row)
	}

	return job, nil
}

// exportChatSpec derives the scope-resolver spec from the export's own chat
// identity: saved_messages maps to the self peer, every typed chat to its
// bare id; exports without a usable identity demand --chat.
func exportChatSpec(doc desktopExport) (string, error) {
	if doc.Type == exportTypeSaved {
		return exportSpecSaved, nil
	}

	if doc.ID > 0 && doc.Type != "" {
		return strconv.FormatInt(doc.ID, 10), nil
	}

	return "", fmt.Errorf("export carries no usable chat identity (type %q, id %d): %w",
		doc.Type, doc.ID, errExportBad)
}

// exportRowOf projects one export message onto a manifest row; ok is false
// for service messages and messages without an exported file.
func exportRowOf(msg desktopMessage) (exportRow, bool) {
	if msg.Type != "message" || msg.ID <= 0 {
		return exportRow{}, false
	}

	rel, class, ok := exportMediaOf(msg)
	if !ok {
		return exportRow{}, false
	}

	filename := filepath.Base(filepath.FromSlash(rel))

	item := store.MediaItem{
		MessageID:  msg.ID,
		MediaClass: class,
		MediaID:    exportPseudoID(msg.ID),
		Filename:   &filename,
		Date:       exportDateOf(msg),
		Status:     store.StatusDiscovered,
	}

	if item.Date == nil {
		item.Date = nil // absent dates stay absent, never zero timestamps
	}

	if mime, known := exportMimeOf(class); known {
		item.Mime = &mime
	}

	if msg.GroupedID > 0 {
		item.GroupedID = &msg.GroupedID
	}

	return exportRow{item: item, src: rel}, true
}

// exportMediaOf picks the exported file path and its media class: photos are
// self-evident, documents follow media_type with extension inference for the
// untyped remainder.
func exportMediaOf(msg desktopMessage) (string, string, bool) {
	switch {
	case msg.Photo != "":
		return msg.Photo, "photo", true
	case msg.File == "":
		return "", "", false
	}

	switch msg.MediaType {
	case "sticker":
		return msg.File, "sticker", true
	case "animation":
		return msg.File, "gif", true
	case "video_message":
		return msg.File, "video-note", true
	case "voice_message":
		return msg.File, "voice", true
	}

	return msg.File, exportClassByExt(msg.File), true
}

// exportClassByExt infers the media class of an untyped document from its
// extension; everything unmatched stays a plain document.
func exportClassByExt(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mkv", ".avi", ".mov", ".webm":
		return "video"
	case ".mp3", ".m4a", ".flac", ".ogg", ".oga", ".opus", ".wav":
		return "audio"
	default:
		return "document"
	}
}

// exportMimeOf guesses a row's MIME type from its class and extension.
func exportMimeOf(class string) (string, bool) {
	switch class {
	case "photo":
		return photoMime, true
	case "video", "video-note", "gif":
		return "video/mp4", true
	case "voice":
		return "audio/ogg", true
	case "audio":
		return "audio/mpeg", true
	case "sticker":
		return "image/webp", true
	default:
		return "", false
	}
}

// exportPseudoID derives the deterministic pseudo media id of an export row:
// negative, so it can never collide with a real Telegram media id, and
// stable across re-imports of the same export.
func exportPseudoID(messageID int64) int64 {
	return -(messageID*exportIdxFactor + 0)
}

// exportDateOf renders the message date as RFC3339, preferring the unixtime.
func exportDateOf(msg desktopMessage) *string {
	var stamped time.Time

	if unix, err := strconv.ParseInt(msg.DateUnixtime, 10, 64); err == nil && unix > 0 {
		stamped = time.Unix(unix, 0).UTC()
	} else if parsed, err := time.Parse(exportDateLayout, msg.Date); err == nil {
		stamped = parsed.UTC()
	} else {
		return nil
	}

	rfc3339 := stamped.Format(time.RFC3339)

	return &rfc3339
}

// adoptExportRows runs the export manifest against the resolved chat:
// filters apply first, then every row whose export file sits on disk with
// matching bytes is adopted (hard-linked into the blob store and the final
// path, marked done with an immediate sha256); absent or mismatched files
// queue for download. Dry-run mode links nothing and reports the counts.
func adoptExportRows(
	ctx context.Context,
	cmd *cobra.Command,
	app *App,
	state *store.Store,
	resolver *runResolver,
	collector *walkCollector,
	job *exportJob,
	plan *filters.Plan,
	mode runMode,
	target scan.Target,
) error {
	var adopted, queued int

	for _, row := range job.rows {
		if !planMatches(plan, exportFilterContext(row.item, target, job)) {
			continue
		}

		row.item.ChatID = target.Chat.ID

		if mode.dryRun {
			collector.items = append(collector.items, row.item)
			queued++

			continue
		}

		src := filepath.Join(job.mediaDir, filepath.FromSlash(row.src))

		wasAdopted, err := adoptExportRow(ctx, app, state, resolver, row, src)
		if err != nil {
			return err
		}

		if wasAdopted {
			adopted++

			continue
		}

		collector.items = append(collector.items, row.item)
		queued++
	}

	if mode.dryRun {
		return printLine(cmd, "adopted 0, queued %d (dry-run: adoption skipped)\n", queued)
	}

	return printLine(cmd, "adopted %d, queued %d\n", adopted, queued)
}

// adoptExportRow adopts one row when its source file exists with the
// recorded size (unknown sizes adopt on presence); ok reports adoption.
func adoptExportRow(
	ctx context.Context,
	app *App,
	state *store.Store,
	resolver *runResolver,
	row exportRow, src string,
) (bool, error) {
	info, err := os.Stat(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}

		return false, fmt.Errorf("stat export file %s: %w", src, err)
	}

	if row.item.Size != nil && *row.item.Size != info.Size() {
		return false, nil
	}

	resolved, err := resolver.Resolve(row.item) //nolint:contextcheck // store-only title lookup takes no context
	if err != nil {
		return false, fmt.Errorf("resolve path for message %d: %w", row.item.MessageID, err)
	}

	blob := download.BlobPath(app.paths.Downloads, row.item.MediaClass, row.item.MediaID,
		extOfExportFile(*row.item.Filename))

	if err := adoptIntoBlob(src, blob); err != nil {
		return false, err
	}

	if err := linkExportFinal(blob, resolved.Path); err != nil {
		return false, err
	}

	sum, err := sha256OfFile(src)
	if err != nil {
		return false, err
	}

	finalPath, sha := resolved.Path, sum
	row.item.Status = store.StatusDone
	row.item.Path = &finalPath
	row.item.Sha256 = &sha

	if _, err := state.UpsertMedia(ctx, &row.item); err != nil {
		return false, fmt.Errorf("record adopted message %d: %w", row.item.MessageID, err)
	}

	return true, nil
}

// adoptIntoBlob links the export file into the blob store, healing a stale
// blob whose bytes drifted from the source.
func adoptIntoBlob(src, blob string) error {
	if sameExportFile(src, blob) {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(blob), dirPermAdopt); err != nil {
		return fmt.Errorf("create blob dir: %w", err)
	}

	if err := os.Remove(blob); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("replace stale blob %s: %w", blob, err)
	}

	if err := download.LinkOrCopyFile(src, blob); err != nil {
		return fmt.Errorf("adopt %s into blob store: %w", src, err)
	}

	return nil
}

// linkExportFinal links the canonical blob onto the row's final path,
// treating an existing final as already served.
func linkExportFinal(blob, finalPath string) error {
	if sameExportFile(blob, finalPath) {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(finalPath), dirPermAdopt); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	if err := download.LinkOrCopyFile(blob, finalPath); err != nil {
		return fmt.Errorf("link adopted file to %s: %w", finalPath, err)
	}

	return nil
}

// dirPermAdopt mirrors the blob store directory permissions.
const dirPermAdopt = 0o700

// sameExportFile reports whether two paths share one inode; missing paths
// simply differ.
func sameExportFile(first, second string) bool {
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

// sha256OfFile digests a local file immediately; adopted bytes are on disk
// already, so this never blocks on the network.
func sha256OfFile(path string) (string, error) {
	handle, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s for hashing: %w", path, err)
	}

	defer func() { _ = handle.Close() }()

	digest := sha256.New()

	if _, err := io.Copy(digest, handle); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}

	return hex.EncodeToString(digest.Sum(nil)), nil
}

// exportFilterContext projects one export row onto the minimal filters
// context: chat identity, message id/date and the file description. Fields
// the export cannot know (sender, text, entities) stay unset, so only the
// predicates explicitly passed evaluate.
func exportFilterContext(item store.MediaItem, target scan.Target, job *exportJob) *filters.Context {
	file := &filters.FileInfo{Present: true, Kind: item.MediaClass}

	if item.Filename != nil {
		file.Name = *item.Filename
		file.Ext = strings.ToLower(filepath.Ext(file.Name))
	}

	if item.Size != nil {
		file.Size = *item.Size
	}

	title := target.Chat.Title
	if title == "" {
		title = job.label
	}

	mctx := &filters.Context{
		Chat:    filters.Chat{ID: target.Chat.ID, Type: target.Chat.Type, Title: title},
		Message: filters.Message{ID: item.MessageID},
		File:    file,
	}

	if item.Date != nil {
		if stamped, err := time.Parse(time.RFC3339, *item.Date); err == nil {
			mctx.Message.Date = stamped.Unix()
		}
	}

	return mctx
}

// extOfExportFile returns the extension of an export filename.
func extOfExportFile(name string) string {
	return strings.ToLower(filepath.Ext(name))
}
