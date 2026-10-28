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
	"testing"

	"github.com/stretchr/testify/suite"
)

type MessageRouterTestSuite struct {
	suite.Suite
	*MessageRouter
	testRegistry    *Registry
	msg             *Message
	sessionID       SessionID
	returnReject    MessageRejectError
	routedBy        string
	routedSessionID SessionID
	routedMessage   *Message
}

func TestMessageRouterTestSuite(t *testing.T) {
	suite.Run(t, new(MessageRouterTestSuite))
}

func (suite *MessageRouterTestSuite) givenTheRoute(beginString, msgType string) {
	suite.AddRoute(
		beginString,
		msgType,
		func(msg *Message, sessionID SessionID) MessageRejectError {
			suite.routedBy = fmt.Sprintf("%v:%v", beginString, msgType)
			suite.routedSessionID = sessionID
			suite.routedMessage = msg

			return suite.returnReject
		},
	)
}

func (suite *MessageRouterTestSuite) givenTheMessage(msgBytes []byte) {
	err := ParseMessage(suite.msg, bytes.NewBufferString(withValidCheckSum(string(msgBytes))))
	suite.Nil(err)

	var beginString FIXString
	suite.Require().Nil(suite.msg.Header.GetField(tagBeginString, &beginString))
	var senderCompID FIXString
	suite.Require().Nil(suite.msg.Header.GetField(tagSenderCompID, &senderCompID))
	var targetCompID FIXString
	suite.Require().Nil(suite.msg.Header.GetField(tagTargetCompID, &targetCompID))
	suite.sessionID = SessionID{BeginString: string(beginString), SenderCompID: string(targetCompID), TargetCompID: string(senderCompID)}
}

func (suite *MessageRouterTestSuite) givenTargetDefaultApplVerIDForSession(defaultApplVerID string, sessionID SessionID) {
	s := &session{
		sessionID:              sessionID,
		targetDefaultApplVerID: defaultApplVerID,
	}
	suite.Nil(suite.testRegistry.register(s))
}

func (suite *MessageRouterTestSuite) givenAFIX42NewOrderSingle() {
	suite.givenTheMessage([]byte("8=FIX.4.2\x019=87\x0135=D\x0149=TW\x0134=3\x0156=ISLD\x0152=20160421-14:43:50\x0140=1\x0160=20160421-14:43:50\x0154=1\x0121=3\x0111=id\x0110=235\x01"))
}

func (suite *MessageRouterTestSuite) givenAFIXTLogonMessage() {
	suite.givenTheMessage([]byte("8=FIXT.1.1\x019=63\x0135=A\x0134=1\x0149=TW\x0152=20160420-21:21:49\x0156=ISLD\x0198=0\x01108=2\x011137=8\x0110=105\x01"))
}

func (suite *MessageRouterTestSuite) anticipateReject(rej MessageRejectError) {
	suite.returnReject = rej
}

func (suite *MessageRouterTestSuite) verifyMessageNotRouted() {
	suite.Equal("", suite.routedBy, "Message should not be routed")
}

func (suite *MessageRouterTestSuite) verifyMessageRoutedBy(beginString, msgType string) {
	suite.NotEqual("", suite.routedBy, "Message expected to be routed")

	suite.Equal(fmt.Sprintf("%v:%v", beginString, msgType), suite.routedBy)
	suite.Equal(suite.sessionID, suite.routedSessionID)
	suite.Equal(suite.msg.String(), suite.routedMessage.String())
}

func (suite *MessageRouterTestSuite) resetRouter() {
	suite.MessageRouter = NewMessageRouter(WithRegistry(suite.testRegistry))
	suite.routedBy = ""
	suite.routedSessionID = SessionID{}
	suite.routedMessage = &Message{}
	suite.returnReject = nil
}

func (suite *MessageRouterTestSuite) SetupTest() {
	suite.testRegistry = NewRegistry()
	suite.resetRouter()
	suite.msg = NewMessage()
}

func (suite *MessageRouterTestSuite) TestNoRoute() {
	suite.givenTheMessage([]byte("8=FIX.4.3\x019=87\x0135=D\x0149=TW\x0134=3\x0156=ISLD\x0152=20160421-14:43:50\x0140=1\x0160=20160421-14:43:50\x0154=1\x0121=3\x0111=id\x0110=235\x01"))

	rej := suite.Route(suite.msg, suite.sessionID)
	suite.verifyMessageNotRouted()
	suite.Equal(NewBusinessMessageRejectError("Unsupported Message Type", 3, nil), rej)
}

