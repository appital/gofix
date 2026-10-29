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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/appital/gofix/internal"
)

type LogonStateTestSuite struct {
	SessionSuiteRig
}

func TestLogonStateTestSuite(t *testing.T) {
	suite.Run(t, new(LogonStateTestSuite))
}

func (s *LogonStateTestSuite) SetupTest() {
	s.Init()
	s.session.stateMachine.State = logonState{}
}

func (s *LogonStateTestSuite) TestPreliminary() {
	s.False(s.session.IsLoggedOn())
	s.True(s.session.IsConnected())
	s.True(s.session.IsSessionTime())
}

func (s *LogonStateTestSuite) TestTimeoutLogonTimeout() {
	s.Timeout(s.session, internal.LogonTimeout)
	s.State(latentState{})
}

func (s *LogonStateTestSuite) TestTimeoutLogonTimeoutInitiatedLogon() {
	s.session.InitiateLogon = true

	s.MockApp.On("OnLogout")
	s.Timeout(s.session, internal.LogonTimeout)

	s.MockApp.AssertExpectations(s.T())
	s.State(latentState{})
}

func (s *LogonStateTestSuite) TestTimeoutNotLogonTimeout() {
	tests := []internal.Event{internal.PeerTimeout, internal.NeedHeartbeat, internal.LogoutTimeout}

	for _, test := range tests {
		s.Timeout(s.session, test)
		s.State(logonState{})
	}
}

func (s *LogonStateTestSuite) TestDisconnected() {
	s.session.Disconnected(s.session)
	s.State(latentState{})
}

func (s *LogonStateTestSuite) TestFixMsgInNotLogon() {
	s.fixMsgIn(s.session, s.NewOrderSingle())

	s.MockApp.AssertExpectations(s.T())
	s.State(latentState{})
	s.NextTargetMsgSeqNum(1)
}

func (s *LogonStateTestSuite) TestFixMsgInLogon() {
	s.IncrNextSenderMsgSeqNum()
	s.MessageFactory.seqNum = 1
	s.IncrNextTargetMsgSeqNum()

	logon := s.Logon()
	logon.Body.SetField(tagHeartBtInt, FIXInt(32))

	s.MockApp.On("FromAdmin").Return(nil)
	s.MockApp.On("OnLogon")
	s.MockApp.On("ToAdmin")
	s.Zero(s.session.HeartBtInt)
	s.fixMsgIn(s.session, logon)

	s.MockApp.AssertExpectations(s.T())

	s.State(inSession{})
	s.Equal(32*time.Second, s.session.HeartBtInt) // Should be written from logon message.
	s.False(s.session.HeartBtIntOverride)

	s.LastToAdminMessageSent()
	s.MessageType(string(msgTypeLogon), s.MockApp.lastToAdmin)
	s.FieldEquals(tagHeartBtInt, 32, s.MockApp.lastToAdmin.Body)

	s.NextTargetMsgSeqNum(3)
	s.NextSenderMsgSeqNum(3)
}

func (s *LogonStateTestSuite) TestFixMsgInLogonHeartBtIntOverride() {
	s.IncrNextSenderMsgSeqNum()
	s.MessageFactory.seqNum = 1
	s.IncrNextTargetMsgSeqNum()

	logon := s.Logon()
	logon.Body.SetField(tagHeartBtInt, FIXInt(32))

	s.MockApp.On("FromAdmin").Return(nil)
	s.MockApp.On("OnLogon")
	s.MockApp.On("ToAdmin")
	s.session.HeartBtIntOverride = true
	s.session.HeartBtInt = time.Second
	s.fixMsgIn(s.session, logon)

	s.MockApp.AssertExpectations(s.T())

	s.State(inSession{})
	s.Equal(time.Second, s.session.HeartBtInt) // Should not have changed.
	s.True(s.session.HeartBtIntOverride)

	s.LastToAdminMessageSent()
	s.MessageType(string(msgTypeLogon), s.MockApp.lastToAdmin)
	s.FieldEquals(tagHeartBtInt, 1, s.MockApp.lastToAdmin.Body)

	s.NextTargetMsgSeqNum(3)
	s.NextSenderMsgSeqNum(3)
}

