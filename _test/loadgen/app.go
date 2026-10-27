package main

import (
	"strconv"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"

	"github.com/appital/gofix"
	"github.com/appital/gofix/gen/enum"
	"github.com/appital/gofix/gen/field"
	"github.com/appital/gofix/gen/fix44/executionreport"
	"github.com/appital/gofix/gen/fix44/ioi"
	"github.com/appital/gofix/gen/fix44/newordersingle"
	"github.com/appital/gofix/gen/tag"
	"github.com/appital/gofix/internal/tape"
)

// serverApp is the acceptor side: it receives orders and answers each one with
// an ExecutionReport built through the generated FIX 4.4 types. This is the
// path an application's own send code takes, interface boxing and all.
type serverApp struct {
	received atomic.Uint64
	sent     atomic.Uint64
	errors   atomic.Uint64

	router *gofix.MessageRouter
}

func newServerApp() *serverApp {
	a := &serverApp{router: gofix.NewMessageRouter()}
	a.router.AddRoute(gofix.BeginStringFIX44, "D", a.onNewOrderSingle)
	return a
}

var _ gofix.Application = (*serverApp)(nil)

func (a *serverApp) FromApp(msg *gofix.Message, sessionID gofix.SessionID) gofix.MessageRejectError {
	a.received.Add(1)
	return a.router.Route(msg, sessionID)
}

var (
	execQty = decimal.NewFromInt(100)
	execPx  = decimal.NewFromFloat(101.25)
)

func (a *serverApp) onNewOrderSingle(msg *gofix.Message, sessionID gofix.SessionID) gofix.MessageRejectError {
	// Echo the ClOrdID back so the client can match the reply to its send time.
	clOrdID, err := msg.Body.GetString(tag.ClOrdID)
	if err != nil {
		a.errors.Add(1)
		return err
	}

	var side field.SideField
	if err := msg.Body.Get(&side); err != nil {
		a.errors.Add(1)
		return err
	}

	report := executionreport.New(
		field.NewOrderID(clOrdID),
		field.NewExecID(clOrdID),
		field.NewExecType(enum.ExecType_TRADE),
		field.NewOrdStatus(enum.OrdStatus_FILLED),
		side,
		field.NewLeavesQty(decimal.Zero, 2),
		field.NewCumQty(execQty, 2),
		field.NewAvgPx(execPx, 4),
	)
	report.SetClOrdID(clOrdID)
	report.SetLastQty(execQty, 2)
	report.SetLastPx(execPx, 4)
	report.SetTransactTime(time.Now())

	if symbol, err := msg.Body.GetString(tag.Symbol); err == nil {
		report.SetSymbol(symbol)
	}

	if sendErr := gofix.SendToTarget(report, sessionID); sendErr != nil {
		a.errors.Add(1)
		return nil
	}
	a.sent.Add(1)

	return nil
}

// clientApp is the initiator side. In the orders scenario it paces orders out
// and times the round trip of each reply; in the IOI scenario it is the
// consumer of the acceptor's IOI stream and times each one's delivery.
type clientApp struct {
	hist *histogram

	// sendNanos is a ring indexed by ClOrdID or IOIID, so matching a message to its send
	// time is an array index rather than a map lookup - no allocation on the
	// measurement path.
	sendNanos []int64
	ringMask  uint64

	sent      atomic.Uint64
	received  atomic.Uint64
	unmatched atomic.Uint64

	// measuring gates stats collection so warmup traffic is excluded.
	measuring atomic.Bool

	loggedOn chan gofix.SessionID
}

func newClientApp(hist *histogram, ringSize int) *clientApp {
	size := 1
	for size < ringSize {
		size <<= 1
	}

	return &clientApp{
		hist:      hist,
		sendNanos: make([]int64, size),
		ringMask:  uint64(size - 1),
		loggedOn:  make(chan gofix.SessionID, 64),
	}
}

var (
	_ gofix.Application    = (*clientApp)(nil)
	_ gofix.SessionHandler = (*clientApp)(nil)
)

func (a *clientApp) OnCreate(gofix.SessionID) {}

func (a *clientApp) OnLogon(sessionID gofix.SessionID) {
	select {
	case a.loggedOn <- sessionID:
	default:
	}
}

func (a *clientApp) OnLogout(gofix.SessionID) {}

