package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/4q4r/teleparse/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redirectCredsHome points the credentials file at a temp XDG config home.
// It cannot run parallel: it mutates the process environment.
func redirectCredsHome(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))

	return filepath.Join(root, "xdg", "teleparse", "credentials.toml")
}

// unsetCredsEnv clears the credential environment for the test.
func unsetCredsEnv(t *testing.T) {
	t.Helper()

	t.Setenv("TELEPARSE_API_ID", "")
	t.Setenv("TELEPARSE_API_HASH", "")
}

// validHash is a syntactically valid 32-hex-char api_hash.
const validHash = "0123456789abcdef0123456789abcdef"

func TestCredentialsEnvBeatsFile(t *testing.T) {
	redirectCredsHome(t)

	require.NoError(t, config.SaveCredentials(111111, validHash))

	t.Setenv("TELEPARSE_API_ID", "222222")
	t.Setenv("TELEPARSE_API_HASH", strings.Repeat("a", 32))

	apiID, apiHash, source, err := config.Credentials()

	require.NoError(t, err)
	assert.Equal(t, int64(222222), apiID)
	assert.Equal(t, strings.Repeat("a", 32), apiHash)
	assert.Equal(t, "env", source)
}

func TestCredentialsFileOnly(t *testing.T) {
	redirectCredsHome(t)
	unsetCredsEnv(t)

	require.NoError(t, config.SaveCredentials(333333, validHash))

	apiID, apiHash, source, err := config.Credentials()

	require.NoError(t, err)
	assert.Equal(t, int64(333333), apiID)
	assert.Equal(t, validHash, apiHash)
	assert.Equal(t, "file", source)
}

func TestCredentialsNeitherIsUnresolved(t *testing.T) {
	redirectCredsHome(t)
	unsetCredsEnv(t)

	apiID, apiHash, source, err := config.Credentials()

	require.ErrorIs(t, err, config.ErrCredsMissing)
	assert.Equal(t, int64(0), apiID)
	assert.Empty(t, apiHash)
	assert.Empty(t, source)
}

func TestCredentialsPartialEnvNamesTheMissingVariable(t *testing.T) {
	redirectCredsHome(t)

	t.Setenv("TELEPARSE_API_ID", "222222")
	t.Setenv("TELEPARSE_API_HASH", "")

	apiID, apiHash, source, err := config.Credentials()
	require.ErrorIs(t, err, config.ErrCredsMissing)
	assert.Empty(t, apiID)
	assert.Empty(t, apiHash)
	assert.Empty(t, source)
	assert.Contains(t, err.Error(), "TELEPARSE_API_HASH")
}

func TestCredentialsBadHashLength(t *testing.T) {
	redirectCredsHome(t)

	t.Setenv("TELEPARSE_API_ID", "222222")
	t.Setenv("TELEPARSE_API_HASH", "tooshort")

	apiID, apiHash, source, err := config.Credentials()
	require.ErrorIs(t, err, config.ErrBadAPIHash)
	assert.Empty(t, apiID)
	assert.Empty(t, apiHash)
	assert.Empty(t, source)
	assert.Contains(t, err.Error(), "32")
}

func TestCredentialsBadAPIID(t *testing.T) {
	redirectCredsHome(t)

	t.Setenv("TELEPARSE_API_ID", "notanumber")
	t.Setenv("TELEPARSE_API_HASH", validHash)

	apiID, apiHash, source, err := config.Credentials()
	require.ErrorIs(t, err, config.ErrBadAPIID)
	assert.Empty(t, apiID)
	assert.Empty(t, apiHash)
	assert.Empty(t, source)
}

func TestSaveCredentialsWrites0600WithHeaderAndRoundTrips(t *testing.T) {
	path := redirectCredsHome(t)
	unsetCredsEnv(t)

	require.NoError(t, config.SaveCredentials(444444, validHash))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	dirInfo, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(raw)

	assert.Contains(t, content, "my.telegram.org")
	assert.Contains(t, content, "api_id = 444444")
	assert.Contains(t, content, "api_hash = ")
	assert.Contains(t, content, validHash)

	apiID, apiHash, source, err := config.Credentials()

	require.NoError(t, err)
	assert.Equal(t, int64(444444), apiID)
	assert.Equal(t, validHash, apiHash)
	assert.Equal(t, "file", source)
}

func TestSaveCredentialsRejectsInvalidPair(t *testing.T) {
	path := redirectCredsHome(t)

	err := config.SaveCredentials(0, "short")
	require.ErrorIs(t, err, config.ErrBadAPIID)

	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "no file must be written for invalid creds")
}

func TestCredentialsMalformedTOML(t *testing.T) {
	path := redirectCredsHome(t)
	unsetCredsEnv(t)

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("[app\nbroken"), 0o600))

	apiID, apiHash, source, err := config.Credentials()
	require.Error(t, err)
	assert.Empty(t, apiID)
	assert.Empty(t, apiHash)
	assert.Empty(t, source)
	assert.Contains(t, err.Error(), "credentials.toml")
}

func TestCredentialsFileMissingKeys(t *testing.T) {
	path := redirectCredsHome(t)
	unsetCredsEnv(t)

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("[app]\napi_id = 123\n"), 0o600))

	apiID, apiHash, source, err := config.Credentials()
	require.ErrorIs(t, err, config.ErrCredsMissing)
	assert.Empty(t, apiID)
	assert.Empty(t, apiHash)
	assert.Empty(t, source)
	assert.Contains(t, err.Error(), "api_hash")
}

func TestCredentialsFileBadHashValue(t *testing.T) {
	path := redirectCredsHome(t)
	unsetCredsEnv(t)

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path,
		[]byte("[app]\napi_id = 123\napi_hash = \"nope\"\n"), 0o600))

	apiID, apiHash, source, err := config.Credentials()
	require.ErrorIs(t, err, config.ErrBadAPIHash)
	assert.Empty(t, apiID)
	assert.Empty(t, apiHash)
	assert.Empty(t, source)
}

func TestParseAPIIDValidation(t *testing.T) {
	t.Parallel()

	id, err := config.ParseAPIID("123456")
	require.NoError(t, err)
	assert.Equal(t, int64(123456), id)

	for _, raw := range []string{"", "abc", "0", "-5", "1.5"} {
		_, err := config.ParseAPIID(raw)
		assert.ErrorIs(t, err, config.ErrBadAPIID, "input %q", raw)
	}
}

func TestValidateAPIHash(t *testing.T) {
	t.Parallel()

	require.NoError(t, config.ValidateAPIHash(validHash))
	require.NoError(t, config.ValidateAPIHash(strings.Repeat("F", 32)))

	for _, raw := range []string{"", "abc", strings.Repeat("g", 32), strings.Repeat("a", 31)} {
		err := config.ValidateAPIHash(raw)
		assert.ErrorIs(t, err, config.ErrBadAPIHash, "input len %d", len(raw))
	}
}

func TestCredentialsPathUnderConfigDir(t *testing.T) {
	t.Parallel()

	path := config.CredentialsPath()
	assert.True(t, strings.HasSuffix(path, filepath.Join("teleparse", "credentials.toml")),
		"path %q must live under the teleparse config dir", path)
}
