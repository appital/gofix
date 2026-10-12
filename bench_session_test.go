// Copyright (c) quickfixengine.org  All rights reserved.
//
// This file may be distributed under the terms of the quickfixengine.org
// license as defined by quickfixengine.org and appearing in the file
// LICENSE included in the packaging of this file.
//
// This file is provided AS IS with NO WARRANTY OF ANY KIND, INCLUDING
// THE WARRANTY OF DESIGN, MERCHANTABILITY AND FITNESS FOR A
// PARTICULAR PURPOSE.
//
// See http://www.quickfixengine.org/LICENSE for licensing information.
//
// Contact ask@quickfixengine.org if any conditions of this licensing
// are not clear to you.

package gofix

import (
	"bytes"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appital/gofix/config"
	"github.com/appital/gofix/datadictionary"
	"github.com/appital/gofix/internal"
	"github.com/appital/gofix/internal/tape"
)

// benchApp is a no-op Application. The testify-based MockApp in quickfix_test.go
// records every call through reflection, which would dominate any measurement.
type benchApp struct {
	echo     bool
	received atomic.Int64
}

func (a *benchApp) OnCreate(SessionID)              {}
func (a *benchApp) OnLogon(SessionID)               {}
func (a *benchApp) OnLogout(SessionID)              {}
func (a *benchApp) ToAdmin(*Message, SessionID)     {}
func (a *benchApp) ToApp(*Message, SessionID) error { return nil }
func (a *benchApp) FromAdmin(*Message, SessionID) MessageRejectError {
	a.received.Add(1)
	return nil
}

func (a *benchApp) FromApp(msg *Message, sessionID SessionID) MessageRejectError {
	a.received.Add(1)
	if !a.echo {
		return nil
	}

	// Reflect the message back the way a real application would: reverse the
	// routing, stamp it, send. This puts the outbound build and persist path
	// inside the measurement.
	reply := msg.reverseRoute()
	reply.Header.SetString(tagMsgType, "8")
	reply.Body.SetString(Tag(tape.TagText), "ack")

	// A send failure here means the harness is misconfigured, not that the
	// message was rejected; the benchmark's delivered/op check will catch it.
	_ = SendToTarget(reply, sessionID)

	return nil
}

// benchSession builds a fully configured session that is wired to channels
// rather than a socket, and parked in the in-session state so inbound messages
// take the normal application path.
//
// Session construction goes through sessionFactory so every setting the engine
// reads is populated; only the transport is faked.
type benchSession struct {
	session  *session
	app      *benchApp
	out      chan []byte
	drainer  chan struct{}
	unregist func()
}

// peer returns the tape session an inbound message must claim to come from.
func (bs *benchSession) peer() tape.Session {
	return tape.Session{
		BeginString:  bs.session.sessionID.BeginString,
		SenderCompID: bs.session.sessionID.TargetCompID,
		TargetCompID: bs.session.sessionID.SenderCompID,
	}
}

// inbound renders the corpus as messages arriving from this session's peer,
// numbered from 1 so they satisfy sequence checking on each pass.
func (bs *benchSession) inbound(c *corpus) []*bytes.Buffer {
	wire := tape.WireAll(c.entries, 1, bs.peer())

	bufs := make([]*bytes.Buffer, len(wire))
	for i, w := range wire {
		bufs[i] = bytes.NewBuffer(w)
	}
	return bufs
}

var benchSessionSeq atomic.Int64

