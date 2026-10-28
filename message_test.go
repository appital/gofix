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
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/appital/gofix/datadictionary"
)

// BenchmarkParseMessage parses repeatedly into a single Message, which reuses
// the field arena in doParsing. A live session allocates a fresh Message per
// inbound message instead - see BenchmarkParse in bench_parse_test.go, whose
// fresh/reused split measures the difference.
func BenchmarkParseMessage(b *testing.B) {
	rawMsg := bytes.NewBufferString(withValidCheckSum("8=FIX.4.2\x019=104\x0135=D\x0134=2\x0149=TW\x0152=20140515-19:49:56.659\x0156=ISLD\x0111=100\x0121=1\x0140=1\x0154=1\x0155=TSLA\x0160=00010101-00:00:00.000\x0110=039\x01"))

	msg := NewMessage()

	b.ReportAllocs()

	for b.Loop() {
		if err := ParseMessage(msg, rawMsg); err != nil {
			b.Fatal(err)
		}
	}
}

type MessageSuite struct {
	QuickFIXSuite
	msg *Message
}

func TestMessageSuite(t *testing.T) {
	suite.Run(t, new(MessageSuite))
}

func (s *MessageSuite) SetupTest() {
	s.msg = NewMessage()
}

func TestXMLNonFIX(t *testing.T) {
	rawMsg := bytes.NewBufferString("8=FIX.4.2\x019=372\x0135=n\x0134=25512\x01369=1481\x0152=20200522-07:05:33.756\x0149=CME\x0150=G\x0156=OAEAAAN\x0157=TRADE_CAPTURE\x01143=US,IL\x01212=261\x01213=<RTRF>8=FIX.4.2\x019=225\x0135=BZ\x0134=6549\x01369=6518\x0152=20200522-07:05:33.746\x0149=CME\x0150=G\x0156=9Q5000N\x0157=DUMMY\x01143=US,IL\x0111=ACP1590131133734\x0160=20200522-07:05:33.734\x01533=0\x01893=Y\x011028=Y\x011300=99\x011369=99612:32508\x011373=3\x011374=9\x011375=1\x015979=1590131133734617697\x0110=167\x01</RTRF>\x0110=245\x01\"")
	msg := NewMessage()
	_ = ParseMessage(msg, rawMsg)

	if !msg.Header.Has(tagXMLData) {
		t.Error("Expected xmldata tag")
	}
}

func (s *MessageSuite) TestParseMessageEmpty() {
	rawMsg := bytes.NewBufferString("")

	err := ParseMessage(s.msg, rawMsg)
	s.NotNil(err)
}

func (s *MessageSuite) TestParseXMLDataLenPastEnd() {
	for _, raw := range []string{
		"8=FIX.4.4\x019=12\x0135=0\x01212=50\x0110=000\x01",
		"8=FIX.4.4\x019=27\x0135=0\x01212=9223372036854775806\x0110=000\x01",
	} {
		s.NotPanics(func() {
			s.Error(ParseMessage(NewMessage(), bytes.NewBufferString(raw)), raw)
		}, raw)
	}
}

func (s *MessageSuite) TestParseMessage() {
	rawMsg := bytes.NewBufferString(withValidCheckSum("8=FIX.4.2\x019=104\x0135=D\x0134=2\x0149=TW\x0152=20140515-19:49:56.659\x0156=ISLD\x0111=100\x0121=1\x0140=1\x0154=1\x0155=TSLA\x0160=00010101-00:00:00.000\x0110=039\x01"))

	err := ParseMessage(s.msg, rawMsg)
	s.Nil(err)

	s.True(bytes.Equal(rawMsg.Bytes(), s.msg.rawMessage.Bytes()), "Expected msg bytes to equal raw bytes")

	expectedBodyBytes := []byte("11=100\x0121=1\x0140=1\x0154=1\x0155=TSLA\x0160=00010101-00:00:00.000\x01")

	s.True(bytes.Equal(s.msg.bodyBytes, expectedBodyBytes), "Incorrect body bytes, got %s", string(s.msg.bodyBytes))

	s.Equal(14, len(s.msg.fields))

	msgType, err := s.msg.MsgType()
	s.Nil(err)

	s.Equal("D", msgType)
	s.True(s.msg.IsMsgTypeOf("D"))

	s.False(s.msg.IsMsgTypeOf("A"))
}

func (s *MessageSuite) TestParseMessageWithDataDictionary() {
	dict := new(datadictionary.DataDictionary)
	dict.Header = &datadictionary.MessageDef{
		Fields: map[int]*datadictionary.FieldDef{
			10030: nil,
		},
	}
	dict.Trailer = &datadictionary.MessageDef{
		Fields: map[int]*datadictionary.FieldDef{
			5050: nil,
		},
	}
	rawMsg := bytes.NewBufferString(withValidCheckSum("8=FIX.4.2\x019=126\x0135=D\x0134=2\x0149=TW\x0152=20140515-19:49:56.659\x0156=ISLD\x0110030=CUST\x0111=100\x0121=1\x0140=1\x0154=1\x0155=TSLA\x0160=00010101-00:00:00.000\x015050=HELLO\x0110=039\x01"))

	err := ParseMessageWithDataDictionary(s.msg, rawMsg, dict, dict)
	s.Nil(err)
	s.FieldEquals(Tag(10030), "CUST", s.msg.Header)
	s.FieldEquals(Tag(5050), "HELLO", s.msg.Trailer)
}

