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

package quickfix

import (
	"bytes"
	"strings"
	"testing"

	"github.com/quickfixgo/quickfix/internal/tape"
)

func readable(b []byte) string { return strings.ReplaceAll(string(b), "\x01", "|") }

// TestCorpusBuildMatchesWire is the harness's own correctness gate. The tape's
// encoder and the library's builder are written independently, so agreement
// between them means the corpus really is what it claims to be.
func TestCorpusBuildMatchesWire(t *testing.T) {
	c := loadCorpus(t, tape.MixRealistic)

	for i, msg := range c.messages {
		got := msg.build()
		if !bytes.Equal(got, c.wire[i]) {
			t.Fatalf("entry %d (MsgType %q) build/wire mismatch\n build: %s\n  wire: %s",
				i, c.entries[i].MsgType, readable(got), readable(c.wire[i]))
		}
	}
}

// TestCorpusRoundTrip parses every corpus message and rebuilds it, asserting the
// bytes survive the round trip. This exercises the exact parse->build path the
// benchmarks measure.
func TestCorpusRoundTrip(t *testing.T) {
	c := loadCorpus(t, tape.MixRealistic)
	dd := corpusDD(t)

	for i, w := range c.wire {
		msg := NewMessage()
		if err := ParseMessageWithDataDictionary(msg, bytes.NewBuffer(w), nil, dd); err != nil {
			t.Fatalf("entry %d (MsgType %q): parse failed: %v\n%s",
				i, c.entries[i].MsgType, err, readable(w))
		}

		if got := msg.build(); !bytes.Equal(got, w) {
			t.Fatalf("entry %d (MsgType %q) round-trip mismatch\n   got: %s\n  want: %s",
				i, c.entries[i].MsgType, readable(got), readable(w))
		}
	}
}

// TestCorpusValidates checks the corpus against the FIX 4.4 data dictionary. If
// this fails the tape contains tags or enum values that are not legal FIX 4.4,
// which would make the validation benchmarks measure the reject path instead of
// the happy path.
func TestCorpusValidates(t *testing.T) {
	c := loadCorpus(t, tape.MixRealistic)
	dd := corpusDD(t)
	validator := NewValidator(defaultValidatorSettings, dd, nil)

	for i, w := range c.wire {
		msg := NewMessage()
		if err := ParseMessageWithDataDictionary(msg, bytes.NewBuffer(w), nil, dd); err != nil {
			t.Fatalf("entry %d: parse failed: %v", i, err)
		}

		if err := validator.Validate(msg); err != nil {
			t.Errorf("entry %d (MsgType %q) rejected: %v (tag %v)\n%s",
				i, c.entries[i].MsgType, err, err.RefTagID(), readable(w))
		}
	}
}

// TestCorpusShape guards the assumptions the benchmark names and the baseline
// numbers rest on: every modelled message class is present, and the group-heavy
// classes really do carry groups.
func TestCorpusShape(t *testing.T) {
	c := loadCorpus(t, tape.MixRealistic)

	for _, msgType := range tape.MixRealistic.MsgTypes() {
		sub := c.subset(msgType)
		if len(sub.entries) == 0 {
			t.Errorf("MsgType %q absent from corpus of %d entries", msgType, corpusSize)
		}
	}

	md := c.subset(tape.MsgTypeMarketDataIncrementalRefresh)
	for i, e := range md.entries {
		if len(e.Groups) == 0 || len(e.Groups[0].Entries) < 10 {
			t.Fatalf("market data entry %d has no substantial repeating group", i)
		}
	}

	if avg := c.totalFields / len(c.entries); avg < 20 {
		t.Errorf("corpus averages %d fields/message, expected a realistic >=20", avg)
	}
}
