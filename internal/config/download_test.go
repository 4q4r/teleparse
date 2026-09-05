package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/4q4r/teleparse/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadDefaults(t *testing.T) {
	t.Parallel()

	cfg := config.Default()

	assert.Equal(t, 4, cfg.Download.Threads, "default per-file ranged threads")
	assert.Equal(t, 3, cfg.Download.Connections, "default pool connections per DC")
}

func TestDownloadValidationRanges(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		toml    string
		wantErr error
	}{
		{
			name:    "threads below one rejected",
			toml:    "[download]\nthreads = 0\n",
			wantErr: config.ErrBadThreads,
		},
		{
			name:    "threads above sixteen rejected",
			toml:    "[download]\nthreads = 17\n",
			wantErr: config.ErrBadThreads,
		},
		{
			name:    "connections below one rejected",
			toml:    "[download]\nconnections = 0\n",
			wantErr: config.ErrBadConnections,
		},
		{
			name:    "connections above eight rejected",
			toml:    "[download]\nconnections = 9\n",
			wantErr: config.ErrBadConnections,
		},
		{
			name: "turbo preset accepted",
			toml: "[download]\nthreads = 8\nconnections = 8\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, os.WriteFile(path, []byte(tc.toml), 0o600))

			_, _, err := config.Load(path)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
		})
	}
}