func (s *MessageSuite) TestParseOutOfOrder() {
	// Allow fields out of order, save for validation.
	rawMsg := bytes.NewBufferString(withValidCheckSum("8=FIX.4.0\x019=81\x0135=D\x0111=id\x0121=3\x0138=100\x0140=1\x0154=1\x0155=MSFT\x0134=2\x0149=TW\x0152=20140521-22:07:09\x0156=ISLD\x0110=250\x01"))
	s.Nil(ParseMessage(s.msg, rawMsg))
}

func (s *MessageSuite) TestParseGroup_BodyFieldAfterNestedGroup() {
	dict, dictErr := datadictionary.Parse("spec/FIX44.xml")
	s.Nil(dictErr)

	// Wire layout (FIX 4.4 MassQuoteAcknowledgement):
	//   117=QID              QuoteID (body)
	//   296=1                NoQuoteSets count (body group)
	//     302=SET1           QuoteSetID (inside NoQuoteSets)
	//     295=1              NoQuoteEntries count (nested group)
	//       299=E1           QuoteEntryID (inside NoQuoteEntries)
	//       132=100          BidPx (inside NoQuoteEntries)
	//       133=101          OfferPx (inside NoQuoteEntries)
	//   297=0                QuoteStatus (BODY level, AFTER the group)
	rawMsg := bytes.NewBufferString(
		"8=FIX.4.4\x019=63\x0135=b\x01117=QID\x01" +
			"296=1\x01302=SET1\x01" +
			"295=1\x01299=E1\x01132=100\x01133=101\x01" +
			"297=1\x01" +
			"10=002\x01")

	err := ParseMessageWithDataDictionary(s.msg, rawMsg, dict, dict)
	s.Nil(err)

	rebuildBytes := s.msg.build()
	expectedBytes := rawMsg.Bytes()
	s.True(bytes.Equal(expectedBytes, rebuildBytes), "Unexpected bytes,\n +%s\n -%s", rebuildBytes, expectedBytes)

	// NoQuoteSets count (group delimiter) lands in Body as expected.
	s.True(s.msg.Body.Has(Tag(296)))

	// QuoteStatus (297) must be a top-level body field per FIX 4.4.
	s.True(s.msg.Body.Has(Tag(297)),
		"QuoteStatus (297) must land at the body level; it is a body field "+
			"in MassQuoteAcknowledgement, not a member of NoQuoteSets.")
	val, verr := s.msg.Body.GetInt(Tag(297))
	s.Nil(verr)
	s.Equal(1, val)
}

func (s *MessageSuite) TestBuild() {
	s.msg.Header.SetField(tagBeginString, FIXString(BeginStringFIX44))
	s.msg.Header.SetField(tagMsgType, FIXString("A"))
	s.msg.Header.SetField(tagSendingTime, FIXString("20140615-19:49:56"))

	s.msg.Body.SetField(Tag(553), FIXString("my_user"))
	s.msg.Body.SetField(Tag(554), FIXString("secret"))

	expectedBytes := []byte("8=FIX.4.4\x019=49\x0135=A\x0152=20140615-19:49:56\x01553=my_user\x01554=secret\x0110=072\x01")
	result := s.msg.build()
	s.True(bytes.Equal(expectedBytes, result), "Unexpected bytes, got %s", string(result))
}

func (s *MessageSuite) TestReBuild() {
	rawMsg := bytes.NewBufferString(withValidCheckSum("8=FIX.4.2\x019=104\x0135=D\x0134=2\x0149=TW\x0152=20140515-19:49:56.659\x0156=ISLD\x0111=100\x0121=1\x0140=1\x0154=1\x0155=TSLA\x0160=00010101-00:00:00.000\x0110=039\x01"))

	s.Nil(ParseMessage(s.msg, rawMsg))

	s.msg.Header.SetField(tagOrigSendingTime, FIXString("20140515-19:49:56.659"))
	s.msg.Header.SetField(tagSendingTime, FIXString("20140615-19:49:56"))
	s.msg.Header.SetField(tagPossDupFlag, FIXBoolean(true))

	rebuildBytes := s.msg.build()

	expectedBytes := []byte("8=FIX.4.2\x019=131\x0135=D\x0134=2\x0143=Y\x0149=TW\x0152=20140615-19:49:56\x0156=ISLD\x01122=20140515-19:49:56.659\x0111=100\x0121=1\x0140=1\x0154=1\x0155=TSLA\x0160=00010101-00:00:00.000\x0110=122\x01")

	s.True(bytes.Equal(expectedBytes, rebuildBytes), "Unexpected bytes,\n +%s\n -%s", rebuildBytes, expectedBytes)

	expectedBodyBytes := []byte("11=100\x0121=1\x0140=1\x0154=1\x0155=TSLA\x0160=00010101-00:00:00.000\x01")

	s.True(bytes.Equal(s.msg.bodyBytes, expectedBodyBytes), "Incorrect body bytes, got %s", string(s.msg.bodyBytes))
}

