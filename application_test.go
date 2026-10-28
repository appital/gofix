package gofix

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

// fromAppOnly implements Application and nothing else.
type fromAppOnly struct {
	received []string
}

var _ Application = (*fromAppOnly)(nil)

func (a *fromAppOnly) FromApp(msg *Message, _ SessionID) MessageRejectError {
	msgType, err := msg.Header.GetString(tagMsgType)
	if err != nil {
		return err
	}
	a.received = append(a.received, msgType)
	return nil
}

func TestSetApplicationFindsOptionalInterfaces(t *testing.T) {
	app := &MockApp{}
	s := &session{}
	s.setApplication(app)

	assert.Same(t, app, s.application)
	assert.Same(t, app, s.sessionHandler)
	assert.Same(t, app, s.interceptor)
}

func TestSetApplicationDefaultsMissingInterfaces(t *testing.T) {
	app := &fromAppOnly{}
	s := &session{}
	s.setApplication(app)

	assert.Same(t, app, s.application)
	assert.Equal(t, splitSessionHandler{noopSessionHandler{}, noopSessionHandler{}, noopSessionHandler{}}, s.sessionHandler)
	assert.Equal(t, splitMessageInterceptor{noopMessageInterceptor{}, noopMessageInterceptor{}, noopMessageInterceptor{}}, s.interceptor)
}

func TestSetApplicationFindsEachCallback(t *testing.T) {
	app := &logonChecker{}
	s := &session{}
	s.setApplication(app)

	assert.Equal(t, splitMessageInterceptor{noopMessageInterceptor{}, app, noopMessageInterceptor{}}, s.interceptor)
}

// wrongFromAdmin has a FromAdmin that returns error rather than
// MessageRejectError, so it does not implement FromAdminInterceptor.
type wrongFromAdmin struct{ fromAppOnly }

func (*wrongFromAdmin) FromAdmin(*Message, SessionID) error { return nil }

// valueApp has OnLogon on its pointer type only.
type valueApp struct{}

func (valueApp) FromApp(*Message, SessionID) MessageRejectError { return nil }
func (*valueApp) OnLogon(SessionID)                             {}

func TestCheckApplication(t *testing.T) {
	for _, app := range []Application{nil, &MockApp{}, &fromAppOnly{}, &logonChecker{}, NewMessageRouter(), &valueApp{}} {
		assert.NoError(t, checkApplication(app), "%T", app)
	}

	assert.ErrorContains(t, checkApplication(&wrongFromAdmin{}), "FromAdmin method that does not match gofix.FromAdminInterceptor")
	assert.ErrorContains(t, checkApplication(valueApp{}), "OnLogon method with a pointer receiver")

	_, err := NewAcceptor(&wrongFromAdmin{}, nil, nil, nil)
	assert.ErrorContains(t, err, "FromAdmin")
	_, err = NewInitiator(&wrongFromAdmin{}, nil, nil, nil)
	assert.ErrorContains(t, err, "FromAdmin")
}

func TestMessageRouterIsApplication(t *testing.T) {
	router := NewMessageRouter()
	var called bool
	router.AddRoute(BeginStringFIX42, "D", func(*Message, SessionID) MessageRejectError {
		called = true
		return nil
	})

	var app Application = router
	msg := NewMessage()
	msg.Header.SetField(tagBeginString, FIXString(BeginStringFIX42))
	msg.Header.SetField(tagMsgType, FIXString("D"))

	assert.Nil(t, app.FromApp(msg, SessionID{}))
	assert.True(t, called)
}

type FromAppOnlyTestSuite struct {
	SessionSuiteRig
	app *fromAppOnly
}

func TestFromAppOnlyTestSuite(t *testing.T) {
	suite.Run(t, new(FromAppOnlyTestSuite))
}

func (s *FromAppOnlyTestSuite) SetupTest() {
	s.Init()
	s.app = &fromAppOnly{}
	s.session.setApplication(s.app)
}

// Without a SessionHandler or MessageInterceptor, a session behaves as if both
// were implemented with empty methods: the Logon is accepted and answered,
// application messages reach FromApp, and sends go ahead.
func (s *FromAppOnlyTestSuite) TestLogonReceiveAndSend() {
	s.session.State = logonState{}
	s.IncrNextSenderMsgSeqNum()
	s.MessageFactory.seqNum = 1
	s.IncrNextTargetMsgSeqNum()

	logon := s.Logon()
	logon.Body.SetField(tagHeartBtInt, FIXInt(32))
	s.fixMsgIn(s.session, logon)

	s.State(inSession{})
	s.Equal(32*time.Second, s.session.HeartBtInt)
	s.NextTargetMsgSeqNum(3)
	s.NextSenderMsgSeqNum(3)

	s.fixMsgIn(s.session, s.NewOrderSingle())
	s.Equal([]string{"D"}, s.app.received)
	s.NextTargetMsgSeqNum(4)

	s.Require().Nil(s.session.send(s.NewOrderSingle()))
	s.NextSenderMsgSeqNum(4)
}

// logonChecker implements FromAdmin, to refuse every Logon, and no other
// optional callback.
type logonChecker struct {
	fromAppOnly
	logons int
}

var _ FromAdminInterceptor = (*logonChecker)(nil)

func (a *logonChecker) FromAdmin(msg *Message, _ SessionID) MessageRejectError {
	if msg.IsMsgTypeOf(string(msgTypeLogon)) {
		a.logons++
		return RejectLogon{"bad password"}
	}
	return nil
}

type LogonCheckerTestSuite struct {
	SessionSuiteRig
	app *logonChecker
}

func TestLogonCheckerTestSuite(t *testing.T) {
	suite.Run(t, new(LogonCheckerTestSuite))
}

func (s *LogonCheckerTestSuite) SetupTest() {
	s.Init()
	s.app = &logonChecker{}
	s.session.setApplication(s.app)
}

// An application that implements FromAdmin but not the rest of
// MessageInterceptor still has FromAdmin called, and can refuse a Logon.
func (s *LogonCheckerTestSuite) TestFromAdminRefusesLogon() {
	s.session.State = logonState{}
	s.IncrNextSenderMsgSeqNum()
	s.MessageFactory.seqNum = 1
	s.IncrNextTargetMsgSeqNum()

	logon := s.Logon()
	logon.Body.SetField(tagHeartBtInt, FIXInt(32))
	s.fixMsgIn(s.session, logon)

	s.Equal(1, s.app.logons)
	s.State(latentState{})
}
