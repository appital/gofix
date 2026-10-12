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
	"math/rand"
	"strconv"
	"time"
)

// BaseTime is the fixed instant all generated timestamps derive from. Using a
// constant here (rather than time.Now) is what makes a tape reproducible across
// runs and across machines.
var BaseTime = time.Date(2024, time.June, 3, 13, 30, 0, 0, time.UTC)

// TimestampLayout is the FIX UTCTimestamp format with millisecond precision.
const TimestampLayout = "20060102-15:04:05.000"

var (
	symbols    = []string{"AAPL", "MSFT", "TSLA", "VOD.L", "BARC.L", "SAP.DE", "7203.T", "BP.L", "GOOG", "AMZN"}
	currencies = []string{"USD", "GBP", "EUR", "JPY"}
	exchanges  = []string{"XNAS", "XLON", "XETR", "XTKS"}
	brokers    = []string{"BRKR01", "BRKR02", "BRKR03", "BRKR04"}
	desks      = []string{"DESK-EU", "DESK-US", "DESK-AP"}
)

// Gen builds a deterministic tape of n entries drawn from mix. The same
// (seed, n, mix) always produces an identical result.
func Gen(seed int64, n int, mix Mix) []Entry {
	rng := rand.New(rand.NewSource(seed))

	types := mix.MsgTypes()
	cumulative := make([]int, len(types))
	total := 0
	for i, t := range types {
		total += mix.Weights[t]
		cumulative[i] = total
	}
	if total == 0 {
		return nil
	}

	entries := make([]Entry, n)
	for i := range entries {
		roll := rng.Intn(total)
		msgType := types[len(types)-1]
		for j, c := range cumulative {
			if roll < c {
				msgType = types[j]
				break
			}
		}
		entries[i] = genEntry(rng, msgType, i)
	}

	return entries
}

func genEntry(rng *rand.Rand, msgType string, i int) Entry {
	switch msgType {
	case MsgTypeExecutionReport:
		return genExecutionReport(rng, i)
	case MsgTypeNewOrderSingle:
		return genNewOrderSingle(rng, i)
	case MsgTypeOrderCancelReplaceRequest:
		return genOrderCancelReplace(rng, i)
	case MsgTypeOrderCancelRequest:
		return genOrderCancel(rng, i)
	case MsgTypeMarketDataIncrementalRefresh:
		return genMarketDataIncremental(rng, i)
	case MsgTypeMarketDataSnapshot:
		return genMarketDataSnapshot(rng, i)
	case MsgTypeHeartbeat:
		return Entry{MsgType: MsgTypeHeartbeat}
	}
	panic("tape: no generator for MsgType " + msgType)
}

// ts returns a deterministic timestamp offset from BaseTime.
func ts(i int) string {
	return BaseTime.Add(time.Duration(i) * 137 * time.Millisecond).Format(TimestampLayout)
}

func dateOnly(i int) string {
	return BaseTime.AddDate(0, 0, i%5).Format("20060102")
}

func price(rng *rand.Rand) string {
	return strconv.FormatFloat(10+rng.Float64()*490, 'f', 4, 64)
}

func qty(rng *rand.Rand) string {
	return strconv.Itoa((rng.Intn(200) + 1) * 100)
}

func pick(rng *rand.Rand, from []string) string {
	return from[rng.Intn(len(from))]
}

// genExecutionReport is the dominant message class: ~30 flat fields, no groups.
func genExecutionReport(rng *rand.Rand, i int) Entry {
	symbol := pick(rng, symbols)
	orderQty := qty(rng)
	lastQty := qty(rng)

	return Entry{
		MsgType: MsgTypeExecutionReport,
		Body: []Field{
			{TagAccount, "ACCT" + strconv.Itoa(rng.Intn(500))},
			{TagAvgPx, price(rng)},
			{TagClOrdID, "CLORD-" + strconv.Itoa(i)},
			{TagCumQty, lastQty},
			{TagCurrency, pick(rng, currencies)},
			{TagExecID, "EXEC-" + strconv.Itoa(i)},
			{TagExecInst, "G"},
			{TagLastMkt, pick(rng, exchanges)},
			{TagLastCapacity, strconv.Itoa(rng.Intn(4) + 1)},
			{TagLastPx, price(rng)},
			{TagLastQty, lastQty},
			{TagOrderID, "ORD-" + strconv.Itoa(i)},
			{TagOrderQty, orderQty},
			{TagOrdStatus, pick(rng, []string{"0", "1", "2"})},
			{TagOrdType, pick(rng, []string{"1", "2"})},
			{TagPrice, price(rng)},
			{TagSecurityID, "SEC" + symbol},
			{TagSecurityIDSource, "4"},
			{TagSecurityExchange, pick(rng, exchanges)},
			{TagSide, pick(rng, []string{"1", "2"})},
			{TagSymbol, symbol},
			{TagText, "fill"},
			{TagTimeInForce, pick(rng, []string{"0", "1", "3"})},
			{TagTransactTime, ts(i)},
			{TagSettlType, "0"},
			{TagSettlDate, dateOnly(i)},
			{TagPositionEffect, "O"},
			{TagExecType, pick(rng, []string{"0", "F"})},
			{TagLeavesQty, qty(rng)},
			{TagTradeDate, dateOnly(i)},
			{TagOrderCapacity, "A"},
		},
	}
}