func (s *MessageSuite) TestRebuildOneRepeatingGroupWithDictionary() {
	dict, dictErr := datadictionary.Parse("spec/FIX44.xml")
	s.Nil(dictErr)

	// Given message bytes from a valid string with a 453 repeating group.
	rawMsg := bytes.NewBufferString(
		"8=FIX.4.4\x019=165\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x01453=1\x01448=4501\x01447=D\x01452=28\x01" +
			"10=026\x01")

	// When we parse it into a message
	s.Nil(ParseMessageWithDataDictionary(s.msg, rawMsg, dict, dict))

	// And then rebuild the message bytes
	rebuildBytes := s.msg.build()
	expectedBytes := []byte(
		"8=FIX.4.4\x019=165\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x01453=1\x01448=4501\x01447=D\x01452=28\x01" +
			"10=026\x01")

	// Then the bytes should have repeating groups properly ordered
	s.True(bytes.Equal(expectedBytes, rebuildBytes), "Unexpected bytes,\n expected: %s\n but got: %s", expectedBytes, rebuildBytes)
}

func (s *MessageSuite) TestRebuildTwoRepeatingGroupsWithDictionary() {
	dict, dictErr := datadictionary.Parse("spec/FIX44.xml")
	s.Nil(dictErr)

	// Given message bytes from a valid string with a 386 repeating group and a 453 repeating group.
	rawMsg := bytes.NewBufferString("8=FIX.4.4\x019=210\x0135=D\x0134=2\x01347=UTF-8\x0152=20231231-20:19:41\x0149=01001\x0150=01001a\x0156=TEST\x0144=12\x0111=13976\x011=10100400\x0121=1\x01386=1\x01336=NOPL\x0155=SYMABC\x0154=1\x0160=20231231-20:19:41\x0138=1\x0140=2\x0159=0\x01453=1\x01448=4501\x01447=D\x01452=28\x01354=6\x01355=Public\x0110=104\x01")

	// When we parse it into a message
	s.Nil(ParseMessageWithDataDictionary(s.msg, rawMsg, dict, dict))

	// And then rebuild the message bytes
	rebuildBytes := s.msg.build()
	expectedBytes := []byte("8=FIX.4.4\x019=210\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01347=UTF-8\x011=10100400\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x01354=6\x01355=Public\x01386=1\x01336=NOPL\x01453=1\x01448=4501\x01447=D\x01452=28\x0110=104\x01")

	// Then the bytes should have repeating groups properly ordered
	s.True(bytes.Equal(expectedBytes, rebuildBytes), "Unexpected bytes,\n expected: %s\n but got: %s", expectedBytes, rebuildBytes)
}

func (s *MessageSuite) TestRebuildOneRepeatingGroupWithTwoMembersWithDictionary() {
	dict, dictErr := datadictionary.Parse("spec/FIX44.xml")
	s.Nil(dictErr)

	// Given message bytes from a valid string with a 453 repeating group that has 2 child groups.
	rawMsg := bytes.NewBufferString(
		"8=FIX.4.4\x019=187\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x01453=2\x01448=4501\x01447=D\x01452=28\x01448=4502\x01447=D\x01452=28\x01" +
			"10=044\x01")

	// When we parse it into a message
	s.Nil(ParseMessageWithDataDictionary(s.msg, rawMsg, dict, dict))

	// And then rebuild the message bytes
	rebuildBytes := s.msg.build()
	expectedBytes := []byte(
		"8=FIX.4.4\x019=187\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x01453=2\x01448=4501\x01447=D\x01452=28\x01448=4502\x01447=D\x01452=28\x01" +
			"10=044\x01")

	// Then the bytes should have repeating groups properly ordered
	s.True(bytes.Equal(expectedBytes, rebuildBytes), "Unexpected bytes,\n expected: %s\n but got: %s", expectedBytes, rebuildBytes)
}

func (s *MessageSuite) TestRebuildTwoSequentialRepeatingGroupWithDictionary() {
	dict, dictErr := datadictionary.Parse("spec/FIX44.xml")
	s.Nil(dictErr)

	// Given message bytes from a valid string with both a 78 and 453 repeating group that each have 2 child groups.
	rawMsg := bytes.NewBufferString(
		"8=FIX.4.4\x019=210\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x0178=2\x0179=acct1\x0179=acct2\x01453=2\x01448=4501\x01447=D\x01452=28\x01448=4502\x01447=D\x01452=28\x01" +
			"10=243\x01")

	// When we parse it into a message
	s.Nil(ParseMessageWithDataDictionary(s.msg, rawMsg, dict, dict))

	// And then rebuild the message bytes
	rebuildBytes := s.msg.build()
	expectedBytes := []byte(
		"8=FIX.4.4\x019=210\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x0178=2\x0179=acct1\x0179=acct2\x01453=2\x01448=4501\x01447=D\x01452=28\x01448=4502\x01447=D\x01452=28\x01" +
			"10=243\x01")

	// Then the bytes should have repeating groups properly ordered
	s.True(bytes.Equal(expectedBytes, rebuildBytes), "Unexpected bytes,\n expected: %s\n but got: %s", expectedBytes, rebuildBytes)
}

