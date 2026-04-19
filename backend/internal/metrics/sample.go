package metrics

import "time"

// Sample is the in-memory representation of a single probe tick for one node.
// Pointer fields are nil when the probe could not resolve that measurement
// (e.g. no disk total available yet).
//
// DiskGBTotal is a context value (needed for disk-forecast calculations); it
// has no corresponding Field enum and is not part of the queryable series set.
type Sample struct {
	NodeID      uint
	NodeName    string
	SampledAt   time.Time
	CPUPct      *float64
	MemPct      *float64
	DiskPct     *float64
	Load1       *float64
	LatencyMs   *float64
	DiskGBUsed  *float64
	DiskGBTotal *float64
	ProbeOK     bool
}