func (s *LogonStateTestSuite) TestFixMsgInLogonEnableLastMsgSeqNumProcessed() {
	s.session.EnableLastMsgSeqNumProcessed = true

	s.MessageFactory.SetNextSeqNum(2)
	s.IncrNextSenderMsgSeqNum()
	s.IncrNextTargetMsgSeqNum()

	logon := s.Logon()
	logon.Body.SetField(tagHeartBtInt, FIXInt(32))

	s.MockApp.On("FromAdmin").Return(nil)
	s.MockApp.On("OnLogon")
	s.MockApp.On("ToAdmin")
	s.fixMsgIn(s.session, logon)

	s.MockApp.AssertExpectations(s.T())

	s.LastToAdminMessageSent()
	s.MessageType(string(msgTypeLogon), s.MockApp.lastToAdmin)
	s.FieldEquals(tagLastMsgSeqNumProcessed, 2, s.MockApp.lastToAdmin.Header)
}

func (s *LogonStateTestSuite) TestFixMsgInLogonResetSeqNum() {
	s.IncrNextTargetMsgSeqNum()

	logon := s.Logon()
	logon.Body.SetField(tagHeartBtInt, FIXInt(32))
	logon.Body.SetField(tagResetSeqNumFlag, FIXBoolean(true))

	s.MockApp.On("FromAdmin").Return(nil)
	s.MockApp.On("OnLogon")
	s.MockApp.On("ToAdmin")
	s.fixMsgIn(s.session, logon)

	s.MockApp.AssertExpectations(s.T())

	s.State(inSession{})
	s.Equal(32*time.Second, s.session.HeartBtInt)

	s.LastToAdminMessageSent()
	s.MessageType(string(msgTypeLogon), s.MockApp.lastToAdmin)
	s.FieldEquals(tagHeartBtInt, 32, s.MockApp.lastToAdmin.Body)
	s.FieldEquals(tagResetSeqNumFlag, true, s.MockApp.lastToAdmin.Body)

	s.NextTargetMsgSeqNum(2)
	s.NextSenderMsgSeqNum(2)
}

func (s *LogonStateTestSuite) TestFixMsgInLogonInitiateLogon() {
	s.session.InitiateLogon = true
	s.IncrNextSenderMsgSeqNum()
	s.MessageFactory.seqNum = 1
	s.IncrNextTargetMsgSeqNum()

	logon := s.Logon()
	logon.Body.SetField(tagHeartBtInt, FIXInt(32))

	s.MockApp.On("FromAdmin").Return(nil)
	s.MockApp.On("OnLogon")
	s.fixMsgIn(s.session, logon)

	s.MockApp.AssertExpectations(s.T())
	s.State(inSession{})

	s.NextTargetMsgSeqNum(3)
	s.NextSenderMsgSeqNum(2)
}

func (s *LogonStateTestSuite) TestFixMsgInLogonInitiateLogonExpectResetSeqNum() {
	s.session.InitiateLogon = true
	s.session.sentReset = true
	s.Require().Nil(s.store.IncrNextSenderMsgSeqNum())

	logon := s.Logon()
	logon.Body.SetField(tagHeartBtInt, FIXInt(32))
	logon.Body.SetField(tagResetSeqNumFlag, FIXBoolean(true))

	s.MockApp.On("FromAdmin").Return(nil)
	s.MockApp.On("OnLogon")
	s.fixMsgIn(s.session, logon)

	s.MockApp.AssertExpectations(s.T())
	s.State(inSession{})

	s.NextTargetMsgSeqNum(2)
	s.NextSenderMsgSeqNum(2)
}