func (s *MessageSuite) TestRebuildNestedRepeatingGroupWithDictionary() {
	dict, dictErr := datadictionary.Parse("spec/FIX44.xml")
	s.Nil(dictErr)

	// Given message bytes from a valid string with a 78 repeating group that has
	// a nested 539 group and then another 80 tag in the 78 group
	rawMsg := bytes.NewBufferString(
		"8=FIX.4.4\x019=177\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x0178=1\x0179=acct1\x01539=1\x01524=nestedid\x0180=100\x01" +
			"10=206\x01")

	// When we parse it into a message
	s.Nil(ParseMessageWithDataDictionary(s.msg, rawMsg, dict, dict))

	// And then rebuild the message bytes
	rebuildBytes := s.msg.build()
	expectedBytes := []byte(
		"8=FIX.4.4\x019=177\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x0178=1\x0179=acct1\x01539=1\x01524=nestedid\x0180=100\x01" +
			"10=206\x01")

	// Then the bytes should have repeating groups properly ordered
	s.True(bytes.Equal(expectedBytes, rebuildBytes), "Unexpected bytes,\n expected: %s\n but got: %s", expectedBytes, rebuildBytes)
}

func (s *MessageSuite) TestRebuildDoubleNestedRepeatingGroupWithDictionary() {
	dict, dictErr := datadictionary.Parse("spec/FIX44.xml")
	s.Nil(dictErr)

	// Given message bytes from a valid string with a 78 repeating group that has a
	// double nested 539 and then 804 groups and then another 80 tag in the 78 group
	rawMsg := bytes.NewBufferString(
		"8=FIX.4.4\x019=202\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x0178=1\x0179=acct1\x01539=1\x01524=nestedid\x01804=1\x01545=doublenestedid\x0180=100\x01" +
			"10=117\x01")

	// When we parse it into a message
	s.Nil(ParseMessageWithDataDictionary(s.msg, rawMsg, dict, dict))

	// And then rebuild the message bytes
	rebuildBytes := s.msg.build()
	expectedBytes := []byte(
		"8=FIX.4.4\x019=202\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x0178=1\x0179=acct1\x01539=1\x01524=nestedid\x01804=1\x01545=doublenestedid\x0180=100\x01" +
			"10=117\x01")

	// Then the bytes should have repeating groups properly ordered
	s.True(bytes.Equal(expectedBytes, rebuildBytes), "Unexpected bytes,\n expected: %s\n but got: %s", expectedBytes, rebuildBytes)
}

func (s *MessageSuite) TestRebuildDoubleNestedThenAnotherRepeatingGroupWithDictionary() {
	dict, dictErr := datadictionary.Parse("spec/FIX44.xml")
	s.Nil(dictErr)

	// Given message bytes from a valid string with a 78 repeating group that has a double nested 539 and then 804 groups
	// and then another repeating group 453 with two children.
	rawMsg := bytes.NewBufferString(
		"8=FIX.4.4\x019=245\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x0178=1\x0179=acct1\x01539=1\x01524=nestedid\x01804=1\x01545=doublenestedid\x01453=2\x01448=4501\x01447=D\x01452=28\x01448=4502\x01447=D\x01452=28\x01" +
			"10=106\x01")

	// When we parse it into a message
	s.Nil(ParseMessageWithDataDictionary(s.msg, rawMsg, dict, dict))

	// And then rebuild the message bytes
	rebuildBytes := s.msg.build()
	expectedBytes := []byte(
		"8=FIX.4.4\x019=245\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x0178=1\x0179=acct1\x01539=1\x01524=nestedid\x01804=1\x01545=doublenestedid\x01453=2\x01448=4501\x01447=D\x01452=28\x01448=4502\x01447=D\x01452=28\x01" +
			"10=106\x01")

	// Then the bytes should have repeating groups properly ordered
	s.True(bytes.Equal(expectedBytes, rebuildBytes), "Unexpected bytes,\n expected: %s\n but got: %s", expectedBytes, rebuildBytes)
}

func (s *MessageSuite) TestRebuildDoubleNestedThenBodyTagThenAnotherRepeatingGroupWithDictionary() {
	dict, dictErr := datadictionary.Parse("spec/FIX44.xml")
	s.Nil(dictErr)

	// Given message bytes from a valid string with a 78 repeating group that has a double nested 539 and then 804 groups
	// then a 376 body tag and then another repeating group 453 with two children.
	rawMsg := bytes.NewBufferString(
		"8=FIX.4.4\x019=256\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x0178=1\x0179=acct1\x01539=1\x01524=nestedid\x01804=1\x01545=doublenestedid\x01376=compid\x01453=2\x01448=4501\x01447=D\x01452=28\x01448=4502\x01447=D\x01452=28\x01" +
			"10=198\x01")

	// When we parse it into a message
	s.Nil(ParseMessageWithDataDictionary(s.msg, rawMsg, dict, dict))

	// And then rebuild the message bytes
	rebuildBytes := s.msg.build()
	expectedBytes := []byte(
		"8=FIX.4.4\x019=256\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x0178=1\x0179=acct1\x01539=1\x01524=nestedid\x01804=1\x01545=doublenestedid\x01376=compid\x01453=2\x01448=4501\x01447=D\x01452=28\x01448=4502\x01447=D\x01452=28\x01" +
			"10=198\x01")

	// Then the bytes should have repeating groups properly ordered
	s.True(bytes.Equal(expectedBytes, rebuildBytes), "Unexpected bytes,\n expected: %s\n but got: %s", expectedBytes, rebuildBytes)
}

