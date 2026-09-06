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

// Package tape generates a deterministic corpus ("tape") of representative FIX
// messages for benchmarking and load testing.
//
// The package is deliberately dependency-free: it imports nothing outside the
// standard library, and in particular it does not import quickfix itself. The
// quickfix benchmarks are internal tests (package quickfix), so importing
// quickfix here would create an import cycle. It also avoids the generated
// gen/... packages, which are gitignored build artifacts.
//
// Consumers receive plain tag/value data plus a canonical wire encoding, and
// convert that into whatever representation they need.
package tape

import (
	"sort"
)

// SOH is the FIX field separator.
const SOH = '\x01'

// Field is a single FIX tag/value pair.
type Field struct {
	Tag   int
	Value string
}

// Group is a FIX repeating group: a count tag, the template tag order, and one
// slice of fields per group entry. Nested holds any groups nested inside entry
// i of this group, keyed by entry index.
type Group struct {
	CountTag int
	Template []int
	Entries  [][]Field
	Nested   map[int][]Group
}

// Entry is one message in the tape. Header omits the fields the engine fills in
// itself (8, 9, 35, 34, 49, 56, 52) so that Entry can be replayed through a live
// session as well as rendered standalone.
type Entry struct {
	MsgType string
	Header  []Field
	Body    []Field
	Groups  []Group
}

// Mix describes the relative frequency of each message type in a tape.
type Mix struct {
	Name    string
	Weights map[string]int
}

// MixRealistic models a typical buy-side order flow: mostly execution reports,
// a steady trickle of order actions, some market data with large repeating
// groups, and occasional session-level admin traffic.
var MixRealistic = Mix{
	Name: "realistic",
	Weights: map[string]int{
		MsgTypeExecutionReport:              70,
		MsgTypeNewOrderSingle:               15,
		MsgTypeOrderCancelReplaceRequest:    5,
		MsgTypeOrderCancelRequest:           3,
		MsgTypeMarketDataIncrementalRefresh: 5,
		MsgTypeHeartbeat:                    2,
	},
}

// MixOrderFlow is flat order traffic only - no repeating groups beyond parties.
var MixOrderFlow = Mix{
	Name: "orderflow",
	Weights: map[string]int{
		MsgTypeExecutionReport:           75,
		MsgTypeNewOrderSingle:            17,
		MsgTypeOrderCancelReplaceRequest: 5,
		MsgTypeOrderCancelRequest:        3,
	},
}

// MixMarketData is repeating-group heavy, for stressing group parse and build.
var MixMarketData = Mix{
	Name: "marketdata",
	Weights: map[string]int{
		MsgTypeMarketDataIncrementalRefresh: 80,
		MsgTypeMarketDataSnapshot:           20,
	},
}

// MixFlat is the floor case: minimal messages, measuring per-message overhead.
var MixFlat = Mix{
	Name:    "flat",
	Weights: map[string]int{MsgTypeHeartbeat: 100},
}

// Mixes indexes the predefined mixes by name, for CLI selection.
var Mixes = map[string]Mix{
	MixRealistic.Name:  MixRealistic,
	MixOrderFlow.Name:  MixOrderFlow,
	MixMarketData.Name: MixMarketData,
	MixFlat.Name:       MixFlat,
}

// MsgTypes returns the message types in a mix, in a stable order.
func (m Mix) MsgTypes() []string {
	types := make([]string, 0, len(m.Weights))
	for t := range m.Weights {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}
