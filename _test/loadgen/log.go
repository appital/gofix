package main

import (
	"sync/atomic"

	"github.com/quickfixgo/quickfix"
)

// byteCounter tallies wire volume in both directions. It is attached as the
// session Log because that is the only place the engine hands out the final
// serialised bytes - counting them anywhere else would mean re-serialising the
// message inside the measured loop.
//
// The cost it adds is one atomic add per message, in place of nullLog's no-op.
type byteCounter struct {
	out     atomic.Uint64
	in      atomic.Uint64
	outMsgs atomic.Uint64
	inMsgs  atomic.Uint64

	enabled atomic.Bool
}

func (c *byteCounter) reset() {
	c.out.Store(0)
	c.in.Store(0)
	c.outMsgs.Store(0)
	c.inMsgs.Store(0)
}

// countingLog is the per-session Log. It shares one byteCounter across every
// session on a side.
type countingLog struct{ c *byteCounter }

func (l countingLog) OnIncoming(b []byte) {
	if l.c.enabled.Load() {
		l.c.in.Add(uint64(len(b)))
		l.c.inMsgs.Add(1)
	}
}

func (l countingLog) OnOutgoing(b []byte) {
	if l.c.enabled.Load() {
		l.c.out.Add(uint64(len(b)))
		l.c.outMsgs.Add(1)
	}
}

func (l countingLog) OnEvent(string)                  {}
func (l countingLog) OnEventf(string, ...interface{}) {}

type countingLogFactory struct{ c *byteCounter }

func (f countingLogFactory) Create() (quickfix.Log, error) { return countingLog{f.c}, nil }

func (f countingLogFactory) CreateSessionLog(quickfix.SessionID) (quickfix.Log, error) {
	return countingLog{f.c}, nil
}