func (s *MessageSuite) TestRebuildDoubleNestedWithTwoMembersRepeatingGroupWithDictionary() {
	dict, dictErr := datadictionary.Parse("spec/FIX44.xml")
	s.Nil(dictErr)

	// Given message bytes from a valid string with a 78 repeating group that
	// has a double nested 539 and then 804 groups all with two children.
	rawMsg := bytes.NewBufferString(
		"8=FIX.4.4\x019=406\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x01" +
			"78=1\x0179=acct1\x01" +
			"539=2\x01" +
			"524=nestedid\x01" +
			"804=2\x01" +
			"545=doublenestedid\x01" +
			"545=doublenestedid2\x01" +
			"524=nestedid2\x01" +
			"804=2\x01" +
			"545=doublenestedid\x01" +
			"545=doublenestedid2\x01" +
			"79=acct2\x01" +
			"539=2\x01" +
			"524=nestedid\x01" +
			"804=2\x01" +
			"545=doublenestedid\x01" +
			"545=doublenestedid2\x01" +
			"524=nestedid2\x01" +
			"804=2\x01" +
			"545=doublenestedid\x01" +
			"545=doublenestedid2\x01" +
			"10=046\x01")

	// When we parse it into a message
	s.Nil(ParseMessageWithDataDictionary(s.msg, rawMsg, dict, dict))

	// And then rebuild the message bytes
	rebuildBytes := s.msg.build()
	expectedBytes := []byte(
		"8=FIX.4.4\x019=406\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x01" +
			"78=1\x0179=acct1\x01539=2\x01524=nestedid\x01804=2\x01545=doublenestedid\x01545=doublenestedid2\x01524=nestedid2\x01804=2\x01545=doublenestedid\x01545=doublenestedid2\x01" +
			"79=acct2\x01539=2\x01524=nestedid\x01804=2\x01545=doublenestedid\x01545=doublenestedid2\x01524=nestedid2\x01804=2\x01545=doublenestedid\x01545=doublenestedid2\x01" +
			"10=046\x01")

	// Then the bytes should have repeating groups properly ordered
	s.True(bytes.Equal(expectedBytes, rebuildBytes), "Unexpected bytes,\n expected: %s\n but got: %s", expectedBytes, rebuildBytes)
}

func (s *MessageSuite) TestReBuildWithRepeatingGroupForResend() {
	// Given the following message with a repeating group
	origHeader := "8=FIXT.1.1\x019=161\x0135=8\x0134=3\x0149=ISLD\x0152=20240415-03:43:17.923\x0156=TW\x01"
	origBody := "6=1.00\x0111=1\x0114=1.00\x0117=1\x0131=1.00\x0132=1.00\x0137=1\x0138=1.00\x0139=2\x0154=1\x0155=1\x01150=2\x01151=0.00\x01453=1\x01448=xyzzy\x01447=D\x01452=1\x01"
	origTrailer := "10=014\x01"
	rawMsg := bytes.NewBufferString(origHeader + origBody + origTrailer)

	// When I reparse the message from the store during a resend request
	s.Nil(ParseMessage(s.msg, rawMsg))

	// And I update the headers for resend
	s.msg.Header.SetField(tagOrigSendingTime, FIXString("20240415-03:43:17.923"))
	s.msg.Header.SetField(tagSendingTime, FIXString("20240415-14:41:23.456"))
	s.msg.Header.SetField(tagPossDupFlag, FIXBoolean(true))

	// When I rebuild the message
	rebuildBytes := s.msg.build()

	// Then the repeating groups will not be in the correct order in the rebuilt message (note tags 447, 448, 452, 453)
	expectedBytes := []byte("8=FIXT.1.1\x019=192\x0135=8\x0134=3\x0143=Y\x0149=ISLD\x0152=20240415-14:41:23.456\x0156=TW\x01122=20240415-03:43:17.923\x016=1.00\x0111=1\x0114=1.00\x0117=1\x0131=1.00\x0132=1.00\x0137=1\x0138=1.00\x0139=2\x0154=1\x0155=1\x01150=2\x01151=0.00\x01453=1\x01448=xyzzy\x01447=D\x01452=1\x0110=018\x01")
	s.False(bytes.Equal(expectedBytes, rebuildBytes), "Unexpected bytes,\n expected: %s\n  but was: %s", expectedBytes, rebuildBytes)
	expectedOutOfOrderBytes := []byte("8=FIXT.1.1\x019=192\x0135=8\x0134=3\x0143=Y\x0149=ISLD\x0152=20240415-14:41:23.456\x0156=TW\x01122=20240415-03:43:17.923\x016=1.00\x0111=1\x0114=1.00\x0117=1\x0131=1.00\x0132=1.00\x0137=1\x0138=1.00\x0139=2\x0154=1\x0155=1\x01150=2\x01151=0.00\x01447=D\x01448=xyzzy\x01452=1\x01453=1\x0110=018\x01")
	s.True(bytes.Equal(expectedOutOfOrderBytes, rebuildBytes), "Unexpected bytes,\n expected: %s\n  but was: %s", expectedOutOfOrderBytes, rebuildBytes)

	// But the bodyBytes will still be correct
	origBodyBytes := []byte(origBody)
	s.True(bytes.Equal(origBodyBytes, s.msg.bodyBytes), "Incorrect body bytes, \n expected: %s\n  but was: %s", origBodyBytes, s.msg.bodyBytes)

	// So when I combine the updated header + the original bodyBytes + the as-is trailer
	resendBytes := s.msg.buildWithBodyBytes(s.msg.bodyBytes)

	// Then the reparsed, rebuilt message will retain the correct ordering of repeating group tags during resend
	s.True(bytes.Equal(expectedBytes, resendBytes), "Unexpected bytes,\n expected: %s\n  but was: %s", expectedBytes, resendBytes)
}