func (s *LogonStateTestSuite) TestFixMsgInLogonInitiateLogonRejectedSeqNumNotReset() {
	s.session.InitiateLogon = true
	s.session.sentReset = true
	s.Require().Nil(s.store.IncrNextSenderMsgSeqNum())

	logon := s.Logon()
	logon.Body.SetField(tagHeartBtInt, FIXInt(32))
	logon.Body.SetField(tagResetSeqNumFlag, FIXBoolean(true))

	s.MockApp.On("FromAdmin").Return(RejectLogon{"reject message"})
	s.MockApp.On("OnLogout")
	s.MockApp.On("ToAdmin")
	s.fixMsgIn(s.session, logon)

	s.MockApp.AssertExpectations(s.T())
	s.State(latentState{})

	s.NextTargetMsgSeqNum(2)
	s.NextSenderMsgSeqNum(3)
}

func (s *LogonStateTestSuite) TestFixMsgInLogonInitiateLogonUnExpectedResetSeqNum() {
	s.session.InitiateLogon = true
	s.session.sentReset = false
	s.IncrNextTargetMsgSeqNum()
	s.IncrNextSenderMsgSeqNum()

	logon := s.Logon()
	logon.Body.SetField(tagHeartBtInt, FIXInt(32))
	logon.Body.SetField(tagResetSeqNumFlag, FIXBoolean(true))

	s.MockApp.On("FromAdmin").Return(nil)
	s.MockApp.On("OnLogon")
	s.fixMsgIn(s.session, logon)

	s.MockApp.AssertExpectations(s.T())
	s.State(inSession{})

	s.NextTargetMsgSeqNum(2)
	s.NextSenderMsgSeqNum(1)
}

func (s *LogonStateTestSuite) TestFixMsgInLogonRefreshOnLogon() {
	var tests = []bool{true, false}

	for _, doRefresh := range tests {
		s.SetupTest()
		s.session.RefreshOnLogon = doRefresh

		logon := s.Logon()
		logon.Body.SetField(tagHeartBtInt, FIXInt(32))

		if doRefresh {
			s.MockStore.On("Refresh").Return(nil)
		}
		s.MockApp.On("FromAdmin").Return(nil)
		s.MockApp.On("OnLogon")
		s.MockApp.On("ToAdmin")
		s.fixMsgIn(s.session, logon)

		s.MockStore.AssertExpectations(s.T())
	}
}

func (s *LogonStateTestSuite) TestStop() {
	var tests = []bool{true, false}

	for _, doInitiateLogon := range tests {
		s.SetupTest()
		s.session.InitiateLogon = doInitiateLogon

		if doInitiateLogon {
			s.MockApp.On("OnLogout")
		}

		s.session.Stop(s.session)
		s.MockApp.AssertExpectations(s.T())
		s.Disconnected()
		s.Stopped()
	}
}

func (s *LogonStateTestSuite) TestFixMsgInLogonRejectLogon() {
	s.IncrNextSenderMsgSeqNum()
	s.MessageFactory.seqNum = 1
	s.IncrNextTargetMsgSeqNum()

	logon := s.Logon()
	logon.Body.SetField(tagHeartBtInt, FIXInt(32))

	s.MockApp.On("FromAdmin").Return(RejectLogon{"reject message"})
	s.MockApp.On("ToAdmin")
	s.fixMsgIn(s.session, logon)

	s.MockApp.AssertExpectations(s.T())

	s.State(latentState{})

	s.LastToAdminMessageSent()
	s.MessageType(string(msgTypeLogout), s.MockApp.lastToAdmin)
	s.FieldEquals(tagText, "reject message", s.MockApp.lastToAdmin.Body)

	s.NextTargetMsgSeqNum(3)
	s.NextSenderMsgSeqNum(3)
}

