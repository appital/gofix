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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appital/gofix/datadictionary"
	"github.com/appital/gofix/internal/tape"
)

// The benchmark corpus. Keep these fixed: changing them invalidates every
// baseline recorded in docs/BENCHMARKING.md.
const (
	corpusSeed = 20240603
	corpusSize = 512
)

// sink defeats dead-code elimination in benchmarks. Assign every result you do
// not otherwise use to one of these.
var (
	sinkBytes []byte
	sinkInt   int
	sinkMsg   *Message
	sinkErr   error
	sinkTVs   []TagValue
)

// corpus is a tape rendered into every representation the benchmarks need.
type corpus struct {
	name string

	entries []tape.Entry
	// wire holds the canonical encoding of each entry, produced independently
	// of Message.build.
	wire [][]byte
	// stream is wire concatenated, i.e. what arrives off a socket.
	stream []byte
	// messages holds each entry built through the library's own core API.
	messages []*Message

	totalBytes  int
	totalFields int
}

var (
	corpusCache   = map[string]*corpus{}
	corpusCacheMu sync.Mutex
)

// loadCorpus returns a cached corpus for the named mix. Building it is slow
// enough to matter, so benchmarks share one instance.
func loadCorpus(tb testing.TB, mix tape.Mix) *corpus {
	tb.Helper()

	corpusCacheMu.Lock()
	defer corpusCacheMu.Unlock()

	if c, ok := corpusCache[mix.Name]; ok {
		return c
	}

	entries := tape.Gen(corpusSeed, corpusSize, mix)
	c := &corpus{
		name:     mix.Name,
		entries:  entries,
		wire:     tape.WireAll(entries, 1, tape.DefaultSession),
		stream:   tape.Stream(entries, 1, tape.DefaultSession),
		messages: make([]*Message, len(entries)),
	}

	for i, e := range entries {
		c.messages[i] = messageFromEntry(e, i+1, tape.DefaultSession)
		c.totalBytes += len(c.wire[i])
		c.totalFields += e.FieldCount()
	}

	corpusCache[mix.Name] = c
	return c
}

// buffers returns a fresh *bytes.Buffer per message. ParseMessage retains the
// buffer it is handed (TagValue.value aliases its backing array), so parse
// benchmarks must not share one across messages.
func (c *corpus) buffers() []*bytes.Buffer {
	bufs := make([]*bytes.Buffer, len(c.wire))
	for i, w := range c.wire {
		bufs[i] = bytes.NewBuffer(w)
	}
	return bufs
}

// bytesPerMsg is what benchmarks pass to b.SetBytes so throughput reads as MB/s.
func (c *corpus) bytesPerMsg() int64 {
	return int64(c.totalBytes / len(c.wire))
}

// subset returns a corpus-like view containing only entries of one MsgType.
// Used for per-message-class sub-benchmarks.
func (c *corpus) subset(msgType string) *corpus {
	sub := &corpus{name: c.name + "/" + msgType}

	for i, e := range c.entries {
		if e.MsgType != msgType {
			continue
		}
		sub.entries = append(sub.entries, e)
		sub.wire = append(sub.wire, c.wire[i])
		sub.messages = append(sub.messages, c.messages[i])
		sub.stream = append(sub.stream, c.wire[i]...)
		sub.totalBytes += len(c.wire[i])
		sub.totalFields += e.FieldCount()
	}

	return sub
}

// messageFromEntry builds a Message using only the core public API - no
// generated gen/field types. That keeps this file buildable on a fresh clone
// where gen/ has not been generated, and isolates the benchmarks from the
// generated setters' extra interface boxing (which _test/loadgen covers).
func messageFromEntry(e tape.Entry, seq int, sess tape.Session) *Message {
	msg := NewMessage()

	msg.Header.SetString(tagBeginString, sess.BeginString)
	msg.Header.SetString(tagMsgType, e.MsgType)
	msg.Header.SetString(tagSenderCompID, sess.SenderCompID)
	msg.Header.SetString(tagTargetCompID, sess.TargetCompID)
	msg.Header.SetInt(tagMsgSeqNum, seq)
	msg.Header.SetString(tagSendingTime,
		tape.BaseTime.Add(time.Duration(seq-1)*time.Millisecond).UTC().Format(tape.TimestampLayout))

	for _, f := range e.Header {
		msg.Header.SetString(Tag(f.Tag), f.Value)
	}
	for _, f := range e.Body {
		msg.Body.SetString(Tag(f.Tag), f.Value)
	}
	for _, g := range e.Groups {
		msg.Body.SetGroup(repeatingGroupFromTape(g))
	}

	return msg
}