// genNewOrderSingle carries a small NoPartyIDs group with nested NoPartySubIDs.
func genNewOrderSingle(rng *rand.Rand, i int) Entry {
	symbol := pick(rng, symbols)

	return Entry{
		MsgType: MsgTypeNewOrderSingle,
		Body: []Field{
			{TagAccount, "ACCT" + strconv.Itoa(rng.Intn(500))},
			{TagClOrdID, "CLORD-" + strconv.Itoa(i)},
			{TagCurrency, pick(rng, currencies)},
			{TagHandlInst, "1"},
			{TagOrderQty, qty(rng)},
			{TagOrdType, "2"},
			{TagPrice, price(rng)},
			{TagSecurityID, "SEC" + symbol},
			{TagSecurityIDSource, "4"},
			{TagSecurityExchange, pick(rng, exchanges)},
			{TagSide, pick(rng, []string{"1", "2"})},
			{TagSymbol, symbol},
			{TagTimeInForce, "0"},
			{TagTransactTime, ts(i)},
			{TagSettlType, "0"},
			{TagSettlDate, dateOnly(i)},
			{TagOrderCapacity, "A"},
			{TagText, "new"},
		},
		Groups: []Group{genParties(rng, 2+rng.Intn(2))},
	}
}

func genOrderCancelReplace(rng *rand.Rand, i int) Entry {
	symbol := pick(rng, symbols)

	return Entry{
		MsgType: MsgTypeOrderCancelReplaceRequest,
		Body: []Field{
			{TagAccount, "ACCT" + strconv.Itoa(rng.Intn(500))},
			{TagClOrdID, "CLORD-" + strconv.Itoa(i)},
			{TagOrigClOrdID, "CLORD-" + strconv.Itoa(i-1)},
			{TagOrderID, "ORD-" + strconv.Itoa(i-1)},
			{TagCurrency, pick(rng, currencies)},
			{TagHandlInst, "1"},
			{TagOrderQty, qty(rng)},
			{TagOrdType, "2"},
			{TagPrice, price(rng)},
			{TagSecurityID, "SEC" + symbol},
			{TagSecurityIDSource, "4"},
			{TagSide, pick(rng, []string{"1", "2"})},
			{TagSymbol, symbol},
			{TagTimeInForce, "0"},
			{TagTransactTime, ts(i)},
			{TagText, "replace"},
		},
		Groups: []Group{genParties(rng, 2)},
	}
}

func genOrderCancel(rng *rand.Rand, i int) Entry {
	symbol := pick(rng, symbols)

	return Entry{
		MsgType: MsgTypeOrderCancelRequest,
		Body: []Field{
			{TagAccount, "ACCT" + strconv.Itoa(rng.Intn(500))},
			{TagClOrdID, "CLORD-" + strconv.Itoa(i)},
			{TagOrigClOrdID, "CLORD-" + strconv.Itoa(i-1)},
			{TagOrderID, "ORD-" + strconv.Itoa(i-1)},
			{TagOrderQty, qty(rng)},
			{TagSecurityID, "SEC" + symbol},
			{TagSecurityIDSource, "4"},
			{TagSide, pick(rng, []string{"1", "2"})},
			{TagSymbol, symbol},
			{TagTransactTime, ts(i)},
			{TagText, "cancel"},
		},
	}
}