func (s *LogonStateTestSuite) TestFixMsgInLogonSeqNumTooHigh() {
	s.MessageFactory.SetNextSeqNum(6)
	logon := s.Logon()
	logon.Body.SetField(tagHeartBtInt, FIXInt(32))

	s.MockApp.On("FromAdmin").Return(nil)
	s.MockApp.On("OnLogon")
	s.MockApp.On("ToAdmin")
	s.fixMsgIn(s.session, logon)

	s.State(resendState{})
	s.NextTargetMsgSeqNum(1)

	// Session should send logon, and then queues resend request for send.
	s.MockApp.AssertNumberOfCalls(s.T(), "ToAdmin", 2)
	msgBytesSent, ok := s.Receiver.LastMessage()
	s.Require().True(ok)
	sentMessage := NewMessage()
	err := ParseMessage(sentMessage, bytes.NewBuffer(msgBytesSent))
	s.Require().Nil(err)
	s.MessageType(string(msgTypeLogon), sentMessage)

	s.session.sendQueued(true)
	s.MessageType(string(msgTypeResendRequest), s.MockApp.lastToAdmin)
	s.FieldEquals(tagBeginSeqNo, 1, s.MockApp.lastToAdmin.Body)

	s.MockApp.On("FromAdmin").Return(nil)
	s.MessageFactory.SetNextSeqNum(1)
	s.fixMsgIn(s.session, s.SequenceReset(3))
	s.State(resendState{})
	s.NextTargetMsgSeqNum(3)

	s.MessageFactory.SetNextSeqNum(3)
	s.MockApp.On("FromAdmin").Return(nil)
	s.fixMsgIn(s.session, s.SequenceReset(7))
	s.State(inSession{})
	s.NextTargetMsgSeqNum(7)
}

func (s *LogonStateTestSuite) TestFixMsgInLogonSeqNumTooLow() {
	s.IncrNextSenderMsgSeqNum()
	s.IncrNextTargetMsgSeqNum()

	logon := s.Logon()
	logon.Body.SetField(tagHeartBtInt, FIXInt(32))
	logon.Header.SetInt(tagMsgSeqNum, 1)

	s.MockApp.On("FromAdmin").Return(nil)
	s.MockApp.On("ToAdmin")
	s.NextTargetMsgSeqNum(2)
	s.fixMsgIn(s.session, logon)

	s.State(latentState{})
	s.NextTargetMsgSeqNum(2)

	s.MockApp.AssertNumberOfCalls(s.T(), "ToAdmin", 1)
	msgBytesSent, ok := s.Receiver.LastMessage()
	s.Require().True(ok)
	sentMessage := NewMessage()
	err := ParseMessage(sentMessage, bytes.NewBuffer(msgBytesSent))
	s.Require().Nil(err)
	s.MessageType(string(msgTypeLogout), sentMessage)

	s.session.sendQueued(true)
	s.MessageType(string(msgTypeLogout), s.MockApp.lastToAdmin)
	s.FieldEquals(tagText, "MsgSeqNum too low, expecting 2 but received 1", s.MockApp.lastToAdmin.Body)
}

func (s *LogonStateTestSuite) TestStayLoggedInOnReset() {
	s.IncrNextTargetMsgSeqNum()
	s.IncrNextSenderMsgSeqNum()

	logon := s.Logon()
	logon.Body.SetField(tagResetSeqNumFlag, FIXBoolean(true))
	logon.Body.SetField(tagHeartBtInt, FIXInt(30))

	s.MockApp.On("FromAdmin").Return(nil)
	s.MockApp.On("OnLogon")
	s.MockApp.On("ToAdmin")
	s.fixMsgIn(s.session, logon)

	s.MockApp.AssertExpectations(s.T())

	s.State(inSession{})

	s.IncrNextTargetMsgSeqNum()
	s.IncrNextSenderMsgSeqNum()

	s.NextTargetMsgSeqNum(3)
	s.NextSenderMsgSeqNum(3)

	s.fixMsgIn(s.session, logon)

	s.True(s.session.IsConnected())
	s.True(s.session.IsLoggedOn())

	s.NextTargetMsgSeqNum(2)
	s.NextSenderMsgSeqNum(2)
}

