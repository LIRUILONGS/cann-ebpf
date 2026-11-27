package metrics

import "time"

// NowFunc returns a host monotonic timestamp and a wall-clock timestamp in
// nanoseconds. Both values are sampled as close together as practical.
type NowFunc func() (monoNS int64, wallNS int64)

// SystemNow is the production clock. On Linux monoNS comes from
// CLOCK_MONOTONIC; on other platforms it falls back to process-relative time so
// local development and tests still build.
func SystemNow() (int64, int64) { return systemNow() }

func wallTime(ns int64) time.Time { return time.Unix(0, ns) }