func (s *MessageSuite) TestReBuildWithRepeatingGroupMultipleEntriesInGroupForResend() {
	// Given the following message with a repeating group that has 2 entries
	origHeader := "8=FIXT.1.1\x019=184\x0135=8\x0134=3\x0149=ISLD\x0152=20240415-03:43:17.923\x0156=TW\x01"
	origBody := "6=1.00\x0111=1\x0114=1.00\x0117=1\x0131=1.00\x0132=1.00\x0137=1\x0138=1.00\x0139=2\x0154=1\x0155=1\x01150=2\x01151=0.00\x01453=2\x01448=xyzzy\x01447=D\x01452=1\x01448=foobar\x01447=D\x01452=3\x01"
	origTrailer := "10=152\x01"
	rawMsg := bytes.NewBufferString(origHeader + origBody + origTrailer)

	// When I reparse the message from the store during a resend request
	s.Nil(ParseMessage(s.msg, rawMsg))

	// And I update the headers for resend
	s.msg.Header.SetField(tagOrigSendingTime, FIXString("20240415-03:43:17.923"))
	s.msg.Header.SetField(tagSendingTime, FIXString("20240415-14:41:23.456"))
	s.msg.Header.SetField(tagPossDupFlag, FIXBoolean(true))

	// The bodyBytes will still be correct
	origBodyBytes := []byte(origBody)
	s.True(bytes.Equal(origBodyBytes, s.msg.bodyBytes), "Incorrect body bytes, \n expected: %s\n  but was: %s", origBodyBytes, s.msg.bodyBytes)

	// So when I combine the updated header + the original bodyBytes + the as-is trailer
	resendBytes := s.msg.buildWithBodyBytes(origBodyBytes)

	// Then the reparsed, rebuilt message will retain the correct ordering of repeating group tags during resend
	expectedResendHeader := "8=FIXT.1.1\x019=215\x0135=8\x0134=3\x0143=Y\x0149=ISLD\x0152=20240415-14:41:23.456\x0156=TW\x01122=20240415-03:43:17.923\x01"
	expectedResendBody := "6=1.00\x0111=1\x0114=1.00\x0117=1\x0131=1.00\x0132=1.00\x0137=1\x0138=1.00\x0139=2\x0154=1\x0155=1\x01150=2\x01151=0.00\x01453=2\x01448=xyzzy\x01447=D\x01452=1\x01448=foobar\x01447=D\x01452=3\x01"
	expectedResendTrailer := "10=147\x01"
	expectedResendBytes := []byte(expectedResendHeader + expectedResendBody + expectedResendTrailer)
	s.True(bytes.Equal(expectedResendBytes, resendBytes), "Unexpected bytes,\n expected: %s\n  but was: %s", expectedResendBytes, resendBytes)
}

func (s *MessageSuite) TestReBuildWithRepeatingGroupWithDictionaryForResend() {
	dict, dictErr := datadictionary.Parse("spec/FIX44.xml")
	s.Nil(dictErr)

	// A message whose body ends in a repeating group (453), parsed WITH a
	// dictionary so parseGroup handles the group.
	rawMsg := bytes.NewBufferString(
		"8=FIX.4.4\x019=165\x0135=D\x0134=2\x0149=01001\x0150=01001a\x0152=20231231-20:19:41\x0156=TEST\x01" +
			"1=acct1\x0111=13976\x0121=1\x0138=1\x0140=2\x0144=12\x0154=1\x0155=SYMABC\x0159=0\x0160=20231231-20:19:41\x01453=1\x01448=4501\x01447=D\x01452=28\x01" +
			"10=026\x01")
	s.Nil(ParseMessageWithDataDictionary(s.msg, rawMsg, dict, dict))

	// The trailer must not stay embedded in bodyBytes.
	s.False(bytes.Contains(s.msg.bodyBytes, []byte("\x0110=")), "trailer leaked into bodyBytes: %s", s.msg.bodyBytes)

	// A resend rebuilt from bodyBytes must carry exactly one 10= checksum.
	resendBytes := s.msg.buildWithBodyBytes(s.msg.bodyBytes)
	s.Equal(1, bytes.Count(resendBytes, []byte("\x0110=")), "expected exactly one 10= checksum, got: %s", resendBytes)
}

