package filters

import "errors"

// Sentinel errors for plan compilation and file-requiring predicates.
var (
	ErrNoFile  = errors.New("message has no file")
	ErrCompile = errors.New("cannot compile filters")
)
