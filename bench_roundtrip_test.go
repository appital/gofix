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
	"strconv"
	"testing"

	"github.com/appital/gofix/datadictionary"

	"github.com/appital/gofix/internal/tape"
)

// BenchmarkRoundTrip is the closest single number to "engine cost per message"
// without any I/O: take bytes off the wire, parse them, touch a couple of
// fields the way an application would, and serialise a reply.
//
// Track this one over time. The per-layer benchmarks explain it; this is the
// number that says whether the engine as a whole got faster.
func BenchmarkRoundTrip(b *testing.B) {
	c := loadCorpus(b, tape.MixRealistic)

	for _, withDD := range []bool{false, true} {
		// Fetch the dictionary only when it is needed: parsing spec/FIX44.xml
		// allocates heavily, and at -memprofilerate=1 it would otherwise show
		// up in the dd=off profile as encoding/xml noise.
		name, appDD := "dd=off", (*datadictionary.DataDictionary)(nil)
		if withDD {
			name, appDD = "dd=on", corpusDD(b)
		}

		b.Run(name, func(b *testing.B) {
			bufs := c.buffers()

			b.SetBytes(c.bytesPerMsg())
			b.ReportAllocs()

			for i := 0; b.Loop(); i++ {
				idx := i % len(bufs)

				msg := NewMessage()
				if err := ParseMessageWithDataDictionary(msg, bufs[idx], nil, appDD); err != nil {
					b.Fatal(err)
				}

				// An application reads a few fields, then echoes the message
				// back with its own identifiers.
				sinkBytes, _ = msg.Header.GetBytes(tagMsgType)
				msg.Header.SetInt(tagMsgSeqNum, i+1)
				msg.Body.SetString(Tag(tape.TagText), "ack")

				sinkBytes = msg.build()
			}

			reportPerField(b, c)
		})
	}
}

// BenchmarkValidate sizes the data-dictionary validation pass. RejectInvalidMessage
// is what drives validateFields, which allocates a field-value type per field.
func BenchmarkValidate(b *testing.B) {
	c := loadCorpus(b, tape.MixRealistic)
	dd := corpusDD(b)

	// Parse once - Validate operates on an already-parsed Message.
	parsed := make([]*Message, len(c.wire))
	for i, buf := range c.buffers() {
		parsed[i] = NewMessage()
		if err := ParseMessageWithDataDictionary(parsed[i], buf, nil, dd); err != nil {
			b.Fatal(err)
		}
	}

	for _, reject := range []bool{false, true} {
		settings := defaultValidatorSettings
		settings.RejectInvalidMessage = reject

		validator := NewValidator(settings, dd, nil)

		b.Run("RejectInvalidMessage="+strconv.FormatBool(reject), func(b *testing.B) {
			b.SetBytes(c.bytesPerMsg())
			b.ReportAllocs()

			for i := 0; b.Loop(); i++ {
				if err := validator.Validate(parsed[i%len(parsed)]); err != nil {
					b.Fatal(err)
				}
			}

			reportPerField(b, c)
		})
	}
}

// BenchmarkCopyInto measures Message.CopyInto, the only deep-copy path in the
// engine. Applications that need to retain an inbound message past the callback
// pay this, so it is worth tracking alongside the parse path.
func BenchmarkCopyInto(b *testing.B) {
	c := loadCorpus(b, tape.MixRealistic)

	forEachMsgType(b, c, func(b *testing.B, c *corpus) {
		dst := NewMessage()

		b.SetBytes(c.bytesPerMsg())
		b.ReportAllocs()

		for i := 0; b.Loop(); i++ {
			c.messages[i%len(c.messages)].CopyInto(dst)
		}
		sinkMsg = dst
	})
}