// repeatingGroupFromTape mirrors a tape.Group into a *RepeatingGroup, including
// nested groups.
func repeatingGroupFromTape(g tape.Group) *RepeatingGroup {
	rg := NewRepeatingGroup(Tag(g.CountTag), groupTemplateFromTape(g))

	for i, entryFields := range g.Entries {
		group := rg.Add()
		for _, f := range entryFields {
			group.SetString(Tag(f.Tag), f.Value)
		}
		for _, nested := range g.Nested[i] {
			group.SetGroup(repeatingGroupFromTape(nested))
		}
	}

	return rg
}

func groupTemplateFromTape(g tape.Group) GroupTemplate {
	template := make(GroupTemplate, 0, len(g.Template))

	for _, t := range g.Template {
		if nested, ok := nestedWithCountTag(g, t); ok {
			template = append(template, NewRepeatingGroup(Tag(t), groupTemplateFromTape(nested)))
			continue
		}
		template = append(template, GroupElement(Tag(t)))
	}

	return template
}

// nestedWithCountTag finds a representative nested group by its count tag. All
// entries of a tape group share the same nested shape, so the first match
// describes the template for every entry.
func nestedWithCountTag(g tape.Group, countTag int) (tape.Group, bool) {
	for _, nested := range g.Nested {
		for _, n := range nested {
			if n.CountTag == countTag {
				return n, true
			}
		}
	}
	return tape.Group{}, false
}

var (
	fix44DD     *datadictionary.DataDictionary
	fix44DDOnce sync.Once
	fix44DDErr  error
)

// corpusDD returns the FIX 4.4 data dictionary, parsed once. Benchmarks that
// need a dictionary must call this during setup, before ResetTimer - parsing
// the XML takes tens of milliseconds and would swamp any measurement.
func corpusDD(tb testing.TB) *datadictionary.DataDictionary {
	tb.Helper()

	fix44DDOnce.Do(func() {
		fix44DD, fix44DDErr = datadictionary.Parse("spec/FIX44.xml")
	})
	if fix44DDErr != nil {
		tb.Fatalf("parsing spec/FIX44.xml: %v", fix44DDErr)
	}

	return fix44DD
}

// forEachMsgType runs fn as a sub-benchmark per message class, plus once over
// the whole tape. Per-class numbers say where cost lives; the tape number is
// the one to track over time.
//
// IOIs are not part of the realistic tape, so their class is drawn from the IOI
// corpus instead. That adds msgtype=IOI without moving the tape number.
func forEachMsgType(b *testing.B, c *corpus, fn func(b *testing.B, c *corpus)) {
	b.Helper()

	for _, msgType := range tape.MixRealistic.MsgTypes() {
		sub := c.subset(msgType)
		if len(sub.entries) == 0 {
			continue
		}
		b.Run("msgtype="+msgTypeName(msgType), func(b *testing.B) { fn(b, sub) })
	}

	ioi := loadCorpus(b, tape.MixIOI).subset(tape.MsgTypeIOI)
	b.Run("msgtype="+msgTypeName(tape.MsgTypeIOI), func(b *testing.B) { fn(b, ioi) })

	b.Run("tape", func(b *testing.B) { fn(b, c) })
}

// msgTypeName gives sub-benchmark names that survive benchstat's parsing and
// stay readable in profiles.
func msgTypeName(msgType string) string {
	switch msgType {
	case tape.MsgTypeHeartbeat:
		return "Heartbeat"
	case tape.MsgTypeIOI:
		return "IOI"
	case tape.MsgTypeExecutionReport:
		return "ExecutionReport"
	case tape.MsgTypeNewOrderSingle:
		return "NewOrderSingle"
	case tape.MsgTypeOrderCancelRequest:
		return "OrderCancel"
	case tape.MsgTypeOrderCancelReplaceRequest:
		return "OrderCancelReplace"
	case tape.MsgTypeMarketDataSnapshot:
		return "MarketDataSnapshot"
	case tape.MsgTypeMarketDataIncrementalRefresh:
		return "MarketDataIncremental"
	}
	return "MsgType" + strings.ToUpper(msgType)
}
