package verify_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/4q4r/teleparse/internal/verify"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeZipFixture builds a real two-entry zip archive on disk and returns
// its path.
func writeZipFixture(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "archive.zip")

	out, err := os.Create(path)
	require.NoError(t, err)

	writer := zip.NewWriter(out)

	entry, err := writer.CreateHeader(&zip.FileHeader{Name: "notes.txt", Method: zip.Deflate})
	require.NoError(t, err)

	_, err = entry.Write([]byte(strings.Repeat("teleparse verify payload ", 64)))
	require.NoError(t, err)

	second, err := writer.CreateHeader(&zip.FileHeader{Name: "bin/data.bin", Method: zip.Deflate})
	require.NoError(t, err)

	_, err = second.Write(bytes.Repeat([]byte{0xA5}, 512))
	require.NoError(t, err)

	require.NoError(t, writer.Close())
	require.NoError(t, out.Close())

	return path
}

// writeTarGzFixture builds a real tar.gz archive with one regular file and
// returns its path.
func writeTarGzFixture(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name)

	out, err := os.Create(path)
	require.NoError(t, err)

	compressed := gzip.NewWriter(out)
	writer := tar.NewWriter(compressed)

	content := strings.Repeat("member payload\n", 128)

	require.NoError(t, writer.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: "report.txt", Mode: 0o600, Size: int64(len(content)),
	}))

	_, err = writer.Write([]byte(content))
	require.NoError(t, err)

	require.NoError(t, writer.Close())
	require.NoError(t, compressed.Close())
	require.NoError(t, out.Close())

	return path
}

// flipByte rewrites one byte of the file at offset so corruption fixtures
// share one code path.
func flipByte(t *testing.T, path string, offset int) {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	raw[offset] ^= 0xFF

	require.NoError(t, os.WriteFile(path, raw, 0o600))
}

func TestValidateFileValidZipIsOK(t *testing.T) {
	t.Parallel()

	verdict, err := verify.ValidateFile(writeZipFixture(t, t.TempDir()), ".zip")

	require.NoError(t, err)
	assert.Equal(t, verify.VerdictOK, verdict)
}

func TestValidateFileTruncatedZipReportsFormatError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeZipFixture(t, dir)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	// Cutting the tail removes the end-of-central-directory record.
	require.NoError(t, os.WriteFile(path, raw[:len(raw)-12], 0o600))

	verdict, err := verify.ValidateFile(path, ".zip")

	require.Error(t, err, "a zip without its central directory must fail to open")
	assert.Equal(t, verify.VerdictFormatError, verdict)
}

func TestValidateFileCorruptedZipMemberFailsCRC(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeZipFixture(t, dir)

	// Local header is 30 bytes plus the 9-byte entry name, so offset 40
	// lands inside the deflate stream of the first member.
	flipByte(t, path, 40)

	verdict, err := verify.ValidateFile(path, ".zip")

	require.Error(t, err, "walking the member must surface the CRC or flate failure")
	assert.Equal(t, verify.VerdictFormatError, verdict)
}

func TestValidateFileValidTarGzIsOK(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	verdict, err := verify.ValidateFile(writeTarGzFixture(t, dir, "bundle.tar.gz"), ".gz")
	require.NoError(t, err)
	assert.Equal(t, verify.VerdictOK, verdict)

	verdict, err = verify.ValidateFile(writeTarGzFixture(t, dir, "bundle.tgz"), ".tgz")
	require.NoError(t, err)
	assert.Equal(t, verify.VerdictOK, verdict)
}

func TestValidateFileCorruptedGzMemberFailsCRC(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeTarGzFixture(t, dir, "broken.tar.gz")

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	// The middle of the file sits deep inside the deflate stream, well
	// past the 10-byte gzip header and before the 8-byte trailer.
	flipByte(t, path, len(raw)/2)

	verdict, err := verify.ValidateFile(path, ".gz")

	require.Error(t, err, "decompression must surface the corrupt member")
	assert.Equal(t, verify.VerdictFormatError, verdict)
}

func TestValidateFilePlainGzIsDecompressed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "plain.txt.gz")

	out, err := os.Create(path)
	require.NoError(t, err)

	compressed := gzip.NewWriter(out)

	_, err = compressed.Write([]byte(strings.Repeat("just text\n", 256)))
	require.NoError(t, err)

	require.NoError(t, compressed.Close())
	require.NoError(t, out.Close())

	verdict, err := verify.ValidateFile(path, ".gz")

	require.NoError(t, err)
	assert.Equal(t, verify.VerdictOK, verdict)
}

func TestValidateFileRarMagicPositiveAndNegative(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// RAR4 signature (7 bytes) plus filler standing in for archive blocks.
	rar4 := filepath.Join(dir, "old.rar")
	require.NoError(t, os.WriteFile(rar4,
		append([]byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x00}, bytes.Repeat([]byte{0x00}, 32)...), 0o600))

	verdict, err := verify.ValidateFile(rar4, ".rar")
	require.NoError(t, err)
	assert.Equal(t, verify.VerdictCheckedLite, verdict)

	// RAR5 signature (8 bytes) plus filler.
	rar5 := filepath.Join(dir, "new.rar")
	require.NoError(t, os.WriteFile(rar5,
		append([]byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x01, 0x00}, bytes.Repeat([]byte{0xAB}, 32)...), 0o600))

	verdict, err = verify.ValidateFile(rar5, ".rar")
	require.NoError(t, err)
	assert.Equal(t, verify.VerdictCheckedLite, verdict)

	fake := filepath.Join(dir, "fake.rar")
	require.NoError(t, os.WriteFile(fake, bytes.Repeat([]byte("not a rar at all"), 4), 0o600))

	verdict, err = verify.ValidateFile(fake, ".rar")

	require.Error(t, err, "a file without the RAR signature is not a rar")
	assert.Equal(t, verify.VerdictFormatError, verdict)
}

func TestValidateFileRarEndMarkerDetected(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// RAR4 signature followed by filler and the end-of-archive block, so
	// the lite scan finds the terminator in the final bytes.
	body := append([]byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x00}, bytes.Repeat([]byte{0x11}, 16)...)
	body = append(body, 0xC4, 0x3D, 0x7B, 0x00, 0x40, 0x07, 0x00)

	path := filepath.Join(dir, "complete.rar")
	require.NoError(t, os.WriteFile(path, body, 0o600))

	verdict, detail, err := verify.ValidateFileDetailed(path, ".rar")

	require.NoError(t, err)
	assert.Equal(t, verify.VerdictCheckedLite, verdict)
	assert.Contains(t, detail, "end marker")
}

func TestValidateFileUnknownExtensionIsUnchecked(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte{0x00}, 256), 0o600))

	verdict, err := verify.ValidateFile(path, ".mkv")

	require.NoError(t, err)
	assert.Equal(t, verify.VerdictUnsupported, verdict)
}