func (a *clientApp) FromApp(msg *gofix.Message, _ gofix.SessionID) gofix.MessageRejectError {
	if !a.measuring.Load() {
		return nil
	}
	a.received.Add(1)

	var id []byte
	var err gofix.MessageRejectError
	if msg.IsMsgTypeOf(tape.MsgTypeIOI) {
		id, err = a.consumeIOI(msg)
	} else {
		id, err = msg.Body.GetBytes(tag.ClOrdID)
	}
	if err != nil {
		a.unmatched.Add(1)
		return nil
	}

	seq, convErr := strconv.ParseUint(string(id), 10, 64)
	if convErr != nil {
		a.unmatched.Add(1)
		return nil
	}

	sent := atomic.LoadInt64(&a.sendNanos[seq&a.ringMask])
	if sent == 0 {
		a.unmatched.Add(1)
		return nil
	}

	a.hist.record(time.Now().UnixNano() - sent)

	return nil
}

// orderFromEntry builds a NewOrderSingle using the generated typed setters, so
// the loadgen exercises the same construction path an application would.
func orderFromEntry(e tape.Entry, seq uint64) *gofix.Message {
	order := newordersingle.New(
		field.NewClOrdID(strconv.FormatUint(seq, 10)),
		field.NewSide(enum.Side_BUY),
		field.NewTransactTime(time.Now()),
		field.NewOrdType(enum.OrdType_LIMIT),
	)

	for _, f := range e.Body {
		switch f.Tag {
		case tape.TagClOrdID, tape.TagSide, tape.TagTransactTime, tape.TagOrdType:
			// Already set by the constructor; ClOrdID in particular must stay
			// the sequence number so replies can be matched.
			continue
		}
		order.Body.SetString(gofix.Tag(f.Tag), f.Value)
	}

	return order.ToMessage()
}

// consumeIOI reads an IOI through the generated getters the way a consumer
// deciding whether to act on it would, and returns its IOIID.
func (a *clientApp) consumeIOI(msg *gofix.Message) ([]byte, gofix.MessageRejectError) {
	interest := ioi.FromMessage(msg)

	if _, err := interest.GetIOITransType(); err != nil {
		return nil, err
	}
	if _, err := interest.GetSymbol(); err != nil {
		return nil, err
	}
	if _, err := interest.GetSide(); err != nil {
		return nil, err
	}
	if _, err := interest.GetIOIQty(); err != nil {
		return nil, err
	}

	return msg.Body.GetBytes(tag.IOIID)
}

// ioiFromEntry builds an IOI using the generated typed setters and groups. The
// IOIID is the sequence number, so the receiver can match it to its send time.
func ioiFromEntry(e tape.Entry, seq uint64) *gofix.Message {
	var transType, side, qty string
	for _, f := range e.Body {
		switch f.Tag {
		case tape.TagIOITransType:
			transType = f.Value
		case tape.TagSide:
			side = f.Value
		case tape.TagIOIQty:
			qty = f.Value
		}
	}

	interest := ioi.New(
		field.NewIOIID(strconv.FormatUint(seq, 10)),
		field.NewIOITransType(enum.IOITransType(transType)),
		field.NewSide(enum.Side(side)),
		field.NewIOIQty(enum.IOIQty(qty)),
	)
	interest.SetTransactTime(time.Now())

	for _, f := range e.Body {
		switch f.Tag {
		case tape.TagIOIID, tape.TagIOITransType, tape.TagSide, tape.TagIOIQty, tape.TagTransactTime:
			continue
		}
		interest.Body.SetString(gofix.Tag(f.Tag), f.Value)
	}

	for _, g := range e.Groups {
		switch g.CountTag {
		case tape.TagNoIOIQualifiers:
			qualifiers := ioi.NewNoIOIQualifiersRepeatingGroup()
			for _, entry := range g.Entries {
				qualifiers.Add().SetIOIQualifier(enum.IOIQualifier(entry[0].Value))
			}
			interest.SetNoIOIQualifiers(qualifiers)
		case tape.TagNoRoutingIDs:
			routing := ioi.NewNoRoutingIDsRepeatingGroup()
			for _, entry := range g.Entries {
				r := routing.Add()
				r.SetRoutingType(enum.RoutingType(entry[0].Value))
				r.SetRoutingID(entry[1].Value)
			}
			interest.SetNoRoutingIDs(routing)
		}
	}

	return interest.ToMessage()
}
