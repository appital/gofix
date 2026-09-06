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

package tape

import (
	"sort"
	"strconv"
	"time"
)

// Session identifies the endpoints a rendered message belongs to.
type Session struct {
	BeginString  string
	SenderCompID string
	TargetCompID string
}

// DefaultSession is the FIX 4.4 session the benchmarks render against.
var DefaultSession = Session{
	BeginString:  "FIX.4.4",
	SenderCompID: "SENDER",
	TargetCompID: "TARGET",
}

// Wire renders an Entry to canonical FIX bytes with a correct BodyLength (9)
// and CheckSum (10).
//
// This encoder is deliberately independent of quickfix's own Message.build(),
// so that parser benchmarks measure the parser against a fixed external
// reference rather than against whatever the builder happens to emit. It also
// serves as an oracle for the round-trip test.
func Wire(e Entry, seq int, sess Session, sendingTime time.Time) []byte {
	// Body: everything after 9=..<SOH> and before 10=..<SOH>.
	//
	// Fields are emitted in quickfix's canonical order - MsgType first, then
	// header tags ascending, then body items ascending with each repeating
	// group rendered whole at its count tag's position. Matching the engine's
	// own ordering lets the round-trip test compare Wire output against
	// Message.build() byte for byte.
	body := make([]byte, 0, 512)
	body = appendField(body, TagMsgType, e.MsgType)

	header := []Field{
		{TagMsgSeqNum, strconv.Itoa(seq)},
		{TagSenderCompID, sess.SenderCompID},
		{TagTargetCompID, sess.TargetCompID},
		{TagSendingTime, sendingTime.UTC().Format(TimestampLayout)},
	}
	header = append(header, e.Header...)
	sort.SliceStable(header, func(i, j int) bool { return header[i].Tag < header[j].Tag })

	for _, f := range header {
		body = appendField(body, f.Tag, f.Value)
	}

	for _, item := range bodyItems(e) {
		if item.group != nil {
			body = appendGroup(body, *item.group)
			continue
		}
		body = appendField(body, item.tag, item.value)
	}

	out := make([]byte, 0, len(body)+64)
	out = appendField(out, TagBeginString, sess.BeginString)
	out = appendIntField(out, TagBodyLength, len(body))
	out = append(out, body...)

	sum := 0
	for _, b := range out {
		sum += int(b)
	}
	out = appendField(out, TagCheckSum, formatCheckSum(sum%256))

	return out
}

// wireItem is either a plain field or a whole repeating group, tagged with the
// tag it sorts by.
type wireItem struct {
	tag   int
	value string
	group *Group
}

// bodyItems returns an Entry's body fields and groups interleaved in ascending
// tag order, which is quickfix's normalFieldOrder.
func bodyItems(e Entry) []wireItem {
	items := make([]wireItem, 0, len(e.Body)+len(e.Groups))
	for _, f := range e.Body {
		items = append(items, wireItem{tag: f.Tag, value: f.Value})
	}
	for i := range e.Groups {
		items = append(items, wireItem{tag: e.Groups[i].CountTag, group: &e.Groups[i]})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].tag < items[j].tag })

	return items
}

// WireAll renders every entry in a tape, assigning sequence numbers from
// firstSeq and timestamps stepping deterministically from BaseTime.
func WireAll(entries []Entry, firstSeq int, sess Session) [][]byte {
	out := make([][]byte, len(entries))
	for i, e := range entries {
		out[i] = Wire(e, firstSeq+i, sess, BaseTime.Add(time.Duration(i)*time.Millisecond))
	}
	return out
}

// Stream concatenates rendered entries into a single byte stream, which is what
// a socket actually delivers. Use it to benchmark parser framing.
func Stream(entries []Entry, firstSeq int, sess Session) []byte {
	msgs := WireAll(entries, firstSeq, sess)

	total := 0
	for _, m := range msgs {
		total += len(m)
	}

	out := make([]byte, 0, total)
	for _, m := range msgs {
		out = append(out, m...)
	}
	return out
}

func appendGroup(dst []byte, g Group) []byte {
	dst = appendIntField(dst, g.CountTag, len(g.Entries))

	for i, entry := range g.Entries {
		// Emit in template order so the delimiter tag leads each entry and
		// nested groups land at their count tag's template position - which is
		// where quickfix's own group writer puts them.
		emitted := 0
		for _, tag := range g.Template {
			if nested, ok := nestedEntryGroup(g, i, tag); ok {
				dst = appendGroup(dst, nested)
				emitted++
				continue
			}
			for _, f := range entry {
				if f.Tag == tag {
					dst = appendField(dst, f.Tag, f.Value)
				}
			}
		}

		// Any nested group whose count tag is absent from the template still
		// has to be written; put it last rather than dropping it.
		if emitted < len(g.Nested[i]) {
			for _, nested := range g.Nested[i] {
				if !templateHasTag(g.Template, nested.CountTag) {
					dst = appendGroup(dst, nested)
				}
			}
		}
	}

	return dst
}

func nestedEntryGroup(g Group, entry, countTag int) (Group, bool) {
	for _, n := range g.Nested[entry] {
		if n.CountTag == countTag {
			return n, true
		}
	}
	return Group{}, false
}

func templateHasTag(template []int, tag int) bool {
	for _, t := range template {
		if t == tag {
			return true
		}
	}
	return false
}

func appendField(dst []byte, tag int, value string) []byte {
	dst = strconv.AppendInt(dst, int64(tag), 10)
	dst = append(dst, '=')
	dst = append(dst, value...)
	return append(dst, SOH)
}

func appendIntField(dst []byte, tag, value int) []byte {
	dst = strconv.AppendInt(dst, int64(tag), 10)
	dst = append(dst, '=')
	dst = strconv.AppendInt(dst, int64(value), 10)
	return append(dst, SOH)
}

func formatCheckSum(v int) string {
	return string([]byte{byte('0' + v/100), byte('0' + (v/10)%10), byte('0' + v%10)})
}

// Fields flattens an Entry's non-group fields, header first. Useful for
// consumers that set fields individually rather than rendering to bytes.
func (e Entry) Fields() []Field {
	out := make([]Field, 0, len(e.Header)+len(e.Body))
	out = append(out, e.Header...)
	return append(out, e.Body...)
}

// FieldCount reports how many tag/value pairs an Entry expands to on the wire,
// excluding the session-level fields Wire adds. Used to normalise per-field
// benchmark results.
func (e Entry) FieldCount() int {
	n := len(e.Header) + len(e.Body)
	for _, g := range e.Groups {
		n += groupFieldCount(g)
	}
	return n
}

func groupFieldCount(g Group) int {
	n := 1 // the count tag itself
	for i, entry := range g.Entries {
		n += len(entry)
		for _, nested := range g.Nested[i] {
			n += groupFieldCount(nested)
		}
	}
	return n
}
