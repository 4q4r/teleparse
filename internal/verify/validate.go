// Package verify sweeps the media manifest against the downloads tree,
// classifying every tracked file and validating archive formats natively
// with the Go standard library — no external tools.
package verify

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Verdict reports the outcome of one native format validation.
type Verdict string

// Validation verdicts: VerdictOK means the format was fully validated
// (per-entry CRC32 for zip, CRC32+ISIZE for gzip, tar header walk for
// tar.gz); VerdictFormatError means the file failed its format check;
// VerdictUnsupported means the extension has no native validator (size
// check only); VerdictCheckedLite means a weaker honest check passed (rar:
// signature plus best-effort end marker).
const (
	VerdictOK          Verdict = "ok"
	VerdictFormatError Verdict = "format-error"
	VerdictUnsupported Verdict = "unchecked-format"
	VerdictCheckedLite Verdict = "rar-signature-ok"
)

// Sentinel errors of the native validators.
var (
	// ErrBadRarSignature reports a .rar file without a RAR4 or RAR5 magic.
	ErrBadRarSignature = errors.New("missing RAR signature")
	// ErrEmptyRar reports a rar-sized file that carries no bytes at all.
	ErrEmptyRar = errors.New("empty rar file")
)

// Format sniffing constants: the tar magic lives at a fixed offset inside
// the first 512-byte header block, and rar signatures are fixed prefixes.
const (
	tarMagicOffset = 257
	tarMagicLen    = 6 // len("ustar")
	rarHeaderLen   = 8
	rarTailLen     = 8
)

// rar4Signature, rar5Signature and rar4EndMarker are the two archive
// signatures and the end-of-archive block header scanned for in the final
// bytes; string constants keep the package free of mutable globals.
const (
	rar4Signature = "\x52\x61\x72\x21\x1a\x07\x00"
	rar5Signature = "\x52\x61\x72\x21\x1a\x07\x01\x00"
	rar4EndMarker = "\xc4\x3d\x7b\x00\x40\x07\x00"
)

// ValidateFileDetailed natively validates the file at path according to
// ext and returns the verdict; detail carries the human-readable check
// summary.
func ValidateFileDetailed(path, ext string) (Verdict, string, error) {
	switch strings.ToLower(ext) {
	case ".zip":
		entries, err := validateZip(path)
		if err != nil {
			return VerdictFormatError, "", err
		}

		return VerdictOK, fmt.Sprintf("zip: %d entries", entries), nil
	case ".gz", ".tgz":
		isTar, detail, err := validateGzip(path)
		if err != nil {
			return VerdictFormatError, "", err
		}

		if isTar {
			detail = "tar." + detail
		}

		return VerdictOK, detail, nil
	case ".rar":
		return validateRar(path)
	default:
		return VerdictUnsupported, "unchecked format", nil
	}
}

// ValidateFile natively validates the file at path according to ext,
// discarding the detail summary.
func ValidateFile(path, ext string) (Verdict, error) {
	verdict, _, err := ValidateFileDetailed(path, ext)

	return verdict, err
}

// validateZip opens the archive, walks every entry and drains its bytes,
// which verifies each member's CRC32 natively through archive/zip.
func validateZip(path string) (int, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return 0, fmt.Errorf("open zip: %w", err)
	}

	defer func() { _ = archive.Close() }()

	for _, entry := range archive.File {
		reader, err := entry.Open()
		if err != nil {
			return 0, fmt.Errorf("open zip entry %s: %w", entry.Name, err)
		}

		//nolint:gosec // decompressing the user's own archive member is the integrity check itself
		if _, err := io.Copy(io.Discard, reader); err != nil {
			_ = reader.Close()

			return 0, fmt.Errorf("read zip entry %s: %w", entry.Name, err)
		}

		if err := reader.Close(); err != nil {
			return 0, fmt.Errorf("verify zip entry %s: %w", entry.Name, err)
		}
	}

	return len(archive.File), nil
}