func newBenchSession(tb testing.TB, echo bool, dd *datadictionary.DataDictionary, persist bool) *benchSession {
	tb.Helper()

	// The session registry is a package-level map, so every session in a
	// benchmark run needs a distinct SessionID.
	// Inbound messages are checked with the CompIDs reversed: a message's
	// SenderCompID must equal the session's TargetCompID. The tape is rendered
	// to match, further down.
	suffix := strconv.FormatInt(benchSessionSeq.Add(1), 10)
	sessionID := SessionID{
		BeginString:  "FIX.4.4",
		SenderCompID: "BENCH_LOCAL_" + suffix,
		TargetCompID: "BENCH_PEER_" + suffix,
	}

	settings := NewSessionSettings()
	settings.Set(config.BeginString, sessionID.BeginString)
	settings.Set(config.SenderCompID, sessionID.SenderCompID)
	settings.Set(config.TargetCompID, sessionID.TargetCompID)
	settings.Set(config.HeartBtInt, "30")
	// A benchmark run outlives the default 120s latency window, and the tape's
	// timestamps are fixed anyway.
	settings.Set(config.CheckLatency, "N")
	if !persist {
		settings.Set(config.PersistMessages, "N")
	}

	app := &benchApp{echo: echo}

	s, err := sessionFactory{}.newSession(
		sessionID, NewMemoryStoreFactory(), settings, nullLogFactory{}, app)
	if err != nil {
		tb.Fatalf("building bench session: %v", err)
	}

	s.appDataDictionary = dd
	if dd != nil {
		s.Validator = NewValidator(defaultValidatorSettings, dd, nil)
	}

	out := make(chan []byte, 1024)
	s.messageOut = out
	s.messageEvent = make(chan bool, 1)
	s.sessionEvent = make(chan internal.Event, 1)
	s.admin = make(chan any, 1)

	// run() normally creates these; Incoming resets peerTimer unconditionally.
	s.stateTimer = internal.NewEventTimer(func() {})
	s.peerTimer = internal.NewEventTimer(func() {})

	// Park in the in-session state so inbound app messages reach the
	// application rather than being answered with a logon.
	s.State = inSession{}

	bs := &benchSession{session: s, app: app, out: out, drainer: make(chan struct{})}

	// Drain outbound bytes; a full channel would otherwise block the sender.
	go func() {
		for {
			select {
			case <-out:
			case <-bs.drainer:
				return
			}
		}
	}()

	if err := registerSession(s); err != nil {
		tb.Fatalf("registering bench session: %v", err)
	}
	bs.unregist = func() { UnregisterSession(sessionID) }

	tb.Cleanup(bs.stop)

	return bs
}

func (bs *benchSession) stop() {
	close(bs.drainer)
	bs.session.stateTimer.Stop()
	bs.session.peerTimer.Stop()
	bs.unregist()
}

// BenchmarkSessionIncoming drives the full inbound session path - parse, verify,
// validate, application callback - with no sockets and no goroutine scheduling.
// It is the bridge between the per-layer benchmarks and the full-stack loadgen:
// everything the engine does per inbound message, and nothing the OS does.
func BenchmarkSessionIncoming(b *testing.B) {
	c := loadCorpus(b, tape.MixRealistic)
	dd := corpusDD(b)

	cases := []struct {
		name    string
		echo    bool
		dd      *datadictionary.DataDictionary
		persist bool
	}{
		{name: "receive/dd=off", echo: false, dd: nil, persist: false},
		{name: "receive/dd=on", echo: false, dd: dd, persist: false},
		{name: "echo/dd=off/persist=off", echo: true, dd: nil, persist: false},
		{name: "echo/dd=off/persist=on", echo: true, dd: nil, persist: true},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			bs := newBenchSession(b, tc.echo, tc.dd, tc.persist)
			bufs := bs.inbound(c)
			now := time.Now()

			b.SetBytes(c.bytesPerMsg())
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				idx := i % len(bufs)

				// The tape is replayed in a loop, so rewind the expected
				// sequence number each time it wraps rather than per message.
				if idx == 0 {
					b.StopTimer()
					if err := bs.session.store.SetNextTargetMsgSeqNum(1); err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
				}

				bs.session.Incoming(bs.session, fixIn{bytes: bufs[idx], receiveTime: now})
			}

			b.StopTimer()
			if bs.app.received.Load() == 0 {
				b.Fatal("no messages reached the application - the session rejected the tape")
			}
			b.ReportMetric(float64(bs.app.received.Load())/float64(b.N), "delivered/op")
		})
	}
}

// BenchmarkSessionOutbound measures queueForSend: header population, ToApp
// callback, build, persist, and the queue append.
func BenchmarkSessionOutbound(b *testing.B) {
	c := loadCorpus(b, tape.MixRealistic)

	for _, persist := range []bool{false, true} {
		b.Run("persist="+strconv.FormatBool(persist), func(b *testing.B) {
			bs := newBenchSession(b, false, nil, persist)

			b.SetBytes(c.bytesPerMsg())
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				idx := i % len(c.entries)
				msg := messageFromEntry(c.entries[idx], idx+1, tape.DefaultSession)

				if err := bs.session.queueForSend(msg); err != nil {
					b.Fatal(err)
				}
				bs.session.dropQueued()
			}
		})
	}
}
