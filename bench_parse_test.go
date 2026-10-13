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
	"testing"

	"github.com/appital/gofix/datadictionary"
	"github.com/appital/gofix/internal/tape"
)

// BenchmarkParse measures ParseMessage with no data dictionary.
//
// The fresh/reused split is the important one. "fresh" allocates a Message per
// message, which is what session_state.go does for every inbound message on a
// live session. "reused" parses into one Message repeatedly, which hits the
// field-arena reuse in doParsing. The gap between them is the ceiling on what
// pooling inbound Messages could recover.
func BenchmarkParse(b *testing.B) {
	c := loadCorpus(b, tape.MixRealistic)

	forEachMsgType(b, c, func(b *testing.B, c *corpus) {
		b.Run("fresh", func(b *testing.B) { benchParse(b, c, nil, false) })
		b.Run("reused", func(b *testing.B) { benchParse(b, c, nil, true) })
	})
}

// BenchmarkParseWithDD measures the same path with an application data
// dictionary, which switches on repeating-group detection in doParsing.
func BenchmarkParseWithDD(b *testing.B) {
	c := loadCorpus(b, tape.MixRealistic)
	dd := corpusDD(b)

	forEachMsgType(b, c, func(b *testing.B, c *corpus) {
		b.Run("fresh", func(b *testing.B) { benchParse(b, c, dd, false) })
		b.Run("reused", func(b *testing.B) { benchParse(b, c, dd, true) })
	})
}

func benchParse(b *testing.B, c *corpus, dd *datadictionary.DataDictionary, reuse bool) {
	bufs := c.buffers()
	reused := NewMessage()

	b.SetBytes(c.bytesPerMsg())
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		msg := reused
		if !reuse {
			msg = NewMessage()
		}

		if err := ParseMessageWithDataDictionary(msg, bufs[i%len(bufs)], nil, dd); err != nil {
			b.Fatal(err)
		}
		sinkMsg = msg
	}

	reportPerField(b, c)
}

// BenchmarkParserReadMessage measures message framing only: finding message
// boundaries in a byte stream and copying each one out. It reuses a single
// parser across iterations so the steady-state buffer shifting in readMore is
// what gets measured, rather than parser construction.
func BenchmarkParserReadMessage(b *testing.B) {
	c := loadCorpus(b, tape.MixRealistic)

	for _, name := range []string{"realistic", "marketdata"} {
		mix := tape.Mixes[name]
		streamCorpus := c
		if name != c.name {
			streamCorpus = loadCorpus(b, mix)
		}

		b.Run("mix="+name, func(b *testing.B) {
			p := newParser(&loopReader{data: streamCorpus.stream})

			b.SetBytes(streamCorpus.bytesPerMsg())
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				msg, err := p.ReadMessage()
				if err != nil {
					b.Fatal(err)
				}
				sinkInt = msg.Len()
			}
		})

		// As a session with ReuseIncomingMessages does, hand each buffer back
		// once the message is processed.
		b.Run("mix="+name+"/recycled", func(b *testing.B) {
			p := newParser(&loopReader{data: streamCorpus.stream})

			b.SetBytes(streamCorpus.bytesPerMsg())
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				msg, err := p.ReadMessage()
				if err != nil {
					b.Fatal(err)
				}
				sinkInt = msg.Len()
				releaseRawBuffer(msg)
			}
		})
	}
}

// BenchmarkReadAndParse is framing plus parsing: the whole inbound cost from
// socket bytes to a populated Message, minus the syscall.
func BenchmarkReadAndParse(b *testing.B) {
	c := loadCorpus(b, tape.MixRealistic)

	for _, withDD := range []bool{false, true} {
		// See BenchmarkRoundTrip: the dictionary is parsed lazily so it stays
		// out of the dd=off allocation profile.
		name := "dd=off"
		appDD := (*datadictionary.DataDictionary)(nil)
		if withDD {
			name, appDD = "dd=on", corpusDD(b)
		}

		b.Run(name, func(b *testing.B) {
			p := newParser(&loopReader{data: c.stream})

			b.SetBytes(c.bytesPerMsg())
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				raw, err := p.ReadMessage()
				if err != nil {
					b.Fatal(err)
				}

				msg := NewMessage()
				if err := ParseMessageWithDataDictionary(msg, raw, nil, appDD); err != nil {
					b.Fatal(err)
				}
				sinkMsg = msg
			}
		})
	}
}

// BenchmarkExtractField isolates the innermost parse loop: locating one field's
// SOH and splitting it into tag and value.
func BenchmarkExtractField(b *testing.B) {
	c := loadCorpus(b, tape.MixRealistic)
	// Skip 8= and 9=, which doParsing handles separately.
	raw := c.subset(tape.MsgTypeExecutionReport).wire[0]
	body := raw[bytes.Index(raw, []byte("\x0135="))+1:]

	var tv TagValue

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		rem := body
		for len(rem) > 0 {
			var err error
			if rem, err = extractField(&tv, rem); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkGetField measures field access on a parsed message, including the
// miss path - probing for an absent optional field returns a reject error,
// which allocates only for tags of 256 and above.
func BenchmarkGetField(b *testing.B) {
	c := loadCorpus(b, tape.MixRealistic).subset(tape.MsgTypeExecutionReport)

	msg := NewMessage()
	if err := ParseMessage(msg, bytes.NewBuffer(c.wire[0])); err != nil {
		b.Fatal(err)
	}

	b.Run("hit/GetBytes", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkBytes, sinkErr = msg.Body.GetBytes(Tag(tape.TagClOrdID))
		}
	})

	b.Run("hit/GetString", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var s string
			s, sinkErr = msg.Body.GetString(Tag(tape.TagClOrdID))
			sinkInt = len(s)
		}
	})

	b.Run("hit/GetInt", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt, sinkErr = msg.Body.GetInt(Tag(tape.TagOrderQty))
		}
	})

	b.Run("miss/GetBytes", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkBytes, sinkErr = msg.Body.GetBytes(Tag(tape.TagQuoteEntryID))
		}
	})
}

// loopReader replays a byte stream endlessly, standing in for a socket that
// never runs dry. The stream is a concatenation of whole FIX messages, so
// wrapping around lands exactly on a message boundary.
type loopReader struct {
	data []byte
	off  int
}

func (r *loopReader) Read(p []byte) (int, error) {
	n := copy(p, r.data[r.off:])
	r.off += n
	if r.off >= len(r.data) {
		r.off = 0
	}
	return n, nil
}

// reportPerField normalises a result by field count, which makes message
// classes of very different sizes comparable.
func reportPerField(b *testing.B, c *corpus) {
	b.Helper()

	fieldsPerMsg := float64(c.totalFields) / float64(len(c.entries))
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/fieldsPerMsg, "ns/field")
}
