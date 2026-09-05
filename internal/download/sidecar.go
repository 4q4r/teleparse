package download

import (
	"github.com/4q4r/teleparse/internal/filters"
)

// SidecarMeta is the message metadata written next to a completed download
// as <final>.json when Output.Sidecar is enabled.
type SidecarMeta struct {
	ChatID     int64             `json:"chat_id"`
	ChatTitle  string            `json:"chat_title,omitempty"`
	ChatType   string            `json:"chat_type,omitempty"`
	MessageID  int64             `json:"message_id"`
	MediaIndex int               `json:"media_index"`
	SenderID   int64             `json:"sender_id,omitempty"`
	SenderName string            `json:"sender_name,omitempty"`
	Date       int64             `json:"date,omitempty"`
	Text       string            `json:"text,omitempty"`
	Entities   []filters.Entity  `json:"entities,omitempty"`
	File       *filters.FileInfo `json:"file,omitempty"`
	Filename   string            `json:"filename,omitempty"`
	Path       string            `json:"path"`
}
