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
