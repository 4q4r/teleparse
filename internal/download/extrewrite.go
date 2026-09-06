package download

import (
	"mime"
	"path"
	"strings"
)

// canonicalExtTable pins the canonical extension for the archive types
// whose Telegram-side file names most often disagree with the recorded
// mime type; everything else defers to the stdlib mime table.
func canonicalExtTable() map[string]string {
	return map[string]string{
		"application/zip":              ".zip",
		"application/x-rar-compressed": ".rar",
		"application/vnd.rar":          ".rar",
		"application/x-tar":            ".tar",
		"application/gzip":             ".gz",
	}
}

// compoundArchiveSuffixes lists multi-dot suffixes that already encode a
// container plus a compression layer; rewriting through any of them would
// corrupt the name's meaning.
func compoundArchiveSuffixes() []string {
	return []string{".tar.gz", ".tgz", ".tar.bz2", ".tbz2", ".tar.xz", ".txz", ".tar.zst"}
}

// CanonicalExt returns the canonical file extension for mimeType, "" when
// the type is unknown. The pinned archive table decides first; the stdlib
// mime table's first extension serves as the fallback.
func CanonicalExt(mimeType string) string {
	if ext, ok := canonicalExtTable()[mimeType]; ok {
		return ext
	}

	exts, err := mime.ExtensionsByType(mimeType)
	if err != nil || len(exts) == 0 {
		return ""
	}

	return exts[0]
}

// knownExt reports whether ext is among the extensions the stdlib maps to
// mimeType (case-insensitive), so equivalent spellings such as .jpg and
// .jpeg never count as a mismatch.
func knownExt(mimeType, ext string) bool {
	exts, err := mime.ExtensionsByType(mimeType)
	if err != nil {
		return false
	}

	for _, candidate := range exts {
		if strings.EqualFold(candidate, ext) {
			return true
		}
	}

	return false
}

// RewriteExtPath rewrites the extension of final to the canonical one for
// mimeType when the two disagree. A matching (or equivalently spelled)
// extension, an unknown mime type, an absent mime type and compound archive
// suffixes such as .tar.gz are never touched; the stem keeps its case.
func RewriteExtPath(final, mimeType string) string {
	if mimeType == "" {
		return final
	}

	canonical := CanonicalExt(mimeType)
	if canonical == "" {
		return final
	}

	lower := strings.ToLower(final)
	for _, suffix := range compoundArchiveSuffixes() {
		if strings.HasSuffix(lower, suffix) {
			return final
		}
	}

	ext := path.Ext(final)
	if ext == canonical || knownExt(mimeType, ext) {
		return final
	}

	return strings.TrimSuffix(final, ext) + canonical
}
