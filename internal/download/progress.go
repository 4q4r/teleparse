package download

// Reporter receives coarse progress updates from a running Manager. The
// production implementation renders counters; tests collect them.
type Reporter interface {
	// Inc adds n to the named counter: downloaded, skipped, failed,
	// retries, bytes, hook_errors, sidecar_errors or store_errors.
	Inc(stat string, n int64)
	// SetPhase names the current pipeline phase (scanning, downloading).
	SetPhase(name string)
}

// FailureDetailReporter receives the terminal outcome of a failed item:
// the attempts consumed by the retry ladder and the last error. It is an
// optional capability probed by type assertion; a reporter implementing it
// takes over the failed-item retirement, so ItemDone(key, true) is then
// not called for that transfer.
type FailureDetailReporter interface {
	// ItemFailedDetail retires key as failed after attempts tries ended
	// with err.
	ItemFailedDetail(key string, attempts int, err error)
}

// ItemReporter receives byte-level transfer updates for the live UI. It is
// an optional capability probed by type assertion, so plain Reporter
// implementations (NoopReporter, test fakes) keep compiling untouched.
type ItemReporter interface {
	// ItemStart registers an in-flight transfer: key identifies it, name is
	// the display label, total the expected size (0 when unknown) and offset
	// the bytes a resumed .part file already holds.
	ItemStart(key, name string, total, offset int64)
	// ItemProgress adds freshly written bytes to the transfer.
	ItemProgress(key string, delta int64)
	// ItemDone retires the transfer; failed marks the outcome.
	ItemDone(key string, failed bool)
	// Throttled surfaces a server flood wait the transport is sleeping off.
	Throttled(seconds int)
}

// NoopReporter discards every update.
type NoopReporter struct{}

// Inc discards the counter update.
func (NoopReporter) Inc(string, int64) {}

// SetPhase discards the phase update.
func (NoopReporter) SetPhase(string) {}
