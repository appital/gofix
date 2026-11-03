package gofix

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/appital/gofix/internal"
)

// SessionPanicTestSuite checks that a panic while the session handles an event
// disconnects that session instead of ending the process.
type SessionPanicTestSuite struct {
	SessionSuiteRig
	log *textLog
}

func TestSessionPanicTestSuite(t *testing.T) {
	suite.Run(t, new(SessionPanicTestSuite))
}

func (s *SessionPanicTestSuite) SetupTest() {
	s.Init()
	s.log = &textLog{}
	s.session.log = s.log
	s.session.State = inSession{}
	s.session.admin = make(chan any, 1)
	s.session.messageIn = make(chan fixIn, 1)
	s.session.peerTimer = internal.NewEventTimer(func() {})
	s.session.stateTimer = internal.NewEventTimer(func() {})
}

func (s *SessionPanicTestSuite) TearDownTest() {
	s.session.peerTimer.Stop()
	s.session.stateTimer.Stop()
}

func panics(mock.Arguments) { panic("application bug") }

func (s *SessionPanicTestSuite) receive(msg *Message) {
	in := make(chan fixIn, 1)
	in <- fixIn{bytes: bytes.NewBuffer(msg.build()), receiveTime: time.Now()}
	s.session.messageIn = in
	s.NotPanics(func() { s.session.runOnce(nil) })
}

func (s *SessionPanicTestSuite) TestFromAppPanicDisconnects() {
	s.MockApp.On("FromApp").Run(panics).Return(nil)
	s.MockApp.On("OnLogout")

	s.receive(s.NewOrderSingle())

	s.MockApp.AssertExpectations(s.T())
	s.State(latentState{})
	s.Disconnected()
	s.Nil(s.session.messageIn)
	s.NotStopped()
	s.Contains(s.log.String(), "Session panic, disconnecting: application bug")
	s.Contains(s.log.String(), "runOnce")
}

func (s *SessionPanicTestSuite) TestPanicWhileDisconnectingStillDisconnects() {
	s.MockApp.On("FromApp").Run(panics).Return(nil)
	s.MockApp.On("OnLogout").Run(panics)

	s.receive(s.NewOrderSingle())

	s.State(latentState{})
	s.Disconnected()
	s.Nil(s.session.messageIn)
}

func (s *SessionPanicTestSuite) TestPanicDuringStopStops() {
	s.MockApp.On("ToAdmin").Run(panics)
	s.MockApp.On("OnLogout")

	s.session.admin <- stopReq{}
	s.NotPanics(func() { s.session.runOnce(nil) })

	s.State(latentState{})
	s.Disconnected()
	s.Stopped()
}

func (s *SessionPanicTestSuite) TestPanicInLogStillDisconnects() {
	s.session.log = panickingLog{}
	s.MockApp.On("OnLogout")

	s.receive(s.NewOrderSingle())

	s.MockApp.AssertExpectations(s.T())
	s.State(latentState{})
	s.Disconnected()
	s.Nil(s.session.messageIn)
}

// panickingLog panics on every call.
type panickingLog struct{}

func (panickingLog) OnIncoming([]byte)       { panic("log bug") }
func (panickingLog) OnOutgoing([]byte)       { panic("log bug") }
func (panickingLog) OnEvent(string)          { panic("log bug") }
func (panickingLog) OnEventf(string, ...any) { panic("log bug") }
