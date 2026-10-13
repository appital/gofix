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
	"testing"
	"time"

	"github.com/appital/gofix/internal/tape"
)

// The tests in this file pin the allocation counts of hot paths that have been
// made allocation-free, so that a regression fails `make test` rather than
// waiting to be noticed in a benchmark run. The benchmarks remain the place to
// measure; these only guard.

// assertAllocs fails if fn allocates more than max times per run on average.
func assertAllocs(t *testing.T, max float64, fn func()) {
	t.Helper()
	if testing.CoverMode() != "" {
		t.Skip("allocation counts are not meaningful with coverage instrumentation")
	}
	if got := testing.AllocsPerRun(100, fn); got > max {
		t.Errorf("got %v allocs per run, want at most %v", got, max)
	}
}

// Parsing into a reused Message with an application data dictionary must not
// allocate: repeating-group detection used to allocate per field.
func TestAllocsParseWithDataDictionaryReused(t *testing.T) {
	c := loadCorpus(t, tape.MixRealistic)
	dd := corpusDD(t)
	bufs := c.buffers()
	msg := NewMessage()

	// Warm the reused Message up on every message so field storage has grown
	// to its high-water mark before counting.
	for _, buf := range bufs {
		if err := ParseMessageWithDataDictionary(msg, buf, nil, dd); err != nil {
			t.Fatal(err)
		}
	}

	i := 0
	assertAllocs(t, 0, func() {
		if err := ParseMessageWithDataDictionary(msg, bufs[i%len(bufs)], nil, dd); err != nil {
			t.Fatal(err)
		}
		i++
	})
}

// Validating a well-formed message must not allocate, including with
// RejectInvalidMessage, which used to build a FieldValue per field.
func TestAllocsValidate(t *testing.T) {
	c := loadCorpus(t, tape.MixRealistic)
	dd := corpusDD(t)
	var parsed []*Message
	for _, buf := range c.buffers() {
		msg := NewMessage()
		if err := ParseMessageWithDataDictionary(msg, buf, nil, dd); err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, msg)
	}

	for _, reject := range []bool{false, true} {
		settings := defaultValidatorSettings
		settings.RejectInvalidMessage = reject
		validator := NewValidator(settings, dd, nil)

		i := 0
		assertAllocs(t, 0, func() {
			if err := validator.Validate(parsed[i%len(parsed)]); err != nil {
				t.Fatal(err)
			}
			i++
		})
	}
}

// Adding an entry to a RepeatingGroup costs only the Group itself: the
// template's tag ordering is built once per RepeatingGroup, not per entry.
func TestAllocsRepeatingGroupAdd(t *testing.T) {
	rg := NewRepeatingGroup(Tag(268), GroupTemplate{
		GroupElement(279), GroupElement(269), GroupElement(55), GroupElement(270),
		GroupElement(271), GroupElement(272), GroupElement(273), GroupElement(290),
		GroupElement(346), GroupElement(1023),
	})

	// Grow the groups slice first so append does not reallocate while counting.
	for range 200 {
		rg.Add()
	}
	rg.Reset()

	assertAllocs(t, 1, func() { rg.Add() })
}

// Building a populated message allocates only the output buffer and the new
// BodyLength and CheckSum values: no FieldMap copies or buffer growth.
func TestAllocsBuild(t *testing.T) {
	c := loadCorpus(t, tape.MixRealistic)

	// The first build of a message also creates its BodyLength and CheckSum
	// fields.
	for _, msg := range c.messages {
		sinkBytes = msg.build()
	}

	i := 0
	assertAllocs(t, 3, func() {
		sinkBytes = c.messages[i%len(c.messages)].build()
		i++
	})
}

// Probing for an absent field must not pay for a formatted reject error. Tags
// below 256 box without allocating; larger ones cost one small allocation.
func TestAllocsFieldMapMiss(t *testing.T) {
	msg := NewMessage()

	assertAllocs(t, 0, func() {
		if _, err := msg.Header.GetBytes(tagSenderSubID); err == nil {
			t.Fatal("expected a miss")
		}
	})
	assertAllocs(t, 1, func() {
		if _, err := msg.Header.GetBytes(tagDeliverToLocationID); err == nil {
			t.Fatal("expected a miss")
		}
	})
}