// genMarketDataIncremental is the repeating-group stressor: 10-20 MD entries,
// some of which carry a nested NoSecurityAltID group. Every tag here is a legal
// member of the FIX 4.4 NoMDEntries group (directly or via the Instrument
// component) - membership matters, because a tag the data dictionary does not
// recognise terminates the group and silently flattens the rest of the message
// into the body.
func genMarketDataIncremental(rng *rand.Rand, i int) Entry {
	count := 10 + rng.Intn(11)

	group := Group{
		CountTag: TagNoMDEntries,
		Template: []int{
			TagMDUpdateAction, TagMDEntryType, TagMDEntryID, TagSymbol,
			TagSecurityID, TagSecurityIDSource, TagNoSecurityAltID,
			TagSecurityExchange, TagMDEntryPx, TagCurrency, TagMDEntrySize,
			TagMDEntryDate, TagMDEntryTime, TagTickDirection,
			TagMDEntryOriginator, TagLocationID, TagDeskID,
			TagNumberOfOrders, TagMDEntryPositionNo,
		},
		Entries: make([][]Field, count),
		Nested:  map[int][]Group{},
	}

	for e := range count {
		symbol := pick(rng, symbols)
		group.Entries[e] = []Field{
			{TagMDUpdateAction, pick(rng, []string{"0", "1", "2"})},
			{TagMDEntryType, pick(rng, []string{"0", "1", "2"})},
			{TagMDEntryID, "MDE-" + strconv.Itoa(i) + "-" + strconv.Itoa(e)},
			{TagSymbol, symbol},
			{TagSecurityID, "SEC" + symbol},
			{TagSecurityIDSource, "4"},
			{TagSecurityExchange, pick(rng, exchanges)},
			{TagMDEntryPx, price(rng)},
			{TagCurrency, pick(rng, currencies)},
			{TagMDEntrySize, qty(rng)},
			{TagMDEntryDate, dateOnly(i)},
			{TagMDEntryTime, "13:30:0" + strconv.Itoa(e%10)},
			{TagTickDirection, strconv.Itoa(rng.Intn(4))},
			{TagMDEntryOriginator, pick(rng, brokers)},
			{TagLocationID, pick(rng, exchanges)},
			{TagDeskID, pick(rng, desks)},
			{TagNumberOfOrders, strconv.Itoa(rng.Intn(50) + 1)},
			{TagMDEntryPositionNo, strconv.Itoa(e + 1)},
		}

		// Every third entry nests an alternate-identifier group, so the corpus
		// exercises nested-group parse and build inside a wide outer group.
		if e%3 == 0 {
			group.Nested[e] = []Group{genSecurityAltIDs(rng, 2, symbol)}
		}
	}

	return Entry{
		MsgType: MsgTypeMarketDataIncrementalRefresh,
		Body:    []Field{{TagMDReqID, "MDREQ-" + strconv.Itoa(i)}},
		Groups:  []Group{group},
	}
}

func genMarketDataSnapshot(rng *rand.Rand, i int) Entry {
	count := 8 + rng.Intn(9)
	symbol := pick(rng, symbols)

	group := Group{
		CountTag: TagNoMDEntries,
		Template: []int{
			TagMDEntryType, TagMDEntryPx, TagCurrency, TagMDEntrySize,
			TagMDEntryDate, TagMDEntryTime, TagTickDirection,
			TagMDEntryOriginator, TagLocationID, TagDeskID,
			TagNumberOfOrders, TagMDEntryPositionNo,
		},
		Entries: make([][]Field, count),
	}

	for e := range count {
		group.Entries[e] = []Field{
			{TagMDEntryType, pick(rng, []string{"0", "1"})},
			{TagMDEntryPx, price(rng)},
			{TagCurrency, pick(rng, currencies)},
			{TagMDEntrySize, qty(rng)},
			{TagMDEntryDate, dateOnly(i)},
			{TagMDEntryTime, "13:30:0" + strconv.Itoa(e%10)},
			{TagTickDirection, strconv.Itoa(rng.Intn(4))},
			{TagMDEntryOriginator, pick(rng, brokers)},
			{TagLocationID, pick(rng, exchanges)},
			{TagDeskID, pick(rng, desks)},
			{TagNumberOfOrders, strconv.Itoa(rng.Intn(50) + 1)},
			{TagMDEntryPositionNo, strconv.Itoa(e + 1)},
		}
	}

	return Entry{
		MsgType: MsgTypeMarketDataSnapshot,
		Body: []Field{
			{TagMDReqID, "MDREQ-" + strconv.Itoa(i)},
			{TagSymbol, symbol},
			{TagSecurityID, "SEC" + symbol},
			{TagSecurityIDSource, "4"},
			{TagSecurityExchange, pick(rng, exchanges)},
		},
		Groups: []Group{group},
	}
}

// genParties builds a NoPartyIDs group, each entry carrying a nested
// NoPartySubIDs group. This is the common nested-group shape in order flow.
func genParties(rng *rand.Rand, count int) Group {
	g := Group{
		CountTag: TagNoPartyIDs,
		Template: []int{TagPartyID, TagPartyIDSource, TagPartyRole, TagNoPartySubIDs},
		Entries:  make([][]Field, count),
		Nested:   map[int][]Group{},
	}

	for e := range count {
		g.Entries[e] = []Field{
			{TagPartyID, pick(rng, brokers)},
			{TagPartyIDSource, "D"},
			{TagPartyRole, strconv.Itoa(rng.Intn(10) + 1)},
		}
		g.Nested[e] = []Group{{
			CountTag: TagNoPartySubIDs,
			Template: []int{TagPartySubID, TagPartySubIDType},
			Entries: [][]Field{{
				{TagPartySubID, pick(rng, desks)},
				{TagPartySubIDType, "1"},
			}},
		}}
	}

	return g
}

// genSecurityAltIDs builds a NoSecurityAltID group - the Instrument component's
// nested group, legal anywhere Instrument appears.
func genSecurityAltIDs(rng *rand.Rand, count int, symbol string) Group {
	g := Group{
		CountTag: TagNoSecurityAltID,
		Template: []int{TagSecurityAltID, TagSecurityAltIDSource},
		Entries:  make([][]Field, count),
	}

	for e := range count {
		g.Entries[e] = []Field{
			{TagSecurityAltID, "ALT" + strconv.Itoa(e) + symbol},
			{TagSecurityAltIDSource, pick(rng, []string{"1", "4", "8"})},
		}
	}

	return g
}
