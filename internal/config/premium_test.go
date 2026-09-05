package config_test

import (
	"testing"

	"github.com/4q4r/teleparse/internal/config"

	"github.com/stretchr/testify/assert"
)

// TestPremiumBoostResolutionMatrix pins the [download] premium boost
// semantics: the preset applies only when the boost is on, the account is
// premium and neither knob was customized away from the built-in defaults.
func TestPremiumBoostResolutionMatrix(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		threads     int
		connections int
		boost       bool
		premium     bool
		wantT       int
		wantC       int
	}{
		{
			name:    "boost off keeps config values",
			threads: config.DefaultThreads, connections: config.DefaultConnections,
			boost: false, premium: true,
			wantT: 4, wantC: 3,
		},
		{
			name:    "non premium account keeps defaults",
			threads: config.DefaultThreads, connections: config.DefaultConnections,
			boost: true, premium: false,
			wantT: 4, wantC: 3,
		},
		{
			name:    "premium default sizing upgrades to preset",
			threads: config.DefaultThreads, connections: config.DefaultConnections,
			boost: true, premium: true,
			wantT: 8, wantC: 8,
		},
		{
			name:    "custom threads win over preset",
			threads: 2, connections: config.DefaultConnections,
			boost: true, premium: true,
			wantT: 2, wantC: 3,
		},
		{
			name:    "custom connections win over preset",
			threads: config.DefaultThreads, connections: 6,
			boost: true, premium: true,
			wantT: 4, wantC: 6,
		},
		{
			name:    "custom sizing wins even without premium",
			threads: 16, connections: 8,
			boost: true, premium: false,
			wantT: 16, wantC: 8,
		},
		{
			name:    "turbo preset equals boost result",
			threads: 8, connections: 8,
			boost: false, premium: true,
			wantT: 8, wantC: 8,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dl := config.Download{
				Threads: tc.threads, Connections: tc.connections, PremiumBoost: tc.boost,
			}

			threads, connections := dl.Effective(tc.premium)
			assert.Equal(t, tc.wantT, threads, "effective threads")
			assert.Equal(t, tc.wantC, connections, "effective connections")
		})
	}
}

func TestDownloadPremiumBoostDefault(t *testing.T) {
	t.Parallel()

	cfg := config.Default()

	assert.True(t, cfg.Download.PremiumBoost, "premium boost defaults to on")
}