func (s *MessageSuite) TestReverseRoute() {
	s.Nil(ParseMessage(s.msg, bytes.NewBufferString(withValidCheckSum("8=FIX.4.2\x019=171\x0135=D\x0134=2\x0149=TW\x0150=KK\x0152=20060102-15:04:05\x0156=ISLD\x0157=AP\x01144=BB\x01115=JCD\x01116=CS\x01128=MG\x01129=CB\x01142=JV\x01143=RY\x01145=BH\x0111=ID\x0121=3\x0138=100\x0140=w\x0154=1\x0155=INTC\x0160=20060102-15:04:05\x0110=123\x01"))))

	builder := s.msg.reverseRoute()

	var testCases = []struct {
		tag           Tag
		expectedValue string
	}{
		{tagTargetCompID, "TW"},
		{tagTargetSubID, "KK"},
		{tagTargetLocationID, "JV"},
		{tagSenderCompID, "ISLD"},
		{tagSenderSubID, "AP"},
		{tagSenderLocationID, "RY"},
		{tagDeliverToCompID, "JCD"},
		{tagDeliverToSubID, "CS"},
		{tagDeliverToLocationID, "BB"},
		{tagOnBehalfOfCompID, "MG"},
		{tagOnBehalfOfSubID, "CB"},
		{tagOnBehalfOfLocationID, "BH"},
	}

	for _, tc := range testCases {
		var field FIXString
		s.Nil(builder.Header.GetField(tc.tag, &field))

		s.Equal(tc.expectedValue, string(field))
	}
}

func (s *MessageSuite) TestReverseRouteIgnoreEmpty() {
	s.Nil(ParseMessage(s.msg, bytes.NewBufferString(withValidCheckSum("8=FIX.4.0\x019=128\x0135=D\x0134=2\x0149=TW\x0152=20060102-15:04:05\x0156=ISLD\x01115=\x01116=CS\x01128=MG\x01129=CB\x0111=ID\x0121=3\x0138=100\x0140=w\x0154=1\x0155=INTC\x0160=20060102-15:04:05\x0110=123\x01"))))
	builder := s.msg.reverseRoute()

	s.False(builder.Header.Has(tagDeliverToCompID), "Should not reverse if empty")
}

func (s *MessageSuite) TestReverseRouteFIX40() {
	// The onbehalfof/deliverto location id not supported in fix 4.0.
	s.Nil(ParseMessage(s.msg, bytes.NewBufferString(withValidCheckSum("8=FIX.4.0\x019=171\x0135=D\x0134=2\x0149=TW\x0150=KK\x0152=20060102-15:04:05\x0156=ISLD\x0157=AP\x01144=BB\x01115=JCD\x01116=CS\x01128=MG\x01129=CB\x01142=JV\x01143=RY\x01145=BH\x0111=ID\x0121=3\x0138=100\x0140=w\x0154=1\x0155=INTC\x0160=20060102-15:04:05\x0110=123\x01"))))

	builder := s.msg.reverseRoute()

	s.False(builder.Header.Has(tagDeliverToLocationID), "delivertolocation id not supported in fix40")

	s.False(builder.Header.Has(tagOnBehalfOfLocationID), "onbehalfof location id not supported in fix40")
}

func (s *MessageSuite) TestCopyIntoMessage() {
	msgString := withValidCheckSum("8=FIX.4.2\x019=171\x0135=D\x0134=2\x0149=TW\x0150=KK\x0152=20060102-15:04:05\x0156=ISLD\x0157=AP\x01144=BB\x01115=JCD\x01116=CS\x01128=MG\x01129=CB\x01142=JV\x01143=RY\x01145=BH\x0111=ID\x0121=3\x0138=100\x0140=w\x0154=1\x0155=INTC\x0160=20060102-15:04:05\x0110=123\x01")
	msgBuf := bytes.NewBufferString(msgString)
	s.Nil(ParseMessage(s.msg, msgBuf))

	dest := NewMessage()
	s.msg.CopyInto(dest)

	checkFieldInt(s, dest.Header.FieldMap, int(tagMsgSeqNum), 2)
	checkFieldInt(s, dest.Body.FieldMap, 21, 3)
	checkFieldString(s, dest.Body.FieldMap, 11, "ID")
	s.Equal(len(dest.bodyBytes), len(s.msg.bodyBytes))

	// copying decouples the message from its input buffer, so the raw message will be re-rendered
	renderedString := "8=FIX.4.2\x019=171\x0135=D\x0134=2\x0149=TW\x0150=KK\x0152=20060102-15:04:05\x0156=ISLD\x0157=AP\x01115=JCD\x01116=CS\x01128=MG\x01129=CB\x01142=JV\x01143=RY\x01144=BB\x01145=BH\x0111=ID\x0121=3\x0138=100\x0140=w\x0154=1\x0155=INTC\x0160=20060102-15:04:05\x0110=033\x01"
	s.Equal(dest.String(), renderedString)

	s.True(reflect.DeepEqual(s.msg.bodyBytes, dest.bodyBytes))
	s.True(s.msg.IsMsgTypeOf("D"))
	s.Equal(s.msg.ReceiveTime, dest.ReceiveTime)

	s.True(reflect.DeepEqual(s.msg.fields, dest.fields))

	// update the source message to validate the copy is truly deep
	newMsgString := "8=FIX.4.4\x019=49\x0135=A\x0152=20140615-19:49:56\x01553=my_user\x01554=secret\x0110=072\x01"
	s.Nil(ParseMessage(s.msg, bytes.NewBufferString(newMsgString)))
	s.True(s.msg.IsMsgTypeOf("A"))
	s.Equal(s.msg.String(), newMsgString)
	s.Equal(string(s.msg.Bytes()), newMsgString)

	// clear the source buffer also
	msgBuf.Reset()

	s.True(dest.IsMsgTypeOf("D"))
	s.Equal(dest.String(), renderedString)
	s.Equal(string(dest.Bytes()), renderedString)
}

