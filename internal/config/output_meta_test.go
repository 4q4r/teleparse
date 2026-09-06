package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/4q4r/teleparse/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOutputNamingValidation pins the accepted [output] naming styles.
func TestOutputNamingValidation(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		value   string
		wantErr bool
	}{
		"empty":       {value: ""},
		"original":    {value: "original"},
		"msgid":       {value: "msgid"},
		"typo":        {value: "message-id", wantErr: true},
		"wrong case":  {value: "MsgID", wantErr: true},
		"legacy word": {value: "filename", wantErr: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := config.Default()
			cfg.Output.Naming = tc.value

			err := cfg.Validate()

			if tc.wantErr {
				require.ErrorIs(t, err, config.ErrBadNaming)

				return
			}

			require.NoError(t, err)
		})
	}
}

// TestOutputMetadataValidation pins the accepted [output] metadata modes.
func TestOutputMetadataValidation(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		value   string
		wantErr bool
	}{
		"empty":   {value: ""},
		"chat":    {value: "chat"},
		"file":    {value: "file"},
		"off":     {value: "off"},
		"sidecar": {value: "sidecar", wantErr: true},
		"upper":   {value: "Chat", wantErr: true},
		"none":    {value: "none", wantErr: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := config.Default()
			cfg.Output.Metadata = tc.value

			err := cfg.Validate()

			if tc.wantErr {
				require.ErrorIs(t, err, config.ErrBadMetadata)

				return
			}

			require.NoError(t, err)
		})
	}
}

// TestSidecarBackCompatMapping pins the legacy bool migration: a config
// setting only output.sidecar maps onto metadata (true -> file, false ->
// off), an explicit metadata key always wins, and a config setting neither
// keeps the chat-manifest default.
func TestSidecarBackCompatMapping(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		file        string
		wantMode    string
		wantNaming  string
		description string
	}{
		"sidecar true maps to file": {
			file:        "[output]\nsidecar = true\n",
			wantMode:    config.MetadataFile,
			wantNaming:  config.NamingOriginal,
			description: "legacy sidecar users keep per-file sidecars",
		},
		"sidecar false maps to off": {
			file:        "[output]\nsidecar = false\n",
			wantMode:    config.MetadataOff,
			wantNaming:  config.NamingOriginal,
			description: "legacy opt-out stays opted out",
		},
		"explicit metadata wins over sidecar": {
			file:        "[output]\nsidecar = false\nmetadata = \"chat\"\n",
			wantMode:    config.MetadataChat,
			wantNaming:  config.NamingOriginal,
			description: "the new key takes precedence over the legacy bool",
		},
		"neither key keeps defaults": {
			file:        "[output]\ncollision = \"skip\"\n",
			wantMode:    config.MetadataChat,
			wantNaming:  config.NamingOriginal,
			description: "untouched configs adopt the chat manifest default",
		},
		"metadata alone": {
			file:        "[output]\nmetadata = \"file\"\n",
			wantMode:    config.MetadataFile,
			wantNaming:  config.NamingOriginal,
			description: "the new key works without the legacy bool",
		},
		"naming msgid": {
			file:        "[output]\nnaming = \"msgid\"\n",
			wantMode:    config.MetadataChat,
			wantNaming:  config.NamingMsgID,
			description: "naming is independent of metadata",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, os.WriteFile(path, []byte(tc.file), 0o600))

			cfg, _, err := config.Load(path)
			require.NoError(t, err, tc.description)

			assert.Equal(t, tc.wantMode, cfg.Output.Metadata, tc.description)
			assert.Equal(t, tc.wantNaming, cfg.Output.Naming, tc.description)
		})
	}
}

// TestTemplateDocumentsNamingAndMetadata pins that the first-run template
// documents both new [output] keys and the legacy sidecar mapping.
func TestTemplateDocumentsNamingAndMetadata(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")

	_, _, err := config.Load(path)
	require.NoError(t, err)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	template := string(raw)

	for _, needle := range []string{
		"# naming =",
		"# metadata =",
		"manifest.json",
		"# sidecar =",
	} {
		assert.Contains(t, template, needle, "template must document %q", needle)
	}
}
