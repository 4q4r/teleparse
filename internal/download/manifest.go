package download

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/4q4r/teleparse/internal/config"
	"github.com/4q4r/teleparse/internal/store"
)

// manifestFileName is the per-chat aggregate metadata document name written
// when Output.Metadata is "chat".
const manifestFileName = "manifest.json"

// ChatManifest is the per-chat aggregate metadata document written as
// manifest.json inside the chat's output directory; Files carry seq numbers
// 1..N in completion order with paths relative to the manifest itself.
type ChatManifest struct {
	Chat  ChatManifestHeader `json:"chat"`
	Files []ChatManifestFile `json:"files"`
}

// ChatManifestHeader identifies the chat a manifest belongs to.
type ChatManifestHeader struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// ChatManifestFile is one completed download recorded in a ChatManifest.
type ChatManifestFile struct {
	Seq        int    `json:"seq"`
	MsgID      int64  `json:"msg_id"`
	MediaClass string `json:"media_class"`
	MediaID    int64  `json:"media_id"`
	Filename   string `json:"filename,omitempty"`
	Size       *int64 `json:"size,omitempty"`
	Date       string `json:"date,omitempty"`
	SenderID   int64  `json:"sender_id,omitempty"`
	Sha256     string `json:"sha256,omitempty"`
	Path       string `json:"path"`
}

// writeMetadata emits the configured [output] metadata for one completed
// file: the legacy per-file sidecar ("file"), the per-chat manifest
// ("chat") or nothing ("off"). Failures surface as reporter counters and
// never fail a completed transfer.
func (m *Manager) writeMetadata(item store.MediaItem, resolved Resolved, finalPath string, shaHex *string) {
	switch m.cfg.Output.Metadata {
	case config.MetadataFile:
		if resolved.Meta == nil {
			return
		}

		if err := writeSidecar(finalPath+".json", resolved.Meta); err != nil {
			m.reporter.Inc("sidecar_errors", 1)
		}
	case config.MetadataChat:
		m.recordChatManifest(item, resolved, finalPath, shaHex)
	case config.MetadataOff:
	}
}

// chatManifestPaths computes where a chat's manifest lives and one file's
// path relative to it: the chat directory is the first segment of the
// rendered path, and a flat path (a template without {chat}) keeps the
// manifest at the downloads root as manifest-<chatID>.json.
func chatManifestPaths(root string, chatID int64, finalPath string) (string, string, error) {
	rel, err := filepath.Rel(root, finalPath)
	if err != nil {
		return "", "", fmt.Errorf("relate %s to root %s: %w", finalPath, root, err)
	}

	dir, rest, nested := strings.Cut(filepath.ToSlash(rel), "/")
	if !nested {
		rootManifest := "manifest-" + strconv.FormatInt(chatID, 10) + ".json"

		return filepath.Join(root, rootManifest), rel, nil
	}

	return filepath.Join(root, filepath.FromSlash(dir), manifestFileName), rest, nil
}

// manifestLock returns the per-chat mutex serializing manifest
// read-modify-write cycles; workers of one chat never interleave updates.
func (m *Manager) manifestLock(chatID int64) *sync.Mutex {
	m.manifestMuGuard.Lock()
	defer m.manifestMuGuard.Unlock()

	perChat, ok := m.manifestMu[chatID]
	if !ok {
		perChat = &sync.Mutex{}
		m.manifestMu[chatID] = perChat
	}

	return perChat
}

// recordChatManifest upserts one completed file into the chat's
// manifest.json. Metadata failures never fail a completed transfer — they
// surface as reporter counters, exactly like legacy sidecar errors.
func (m *Manager) recordChatManifest(item store.MediaItem, resolved Resolved, finalPath string, shaHex *string) {
	manifestPath, relPath, err := chatManifestPaths(m.cfg.Root, item.ChatID, finalPath)
	if err != nil {
		m.reporter.Inc("manifest_errors", 1)

		return
	}

	perChat := m.manifestLock(item.ChatID)

	perChat.Lock()
	defer perChat.Unlock()

	manifest, err := readChatManifestFile(manifestPath)
	if err != nil {
		m.reporter.Inc("manifest_errors", 1)

		return
	}

	title := ""
	if resolved.Meta != nil {
		title = resolved.Meta.ChatTitle
	}

	manifest.Chat = ChatManifestHeader{ID: item.ChatID, Title: title}
	upsertChatManifestFile(&manifest, chatManifestFileOf(item, relPath, shaHex))

	if err := writeChatManifestFile(manifestPath, manifest); err != nil {
		m.reporter.Inc("manifest_errors", 1)
	}
}

// chatManifestFileOf projects one completed media row onto its manifest
// entry; sha256 lands only when hashing ran.
func chatManifestFileOf(item store.MediaItem, relPath string, shaHex *string) ChatManifestFile {
	entry := ChatManifestFile{
		MsgID:      item.MessageID,
		MediaClass: item.MediaClass,
		MediaID:    item.MediaID,
		Size:       item.Size,
		Path:       relPath,
	}

	if item.Filename != nil {
		entry.Filename = *item.Filename
	}

	if item.Date != nil {
		entry.Date = *item.Date
	}

	if item.SenderID != nil {
		entry.SenderID = *item.SenderID
	}

	if shaHex != nil {
		entry.Sha256 = *shaHex
	}

	return entry
}

// upsertChatManifestFile replaces the entry for the same message and media
// id in place (keeping its seq) or appends it as seq N+1.
func upsertChatManifestFile(manifest *ChatManifest, entry ChatManifestFile) {
	for idx := range manifest.Files {
		if manifest.Files[idx].MsgID == entry.MsgID && manifest.Files[idx].MediaID == entry.MediaID {
			entry.Seq = manifest.Files[idx].Seq
			manifest.Files[idx] = entry

			return
		}
	}

	entry.Seq = len(manifest.Files) + 1
	manifest.Files = append(manifest.Files, entry)
}

// readChatManifestFile loads a manifest, treating an absent file as a fresh
// one; corrupt content fails loudly to the caller.
func readChatManifestFile(path string) (ChatManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ChatManifest{}, nil
		}

		return ChatManifest{}, fmt.Errorf("read manifest %s: %w", path, err)
	}

	var manifest ChatManifest

	if err := json.Unmarshal(raw, &manifest); err != nil {
		return ChatManifest{}, fmt.Errorf("decode manifest %s: %w", path, err)
	}

	return manifest, nil
}

// writeChatManifestFile persists a manifest atomically enough for the
// per-chat lock's read-modify-write cycle (write-then-rename).
func writeChatManifestFile(path string, manifest ChatManifest) error {
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), dirPermDownload); err != nil {
		return fmt.Errorf("create manifest dir %s: %w", path, err)
	}

	if err := os.WriteFile(path+".tmp", append(encoded, '\n'), filePermDownload); err != nil {
		return fmt.Errorf("write manifest %s: %w", path, err)
	}

	if err := os.Rename(path+".tmp", path); err != nil {
		return fmt.Errorf("promote manifest %s: %w", path, err)
	}

	return nil
}