func checkFieldInt(s *MessageSuite, fields FieldMap, tag, expected int) {
	toCheck, _ := fields.GetInt(Tag(tag))
	s.Equal(expected, toCheck)
}

func checkFieldString(s *MessageSuite, fields FieldMap, tag int, expected string) {
	toCheck, err := fields.GetString(Tag(tag))
	s.NoError(err)
	s.Equal(expected, toCheck)
}

func TestCheckSumDigits(t *testing.T) {
	for v := 0; v < 256; v++ {
		digits := checkSumDigits(v)
		if got, want := string(digits[:]), fmt.Sprintf("%03d", v); got != want {
			t.Errorf("checkSumDigits(%v) = %q, want %q", v, got, want)
		}
	}
}

func newBuildTestMessage() *Message {
	m := NewMessage()
	m.Header.SetString(tagBeginString, "FIX.4.4")
	m.Header.SetString(tagMsgType, "D")
	m.Header.SetString(tagSenderCompID, "S")
	m.Header.SetString(tagTargetCompID, "T")
	m.Body.SetString(Tag(55), "ABC")
	m.Body.SetString(Tag(11), "id")
	return m
}

// wireLengthAndCheckSum computes BodyLength and CheckSum from built bytes.
func wireLengthAndCheckSum(t *testing.T, raw []byte) (int, int) {
	t.Helper()
	fields := bytes.Split(bytes.TrimSuffix(raw, []byte{'\x01'}), []byte{'\x01'})
	if len(fields) < 3 {
		t.Fatalf("too few fields in %q", raw)
	}
	bodyStart := len(fields[0]) + 1 + len(fields[1]) + 1
	checkSumStart := len(raw) - len(fields[len(fields)-1]) - 1
	return checkSumStart - bodyStart, bytesTotal(raw[:checkSumStart]) % 256
}

func assertBuildConsistent(t *testing.T, raw []byte) {
	t.Helper()
	msg := NewMessage()
	if err := ParseMessage(msg, bytes.NewBuffer(bytes.Clone(raw))); err != nil {
		t.Fatalf("built message %q does not parse: %v", raw, err)
	}
	wantLength, wantCheckSum := wireLengthAndCheckSum(t, raw)
	if got, _ := msg.Header.GetInt(tagBodyLength); got != wantLength {
		t.Errorf("BodyLength = %v, want %v in %q", got, wantLength, raw)
	}
	if got, _ := msg.Trailer.GetInt(tagCheckSum); got != wantCheckSum {
		t.Errorf("CheckSum = %v, want %v in %q", got, wantCheckSum, raw)
	}
}

func TestBuildIsSizedExactly(t *testing.T) {
	raw := newBuildTestMessage().build()
	assertBuildConsistent(t, raw)
	if len(raw) != cap(raw) {
		t.Errorf("built %v bytes into a buffer of %v", len(raw), cap(raw))
	}
}

func TestBuildAgainAfterChange(t *testing.T) {
	m := newBuildTestMessage()
	first := m.build()
	if again := m.build(); !bytes.Equal(first, again) {
		t.Fatalf("rebuilding an unchanged message gave %q, want %q", again, first)
	}

	// A tag that sorts first, added after a build, must still be written in
	// order and counted.
	m.Body.SetString(Tag(1), "acct")
	m.Header.SetString(tagMsgSeqNum, "7")
	raw := m.build()
	assertBuildConsistent(t, raw)
	if !bytes.Contains(raw, []byte("\x0134=7\x0149=S\x0156=T\x011=acct\x0111=id\x0155=ABC\x0110=")) {
		t.Errorf("fields out of order in %q", raw)
	}
}

func TestBuildAfterRemoveAndSet(t *testing.T) {
	m := newBuildTestMessage()
	m.Body.Remove(Tag(55))
	m.Body.SetString(Tag(55), "XYZ")
	m.Body.Remove(Tag(999)) // Absent tags are ignored.

	raw := m.build()
	assertBuildConsistent(t, raw)
	if n := bytes.Count(raw, []byte("\x0155=")); n != 1 {
		t.Errorf("tag 55 written %v times in %q", n, raw)
	}
}

func (s *MessageSuite) TestParseEmptyIntField() {
	dict, err := datadictionary.Parse("spec/FIX44.xml")
	s.Require().Nil(err)

	raw := "8=FIX.4.4\x019=5\x0135=0\x019=\x0110=000\x01"
	s.NotPanics(func() {
		s.Error(ParseMessageWithDataDictionary(NewMessage(), bytes.NewBufferString(raw), dict, dict))
	})
}

func (s *MessageSuite) TestParseMalformedReturnsError() {
	for _, raw := range []string{"8=\x01", "8=FIX.4.4\x019=5\x01", "8=0\x019=0\x0135=0\x010=0\x01"} {
		s.NotPanics(func() {
			s.Error(ParseMessage(NewMessage(), bytes.NewBufferString(raw)), raw)
		}, raw)
	}
}
