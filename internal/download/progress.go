package download

// Reporter receives coarse progress updates from a running Manager. The
// production implementation renders counters; tests collect them.
type Reporter interface {
	// Inc adds n to the named counter: downloaded, skipped, failed, bytes,
	// hook_errors, sidecar_errors or store_errors.
	Inc(stat string, n int64)
	// SetPhase names the current pipeline phase (scanning, downloading).
	SetPhase(name string)
}

// NoopReporter discards every update.
type NoopReporter struct{}

// Inc discards the counter update.
func (NoopReporter) Inc(string, int64) {}

// SetPhase discards the phase update.
func (NoopReporter) SetPhase(string) {}