func (suite *MessageRouterTestSuite) TestNoRouteWhitelistedMessageTypes() {
	var tests = []string{"0", "A", "1", "2", "3", "4", "5", "j"}

	for _, test := range tests {
		suite.SetupTest()

		msg := fmt.Sprintf("8=FIX.4.3\x019=87\x0135=%v\x0149=TW\x0134=3\x0156=ISLD\x0152=20160421-14:43:50\x0140=1\x0160=20160421-14:43:50\x0154=1\x0121=3\x0111=id\x0110=235\x01", test)
		suite.givenTheMessage([]byte(msg))

		rej := suite.Route(suite.msg, suite.sessionID)
		suite.verifyMessageNotRouted()
		suite.Nil(rej, "Message type '%v' should not be rejected by the MessageRouter", test)
	}
}

func (suite *MessageRouterTestSuite) TestSimpleRoute() {
	suite.givenTheRoute(string(BeginStringFIX42), "D")
	suite.givenTheRoute(string(BeginStringFIXT11), "A")

	suite.givenAFIX42NewOrderSingle()
	rej := suite.Route(suite.msg, suite.sessionID)

	suite.verifyMessageRoutedBy(string(BeginStringFIX42), "D")
	suite.Nil(rej)
}

func (suite *MessageRouterTestSuite) TestSimpleRouteWithReject() {
	suite.givenTheRoute(string(BeginStringFIX42), "D")
	suite.givenTheRoute(string(BeginStringFIXT11), "A")
	suite.anticipateReject(NewMessageRejectError("some error", 5, nil))

	suite.givenAFIX42NewOrderSingle()
	rej := suite.Route(suite.msg, suite.sessionID)
	suite.verifyMessageRoutedBy(string(BeginStringFIX42), "D")
	suite.Equal(suite.returnReject, rej)
}

func (suite *MessageRouterTestSuite) TestRouteFIXTAdminMessage() {
	suite.givenTheRoute(string(BeginStringFIX42), "D")
	suite.givenTheRoute(string(BeginStringFIXT11), "A")
	suite.givenAFIXTLogonMessage()

	rej := suite.Route(suite.msg, suite.sessionID)
	suite.verifyMessageRoutedBy(string(BeginStringFIXT11), "A")
	suite.Nil(rej)
}

func (suite *MessageRouterTestSuite) TestRouteFIXT50AppWithApplVerID() {
	suite.givenTheRoute(BeginStringFIX42, "D")
	suite.givenTheRoute(ApplVerIDFIX50, "D")
	suite.givenTheRoute(ApplVerIDFIX50SP1, "D")

	suite.givenTheMessage([]byte("8=FIXT.1.1\x019=89\x0135=D\x0149=TW\x0134=3\x0156=ISLD\x0152=20160424-16:48:26\x011128=7\x0140=1\x0160=20160424-16:48:26\x0111=id\x0121=3\x0110=120\x01"))
	rej := suite.Route(suite.msg, suite.sessionID)
	suite.verifyMessageRoutedBy(ApplVerIDFIX50, "D")
	suite.Nil(rej)
}

func (suite *MessageRouterTestSuite) TestRouteFIXTAppWithApplVerID() {
	suite.givenTheRoute(BeginStringFIX42, "D")
	suite.givenTheRoute(ApplVerIDFIX50, "D")
	suite.givenTheRoute(ApplVerIDFIX50SP1, "D")

	suite.givenTheMessage([]byte("8=FIXT.1.1\x019=89\x0135=D\x0149=TW\x0134=3\x0156=ISLD\x0152=20160424-16:48:26\x011128=8\x0140=1\x0160=20160424-16:48:26\x0111=id\x0121=3\x0110=120\x01"))
	rej := suite.Route(suite.msg, suite.sessionID)
	suite.verifyMessageRoutedBy(ApplVerIDFIX50SP1, "D")
	suite.Nil(rej)
}

func (suite *MessageRouterTestSuite) TestRouteFIXTAppWithDefaultApplVerID() {
	suite.givenTheRoute(BeginStringFIX42, "D")
	suite.givenTheRoute(ApplVerIDFIX50, "D")
	suite.givenTheRoute(ApplVerIDFIX50SP1, "D")
	suite.givenTargetDefaultApplVerIDForSession(
		"8",
		SessionID{BeginString: string(BeginStringFIXT11), SenderCompID: "ISLD", TargetCompID: "TW"},
	)

	suite.givenTheMessage([]byte("8=FIXT.1.1\x019=82\x0135=D\x0149=TW\x0134=3\x0156=ISLD\x0152=20160424-16:48:26\x0140=1\x0160=20160424-16:48:26\x0111=id\x0121=3\x0110=120\x01"))
	rej := suite.Route(suite.msg, suite.sessionID)
	suite.verifyMessageRoutedBy(ApplVerIDFIX50SP1, "D")
	suite.Nil(rej)
}