// validateGzip streams the whole file through compress/gzip, verifying the
// CRC32 and ISIZE trailer natively. When the decompressed payload begins
// with a tar header the tar structure is walked as well, renaming gz
// payloads that are really tar archives. Returns whether the payload was a
// tar and the detail summary.
func validateGzip(path string) (bool, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, "", fmt.Errorf("open gzip: %w", err)
	}

	defer func() { _ = file.Close() }()

	stream, err := gzip.NewReader(file)
	if err != nil {
		return false, "", fmt.Errorf("read gzip header: %w", err)
	}

	defer func() { _ = stream.Close() }()

	buffered := bufio.NewReader(stream)

	isTar, err := walkGzipPayload(buffered)
	if err != nil {
		return false, "", err
	}

	// Drain the remainder so the gzip trailer (CRC32 + ISIZE) is always
	// verified, even when the tar walk stopped at the end-of-archive block.
	written, err := io.Copy(io.Discard, buffered)
	if err != nil {
		return false, "", fmt.Errorf("verify gzip stream: %w", err)
	}

	return isTar, fmt.Sprintf("gz: %d bytes", written), nil
}

// walkGzipPayload sniffs the tar magic in the decompressed head and, when
// present, walks the tar headers to the end-of-archive marker.
func walkGzipPayload(buffered *bufio.Reader) (bool, error) {
	head, peekErr := buffered.Peek(tarMagicOffset + tarMagicLen)

	switch {
	case peekErr == nil && bytes.HasPrefix(head[tarMagicOffset:], []byte("ustar")):
		return walkTarPayload(buffered)
	case peekErr == nil,
		errors.Is(peekErr, io.EOF),
		errors.Is(peekErr, io.ErrUnexpectedEOF):
		return false, nil // not a tar (or too small): plain gz, drained by the caller
	default:
		return false, fmt.Errorf("peek gzip head: %w", peekErr)
	}
}

// walkTarPayload reads the tar headers to the end-of-archive marker; every
// member is drained so tar-level damage surfaces as a format error.
func walkTarPayload(buffered *bufio.Reader) (bool, error) {
	reader := tar.NewReader(buffered)

	for {
		_, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return true, nil
		}

		if err != nil {
			return false, fmt.Errorf("read tar header: %w", err)
		}

		//nolint:gosec // decompressing the user's own tar member is the integrity check itself
		if _, err := io.Copy(io.Discard, reader); err != nil {
			return false, fmt.Errorf("read tar member: %w", err)
		}
	}
}

// validateRar applies the honest lite check: the RAR4 or RAR5 signature
// must be present, and the end-of-archive marker is scanned for in the
// final bytes where one fits. Full member integrity is out of reach
// without an external unrar, so the verdict says so.
func validateRar(path string) (Verdict, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return VerdictFormatError, "", fmt.Errorf("open rar: %w", err)
	}

	defer func() { _ = file.Close() }()

	header := make([]byte, rarHeaderLen)

	read, err := io.ReadFull(file, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return VerdictFormatError, "", fmt.Errorf("read rar header: %w", err)
	}

	if read < len(rar4Signature) ||
		(!strings.HasPrefix(string(header), rar5Signature) && !strings.HasPrefix(string(header), rar4Signature)) {
		return VerdictFormatError, "", fmt.Errorf("%s: %w", path, ErrBadRarSignature)
	}

	info, err := file.Stat()
	if err != nil {
		return VerdictFormatError, "", fmt.Errorf("stat rar: %w", err)
	}

	if info.Size() == 0 {
		return VerdictFormatError, "", fmt.Errorf("%s: %w", path, ErrEmptyRar)
	}

	if rarEndMarkerPresent(file, info.Size()) {
		return VerdictCheckedLite, "rar: end marker present", nil
	}

	return VerdictCheckedLite, "rar: signature ok, size check only (no end marker found)", nil
}

// rarEndMarkerPresent scans the final bytes for the RAR4 end-of-archive
// block header; RAR5 terminators are structurally larger, so their absence
// only downgrades the detail, never the check.
func rarEndMarkerPresent(file *os.File, size int64) bool {
	tail := make([]byte, min(rarTailLen, size))

	if _, err := file.ReadAt(tail, size-int64(len(tail))); err != nil {
		return false
	}

	return strings.Contains(string(tail), rar4EndMarker)
}
