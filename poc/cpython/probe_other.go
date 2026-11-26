//go:build !linux || !arm64

package cpython

import (
	"context"
	"fmt"
	"time"
)

type Event struct {
	TsNS      uint64
	Dict      uint64
	PID       uint32
	TID       uint32
	Index     uint32
	Total     uint32
	Nentries  uint32
	Kind      uint32
	Truncated uint32
	IDLen     uint32
	ID        [48]byte
}

func (e Event) RequestID() string { return "" }

type Stats struct {
	Entries        uint64
	NameMatches    uint64
	Returns        uint64
	NullReturns    uint64
	Parsed         uint64
	Emitted        uint64
	Skipped        uint64
	ReadErrors     uint64
	SplitDict      uint64
	NonUnicodeDict uint64
	EmptyDict      uint64
	RingbufLost    uint64
}

type Options struct {
	PID       int
	Duration  time.Duration
	LibPath   string
	MaxEvents int
}

type Result struct {
	PID      int
	LibPath  string
	Events   []Event
	Stats    Stats
	Warnings []string
}

func Run(context.Context, Options) (*Result, error) {
	return nil, fmt.Errorf("CPython POC requires linux/arm64")
}