// Reversing the route of a message only pays for the fields it copies; the
// optional routing fields that are absent cost nothing.
func TestAllocsReverseRoute(t *testing.T) {
	msg := NewMessage()
	msg.Header.SetString(tagBeginString, BeginStringFIX44)
	msg.Header.SetString(tagSenderCompID, "SENDER")
	msg.Header.SetString(tagTargetCompID, "TARGET")

	// The floor is what building the reversed header directly costs.
	floor := testing.AllocsPerRun(100, func() {
		reversed := NewMessage()
		reversed.Header.SetBytes(tagTargetCompID, []byte("SENDER"))
		reversed.Header.SetBytes(tagSenderCompID, []byte("TARGET"))
		sinkMsg = reversed // Keep the Message on the heap, as reverseRoute's is.
	})
	assertAllocs(t, floor, func() { sinkMsg = msg.reverseRoute() })
}

// An empty Message is a single allocation: its section maps are created on
// first use, and its three locks live in the same block as the struct.
func TestAllocsNewMessage(t *testing.T) {
	assertAllocs(t, 1, func() { sinkMsg = NewMessage() })
}

// Populating a fresh Message costs one allocation per field for its wire
// bytes. Storage for the fields grows in chunks rather than per field, and
// costs 17 allocations for this 31-field body, most of them map growth.
func TestAllocsPopulateFresh(t *testing.T) {
	fields := loadCorpus(t, tape.MixRealistic).subset(tape.MsgTypeExecutionReport).entries[0].Body
	values := make([][]byte, len(fields))
	for i, f := range fields {
		values[i] = []byte(f.Value)
	}

	assertAllocs(t, float64(len(fields)+17), func() {
		m := NewMessage()
		for i, f := range fields {
			m.Body.SetBytes(Tag(f.Tag), values[i])
		}
		sinkMsg = m
	})
}

// Parsing into a fresh Message sizes its sections from the field count, so
// the cost barely grows with the number of fields: it was 15 allocations for
// a Heartbeat and 29 for an ExecutionReport when the sections grew per field.
func TestAllocsParseFresh(t *testing.T) {
	c := loadCorpus(t, tape.MixRealistic)
	for name, msgType := range map[string]string{
		"Heartbeat":             tape.MsgTypeHeartbeat,
		"ExecutionReport":       tape.MsgTypeExecutionReport,
		"MarketDataIncremental": tape.MsgTypeMarketDataIncrementalRefresh,
	} {
		t.Run(name, func(t *testing.T) {
			buf := c.subset(msgType).buffers()[0]
			assertAllocs(t, 12, func() {
				msg := NewMessage()
				if err := ParseMessage(msg, buf); err != nil {
					t.Fatal(err)
				}
				sinkMsg = msg
			})
		})
	}
}

// With ReuseIncomingMessages, receiving a message parses into the session's
// reused Message instead of a fresh one; it was 12 allocations without.
func TestAllocsSessionIncomingReused(t *testing.T) {
	c := loadCorpus(t, tape.MixRealistic)
	bs := newBenchSession(t, false, nil, false)
	bs.session.ReuseIncomingMessages = true
	bufs := bs.inbound(c)
	now := time.Now()

	i := 0
	receive := func() {
		idx := i % len(bufs)
		if idx == 0 {
			if err := bs.session.store.SetNextTargetMsgSeqNum(1); err != nil {
				t.Fatal(err)
			}
		}
		bs.session.Incoming(bs.session, fixIn{bytes: bufs[idx], receiveTime: now})
		i++
	}

	// Grow the reused Message to its high-water mark first.
	for range bufs {
		receive()
	}
	assertAllocs(t, 1, receive)

	if bs.app.received.Load() == 0 {
		t.Fatal("no messages reached the application")
	}
}
