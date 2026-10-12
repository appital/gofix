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

	"github.com/appital/gofix/internal/tape"
)

// BenchmarkBuild serialises an already-populated Message. This is the outbound
// hot path minus field population: checksum, body length, three sorts, three
// map walks per section, and the buffer copy.
func BenchmarkBuild(b *testing.B) {
	c := loadCorpus(b, tape.MixRealistic)

	forEachMsgType(b, c, func(b *testing.B, c *corpus) {
		b.SetBytes(c.bytesPerMsg())
		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			sinkBytes = c.messages[i%len(c.messages)].build()
		}

		reportPerField(b, c)
	})
}

// BenchmarkNewAndBuild is the full outbound cost: construct a Message, populate
// every field, serialise. This is what an application pays per message it sends.
func BenchmarkNewAndBuild(b *testing.B) {
	c := loadCorpus(b, tape.MixRealistic)

	forEachMsgType(b, c, func(b *testing.B, c *corpus) {
		b.SetBytes(c.bytesPerMsg())
		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			idx := i % len(c.entries)
			sinkBytes = messageFromEntry(c.entries[idx], idx+1, tape.DefaultSession).build()
		}

		reportPerField(b, c)
	})
}

// BenchmarkNewMessage measures the fixed cost of an empty Message: one struct,
// three mutexes and three maps, before a single field exists.
func BenchmarkNewMessage(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		sinkMsg = NewMessage()
	}
}

// BenchmarkSetField isolates the setters, which is where TagValue.init runs.
func BenchmarkSetField(b *testing.B) {
	const value = "SOME-ORDER-ID-000123"

	b.Run("SetBytes", func(b *testing.B) {
		m := NewMessage()
		v := []byte(value)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			m.Body.SetBytes(Tag(tape.TagClOrdID), v)
		}
	})

	b.Run("SetString", func(b *testing.B) {
		m := NewMessage()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			m.Body.SetString(Tag(tape.TagClOrdID), value)
		}
	})

	b.Run("SetInt", func(b *testing.B) {
		m := NewMessage()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			m.Body.SetInt(Tag(tape.TagOrderQty), 12345)
		}
	})

	b.Run("SetField", func(b *testing.B) {
		m := NewMessage()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			m.Body.SetField(Tag(tape.TagClOrdID), FIXString(value))
		}
	})

	// Populating a fresh map each time, rather than overwriting one tag,
	// exposes the map-growth and tags-slice-growth cost that the single-tag
	// variants above hide.
	b.Run("populate/ExecutionReport", func(b *testing.B) {
		fields := loadCorpus(b, tape.MixRealistic).subset(tape.MsgTypeExecutionReport).entries[0].Body

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			m := NewMessage()
			for _, f := range fields {
				m.Body.SetString(Tag(f.Tag), f.Value)
			}
			sinkMsg = m
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(fields)), "ns/field")
	})
}

// BenchmarkTagValue compares the two ways a TagValue gets populated: init on
// the outbound path, which builds the wire bytes with successive appends from
// nil, and parse on the inbound path, which sub-slices the raw buffer.
func BenchmarkTagValue(b *testing.B) {
	value := []byte("SOME-ORDER-ID-000123")
	raw := []byte("11=SOME-ORDER-ID-000123\x01")

	b.Run("init", func(b *testing.B) {
		var tv TagValue
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			tv.init(Tag(tape.TagClOrdID), value)
		}
		sinkBytes = tv.bytes
	})

	b.Run("parse", func(b *testing.B) {
		var tv TagValue
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := tv.parse(raw); err != nil {
				b.Fatal(err)
			}
		}
		sinkBytes = tv.bytes
	})
}

// BenchmarkFieldMap breaks build() into its three passes over each section.
// build() runs length and total once per section and write once, so the sum of
// these times three sections is roughly BenchmarkBuild.
func BenchmarkFieldMap(b *testing.B) {
	c := loadCorpus(b, tape.MixRealistic).subset(tape.MsgTypeExecutionReport)
	msg := c.messages[0]

	b.Run("length", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt = msg.Body.length()
		}
	})

	b.Run("total", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt = msg.Body.total()
		}
	})

	b.Run("sortedTags", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt = len(msg.Body.sortedTags())
		}
	})

	b.Run("write", func(b *testing.B) {
		var buf bytes.Buffer
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Reset()
			msg.Body.write(&buf)
		}
		sinkInt = buf.Len()
	})
}

// BenchmarkFormatCheckSum isolates the per-message checksum formatting, which
// currently goes through fmt for a value that is always 0-255.
func BenchmarkFormatCheckSum(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		sinkInt = len(formatCheckSum(i % 256))
	}
}

// BenchmarkRepeatingGroup covers group construction, serialisation and parsing
// separately, since each has a different allocation profile.
func BenchmarkRepeatingGroup(b *testing.B) {
	c := loadCorpus(b, tape.MixRealistic)
	md := c.subset(tape.MsgTypeMarketDataIncrementalRefresh)
	tapeGroup := md.entries[0].Groups[0]
	template := groupTemplateFromTape(tapeGroup)

	b.Run("Add", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			rg := NewRepeatingGroup(Tag(tapeGroup.CountTag), template)
			sinkInt = rg.Add().Len()
		}
	})

	b.Run("Build", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkTVs = repeatingGroupFromTape(tapeGroup).Write()
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(tapeGroup.Entries)), "ns/entry")
	})

	b.Run("Write", func(b *testing.B) {
		rg := repeatingGroupFromTape(tapeGroup)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkTVs = rg.Write()
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(tapeGroup.Entries)), "ns/entry")
	})

	b.Run("Read", func(b *testing.B) {
		dd := corpusDD(b)
		msg := NewMessage()
		if err := ParseMessageWithDataDictionary(msg, bytes.NewBuffer(md.wire[0]), nil, dd); err != nil {
			b.Fatal(err)
		}

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			rg := NewRepeatingGroup(Tag(tapeGroup.CountTag), template)
			if err := msg.Body.GetGroup(rg); err != nil {
				b.Fatal(err)
			}
			sinkInt = rg.Len()
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(tapeGroup.Entries)), "ns/entry")
	})
}