// An acceptor refuses a Logon whose HeartBtInt is missing, negative, above
// MaxHeartBtInt or does not parse, before it acts on it: it sends a Logout,
// does not log on, reset the store or advance the target sequence number.
func (s *LogonStateTestSuite) TestFixMsgInLogonInvalidHeartBtInt() {
	tests := []struct {
		name, heartBtInt, text string
	}{
		{"missing", "", "Invalid HeartBtInt (108): Conditionally Required Field Missing (108)"},
		{"negative", "-1", "HeartBtInt (108) must be between 0 and 3600, got -1"},
		{"above maximum", "3601", "HeartBtInt (108) must be between 0 and 3600, got 3601"},
		{"overflows", "99999999999999999999", "Invalid HeartBtInt (108): Incorrect data format for value"},
	}
	for _, test := range tests {
		s.Run(test.name, func() {
			s.SetupTest()
			s.IncrNextSenderMsgSeqNum()
			s.IncrNextTargetMsgSeqNum()
			s.MockApp.On("FromAdmin").Return(nil)
			s.MockApp.On("ToAdmin")

			fields := []string{"98=0", "141=Y"}
			if test.heartBtInt != "" {
				fields = append(fields, "108="+test.heartBtInt)
			}
			logon := s.Logon()
			logon.Header.SetInt(tagMsgSeqNum, 2)
			s.session.Incoming(s.session, fixIn{bytes: withExtraFields(logon, fields...), receiveTime: time.Now()})

			s.State(latentState{})
			s.MockApp.AssertNotCalled(s.T(), "OnLogon")
			s.NextTargetMsgSeqNum(2)
			s.NextSenderMsgSeqNum(3)
			s.MessageType(string(msgTypeLogout), s.MockApp.lastToAdmin)
			s.FieldEquals(tagText, test.text, s.MockApp.lastToAdmin.Body)
		})
	}
}

// HeartBtInt 0 turns heartbeats off, rather than sending them continuously.
func (s *LogonStateTestSuite) TestFixMsgInLogonHeartBtIntZero() {
	var heartbeats, peerTimeouts atomic.Int32
	s.session.stateTimer = internal.NewEventTimer(func() { heartbeats.Add(1) })
	s.session.peerTimer = internal.NewEventTimer(func() { peerTimeouts.Add(1) })
	defer s.session.stateTimer.Stop()
	defer s.session.peerTimer.Stop()

	s.IncrNextSenderMsgSeqNum()
	s.MessageFactory.seqNum = 1
	s.IncrNextTargetMsgSeqNum()
	logon := s.Logon()
	logon.Body.SetField(tagHeartBtInt, FIXInt(0))

	s.MockApp.On("FromAdmin").Return(nil)
	s.MockApp.On("OnLogon")
	s.MockApp.On("ToAdmin")
	s.fixMsgIn(s.session, logon)

	s.State(inSession{})
	s.Zero(s.session.HeartBtInt)
	s.LastToAdminMessageSent()
	s.FieldEquals(tagHeartBtInt, 0, s.MockApp.lastToAdmin.Body)

	time.Sleep(50 * time.Millisecond)
	s.Zero(heartbeats.Load())
	s.Zero(peerTimeouts.Load())
}

func (s *LogonStateTestSuite) TestFixMsgInLogonMaxHeartBtInt() {
	s.session.MaxHeartBtInt = 60 * time.Second
	s.IncrNextSenderMsgSeqNum()
	s.MessageFactory.seqNum = 1
	s.IncrNextTargetMsgSeqNum()
	logon := s.Logon()
	logon.Body.SetField(tagHeartBtInt, FIXInt(60))

	s.MockApp.On("FromAdmin").Return(nil)
	s.MockApp.On("OnLogon")
	s.MockApp.On("ToAdmin")
	s.fixMsgIn(s.session, logon)

	s.State(inSession{})
	s.Equal(60*time.Second, s.session.HeartBtInt)
}
