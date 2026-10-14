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

// Message types used by the tape. Values match FIX MsgType (tag 35).
const (
	MsgTypeHeartbeat                    = "0"
	MsgTypeIOI                          = "6"
	MsgTypeExecutionReport              = "8"
	MsgTypeNewOrderSingle               = "D"
	MsgTypeOrderCancelRequest           = "F"
	MsgTypeOrderCancelReplaceRequest    = "G"
	MsgTypeMarketDataSnapshot           = "W"
	MsgTypeMarketDataIncrementalRefresh = "X"
)

// Session-level tags the engine owns. Tape entries never carry these in
// Entry.Header; Wire supplies them when rendering a standalone message.
const (
	TagBeginString  = 8
	TagBodyLength   = 9
	TagMsgType      = 35
	TagMsgSeqNum    = 34
	TagSenderCompID = 49
	TagTargetCompID = 56
	TagSendingTime  = 52
	TagCheckSum     = 10
)

// Application tags referenced by the tape message shapes.
const (
	TagAccount             = 1
	TagAvgPx               = 6
	TagClOrdID             = 11
	TagCumQty              = 14
	TagCurrency            = 15
	TagExecID              = 17
	TagExecInst            = 18
	TagHandlInst           = 21
	TagSecurityIDSource    = 22
	TagLastCapacity        = 29
	TagLastPx              = 31
	TagLastQty             = 32
	TagOrderID             = 37
	TagOrderQty            = 38
	TagOrdStatus           = 39
	TagOrdType             = 40
	TagOrigClOrdID         = 41
	TagPrice               = 44
	TagSecurityID          = 48
	TagSide                = 54
	TagSymbol              = 55
	TagText                = 58
	TagTimeInForce         = 59
	TagTransactTime        = 60
	TagSettlType           = 63
	TagSettlDate           = 64
	TagPositionEffect      = 77
	TagNoMDEntries         = 268
	TagMDEntryType         = 269
	TagMDEntryPx           = 270
	TagMDEntrySize         = 271
	TagMDEntryDate         = 272
	TagMDEntryTime         = 273
	TagMDUpdateAction      = 279
	TagMDEntryID           = 278
	TagMDReqID             = 262
	TagQuoteEntryID        = 299
	TagSecurityExchange    = 207
	TagLastMkt             = 30
	TagExecType            = 150
	TagLeavesQty           = 151
	TagCashOrderQty        = 152
	TagSecurityType        = 167
	TagMDEntryPositionNo   = 290
	TagNoPartyIDs          = 453
	TagPartyID             = 448
	TagPartyIDSource       = 447
	TagPartyRole           = 452
	TagNoPartySubIDs       = 802
	TagPartySubID          = 523
	TagPartySubIDType      = 803
	TagOrderCapacity       = 528
	TagOrderRestrictions   = 529
	TagLastLiquidityInd    = 851
	TagTradeDate           = 75
	TagMaturityMonthYear   = 200
	TagCFICode             = 461
	TagQuoteCondition      = 276
	TagTradeCondition      = 277
	TagOpenCloseSettlFlag  = 286
	TagTickDirection       = 274
	TagNumberOfOrders      = 346
	TagAggressorIndicator  = 1057
	TagMDEntryOriginator   = 282
	TagLocationID          = 283
	TagDeskID              = 284
	TagQuoteEntryRejectRsn = 368
	TagNoSecurityAltID     = 454
	TagSecurityAltID       = 455
	TagSecurityAltIDSource = 456
	TagTradingSessionID    = 336
	TagIOIID               = 23
	TagIOIQltyInd          = 25
	TagIOIRefID            = 26
	TagIOIQty              = 27
	TagIOITransType        = 28
	TagValidUntilTime      = 62
	TagIOIQualifier        = 104
	TagNoIOIQualifiers     = 199
	TagNoRoutingIDs        = 215
	TagRoutingType         = 216
	TagRoutingID           = 217
	TagPriceType           = 423
)
