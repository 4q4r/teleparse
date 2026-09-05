package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// Sentinel errors wrapped by dynamic credential messages.
var (
	ErrCredsMissing = errors.New("no api credentials found in the environment or the credentials file")
	ErrBadAPIID     = errors.New("api_id must be a positive integer")
	ErrBadAPIHash   = errors.New("api_hash must be exactly 32 hex characters")
)

// apiHashHexLen is the exact length of a Telegram api_hash.
const apiHashHexLen = 32

// credsFileName is the credentials file name inside the teleparse config dir.
const credsFileName = "credentials.toml"

// Credential environment variables, derived from the single TELEPARSE prefix
// registry.
const (
	envAPIIDName   = EnvVarPrefix + "_API_ID"
	envAPIHashName = EnvVarPrefix + "_API_HASH"
)

// Credentials resolves the Telegram API credentials: TELEPARSE_API_ID /
// TELEPARSE_API_HASH first, then the credentials file. source reports which
// side won ("env" or "file"); when neither resolves, err wraps
// ErrCredsMissing (or a validation sentinel naming the exact problem).
func Credentials() (int64, string, string, error) {
	rawID, rawHash := envAPIID(), envAPIHash()

	switch {
	case rawID != "" && rawHash != "":
		apiID, err := ParseAPIID(rawID)
		if err != nil {
			return 0, "", "", fmt.Errorf("%s %q: %w", envAPIIDName, rawID, err)
		}

		if err := ValidateAPIHash(rawHash); err != nil {
			return 0, "", "", fmt.Errorf("%s: %w", envAPIHashName, err)
		}

		return apiID, rawHash, "env", nil
	case rawID != "":
		return 0, "", "", fmt.Errorf("%s is set but %s is empty: %w", envAPIIDName, envAPIHashName, ErrCredsMissing)
	case rawHash != "":
		return 0, "", "", fmt.Errorf("%s is set but %s is empty: %w", envAPIHashName, envAPIIDName, ErrCredsMissing)
	}

	return credentialsFromFile()
}

// credsFile is the on-disk shape of credentials.toml.
type credsFile struct {
	App struct {
		APIID   int64  `toml:"api_id"`
		APIHash string `toml:"api_hash"`
	} `toml:"app"`
}

// credentialsFromFile loads and validates the credentials file; a missing
// file is the plain unresolved case, every other problem names the file.
func credentialsFromFile() (int64, string, string, error) {
	path := CredentialsPath()

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, "", "", fmt.Errorf("env unset and %s missing: %w", path, ErrCredsMissing)
		}

		return 0, "", "", fmt.Errorf("read %s: %w: %w", path, ErrCredsMissing, err)
	}

	file, err := decodeCredsTOML(raw)
	if err != nil {
		return 0, "", "", fmt.Errorf("parse %s: %w: %w", path, ErrCredsMissing, err)
	}

	if file.App.APIID == 0 {
		return 0, "", "", fmt.Errorf("%s: [app] api_id is missing or zero: %w", path, ErrCredsMissing)
	}

	if file.App.APIHash == "" {
		return 0, "", "", fmt.Errorf("%s: [app] api_hash is missing: %w", path, ErrCredsMissing)
	}

	if err := ValidateAPIHash(file.App.APIHash); err != nil {
		return 0, "", "", fmt.Errorf("%s: %w", path, err)
	}

	return file.App.APIID, file.App.APIHash, "file", nil
}

// decodeCredsTOML parses credentials.toml strictly: unknown keys are
// rejected so typos fail loudly.
func decodeCredsTOML(raw []byte) (credsFile, error) {
	var file credsFile

	dec := toml.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&file); err != nil {
		return credsFile{}, fmt.Errorf("%w: %w", ErrDecodeTOML, err)
	}

	return file, nil
}

// SaveCredentials validates the pair and writes it to the credentials file
// (0600, config dir 0700) with a header explaining where to get the values
// and that the file is a secret.
func SaveCredentials(apiID int64, apiHash string) error {
	if apiID <= 0 {
		return fmt.Errorf("api_id %d: %w", apiID, ErrBadAPIID)
	}

	if err := ValidateAPIHash(apiHash); err != nil {
		return err
	}

	path := CredentialsPath()

	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return fmt.Errorf("%w: %w", ErrMakeConfigDir, err)
	}

	var buf bytes.Buffer

	enc := toml.NewEncoder(&buf)
	enc.SetIndentTables(true)

	file := credsFile{}
	file.App.APIID = apiID
	file.App.APIHash = apiHash

	if err := enc.Encode(file); err != nil {
		return fmt.Errorf("%w: %w", ErrEncodeConfig, err)
	}

	header := "# teleparse api credentials\n" +
		"# Get your own at https://my.telegram.org (\"API development tools\",\n" +
		"# any app title works). This file is a secret: keep it private (0600).\n"

	if err := os.WriteFile(path, []byte(header+buf.String()), filePerm); err != nil {
		return fmt.Errorf("%w: %w", ErrWriteConfig, err)
	}

	return nil
}

// CredentialsPath returns ~/.config/teleparse/credentials.toml (respects
// XDG_CONFIG_HOME), next to config.toml.
func CredentialsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}

	return filepath.Join(dir, "teleparse", credsFileName)
}

// ParseAPIID validates and parses a raw api_id string.
func ParseAPIID(raw string) (int64, error) {
	apiID, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || apiID <= 0 {
		return 0, fmt.Errorf("%q: %w", raw, ErrBadAPIID)
	}

	return apiID, nil
}

// ValidateAPIHash requires exactly 32 hex characters.
func ValidateAPIHash(raw string) error {
	if len(raw) != apiHashHexLen {
		return fmt.Errorf("%q: %w (got %d characters)", raw, ErrBadAPIHash, len(raw))
	}

	for _, ch := range []byte(raw) {
		if !isHexDigit(ch) {
			return fmt.Errorf("%q: %w", raw, ErrBadAPIHash)
		}
	}

	return nil
}

// isHexDigit reports whether b is a hex digit.
func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// envAPIID returns the trimmed TELEPARSE_API_ID value.
func envAPIID() string {
	return strings.TrimSpace(os.Getenv(envAPIIDName))
}

// envAPIHash returns the trimmed TELEPARSE_API_HASH value.
func envAPIHash() string {
	return strings.TrimSpace(os.Getenv(envAPIHashName))
}
