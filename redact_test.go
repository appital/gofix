package gofix

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/appital/gofix/config"
	"github.com/appital/gofix/internal"
)

const testSecret = "s3cret"

func TestRedact(t *testing.T) {
	r := newRedactor(defaultRedactedTags)

	tests := []struct {
		name, msg, expected string
	}{
		{
			"password",
			wireMessage("8=FIX.4.4", "35=A", "49=TW", "56=ISLD", "34=1", "52=20260101-00:00:00", "98=0", "108=30", "553=user", "554="+testSecret),
			wireMessage("8=FIX.4.4", "35=A", "49=TW", "56=ISLD", "34=1", "52=20260101-00:00:00", "98=0", "108=30", "553=user", "554=***"),
		},
		{
			"password and new password",
			wireMessage("8=FIX.4.4", "35=A", "554="+testSecret, "925="+testSecret+"2", "108=30"),
			wireMessage("8=FIX.4.4", "35=A", "554=***", "925=***", "108=30"),
		},
		{
			"data field containing SOH, and its length",
			wireMessage("8=FIX.5.0SP2", "35=A", "1400=1", "1401=9", "1402=ab\x01554=cd", "108=30"),
			wireMessage("8=FIX.5.0SP2", "35=A", "1400=1", "1401=3", "1402=***", "108=30"),
		},
		{
			"empty value",
			wireMessage("8=FIX.4.4", "35=A", "554=", "108=30"),
			wireMessage("8=FIX.4.4", "35=A", "554=***", "108=30"),
		},
		{
			"event around a message",
			"Invalid Message: 8=FIX.4.4\x019=5\x01554=" + testSecret + "\x0110=000\x01, bad",
			"Invalid Message: 8=FIX.4.4\x019=5\x01554=***\x0110=000\x01, bad",
		},
		{
			"text without a field before a message",
			"no field\x01554=" + testSecret + "\x01",
			"no field\x01554=***\x01",
		},
		{
			"unterminated field",
			"8=FIX.4.4\x01554=" + testSecret,
			"8=FIX.4.4\x01554=***",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := []byte(test.msg)
			redacted := r.redact(original)
			assert.Equal(t, test.expected, string(redacted))
			assert.Equal(t, test.msg, string(original), "redact must not modify its input")
		})
	}

	t.Run("still parses", func(t *testing.T) {
		raw := wireMessage("8=FIX.5.0SP2", "35=A", "49=TW", "56=ISLD", "34=1", "52=20260101-00:00:00", "98=0", "108=30", "1401=4", "1402=a\x01bc", "554="+testSecret)
		msg := NewMessage()
		require.NoError(t, ParseMessage(msg, bytes.NewBuffer(r.redact([]byte(raw)))))
		password, err := msg.Body.GetString(554)
		require.NoError(t, err)
		assert.Equal(t, redactedValue, password)
	})
}

func TestRedactLargeTags(t *testing.T) {
	r := newRedactor([]Tag{96, 5000, 20000})
	raw := wireMessage("8=FIX.4.4", "35=A", "95=2", "96=ab", "5000=c", "20000=d", "20001=e", "500=f")
	expected := wireMessage("8=FIX.4.4", "35=A", "95=3", "96=***", "5000=***", "20000=***", "20001=e", "500=f")
	assert.Equal(t, expected, string(r.redact([]byte(raw))))

	other := []byte(wireMessage("8=FIX.4.4", "35=A", "20001=e", "4999=f"))
	assert.Same(t, &other[0], &r.redact(other)[0])
}

func TestRedactLeavesOtherMessagesAlone(t *testing.T) {
	r := newRedactor(defaultRedactedTags)

	for _, msg := range []string{
		wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=1", "52=20260101-00:00:00"),
		// A redacted tag inside a data field's value is not a field.
		wireMessage("8=FIX.4.4", "35=0", "95=8", "96=a\x01554=bc", "34=1"),
		// Tags that only start with, or end with, a redacted tag.
		wireMessage("8=FIX.4.4", "35=0", "5540=a", "1554=b", "58=554=c"),
		"",
		"no fields",
	} {
		original := []byte(msg)
		redacted := r.redact(original)
		assert.Equal(t, msg, string(redacted))
		if len(original) > 0 {
			assert.Same(t, &original[0], &redacted[0], "redact copied %q", msg)
		}
	}

	var none *redactor
	msg := []byte("8=FIX.4.4\x01554=" + testSecret + "\x01")
	assert.Same(t, &msg[0], &none.redact(msg)[0])
	assert.Nil(t, newRedactor(nil))
}

// textLog keeps everything logged to it as text.
type textLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *textLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, s)
}

func (l *textLog) OnIncoming(msg []byte)               { l.add(string(msg)) }
func (l *textLog) OnOutgoing(msg []byte)               { l.add(string(msg)) }
func (l *textLog) OnEvent(event string)                { l.add(event) }
func (l *textLog) OnEventf(format string, args ...any) { l.add(fmt.Sprintf(format, args...)) }

func (l *textLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.entries, "\n")
}

type textLogFactory struct{ log *textLog }

func (f textLogFactory) Create() (Log, error)                    { return f.log, nil }
func (f textLogFactory) CreateSessionLog(SessionID) (Log, error) { return f.log, nil }

func TestRedactingLog(t *testing.T) {
	raw := wireMessage("8=FIX.4.4", "35=A", "554="+testSecret)
	msg := NewMessage()
	require.NoError(t, ParseMessage(msg, bytes.NewBufferString(raw)))

	inner := &textLog{}
	log := newRedactingLog(inner, newRedactor(defaultRedactedTags))
	log.OnIncoming([]byte(raw))
	log.OnOutgoing([]byte(raw))
	log.OnEvent("Event " + raw)
	log.OnEventf("%s %s %q %v %v %v %d", raw, []byte(raw), bytes.NewBufferString(raw), msg, errors.New(raw), "plain", 7)

	assert.NotContains(t, inner.String(), testSecret)
	assert.Contains(t, inner.entries[3], " plain 7")
	assert.Equal(t, 8, strings.Count(inner.String(), "554=***"))

	assert.Equal(t, nullLog{}, newRedactingLog(nullLog{}, newRedactor(defaultRedactedTags)))
	assert.Same(t, inner, newRedactingLog(inner, nil))
}

func TestRedactingLogDoesNotModifyArgs(t *testing.T) {
	raw := "8=FIX.4.4\x01554=" + testSecret + "\x01"
	args := []any{raw, 1}
	newRedactingLog(&textLog{}, newRedactor(defaultRedactedTags)).OnEventf("%s %d", args...)
	assert.Equal(t, raw, args[0])
}

// An inbound Logon's credentials are not logged.
func (s *LogonStateTestSuite) TestIncomingCredentialsRedacted() {
	log := &textLog{}
	s.session.log = newRedactingLog(log, newRedactor(defaultRedactedTags))
	s.session.peerTimer = internal.NewEventTimer(func() {})
	defer s.session.peerTimer.Stop()

	s.MockApp.On("FromAdmin").Return(RejectLogon{"bad password"})
	s.MockApp.On("ToAdmin")
	s.MockApp.On("OnLogout")
	logon := s.Logon()
	logon.Body.SetField(554, FIXString(testSecret))
	s.session.Incoming(s.session, fixIn{bytes: bytes.NewBuffer(logon.build()), receiveTime: time.Now()})

	// A message other than Logon is logged as an event in the logon state.
	order := s.NewOrderSingle()
	order.Body.SetField(554, FIXString(testSecret))
	s.session.State = logonState{}
	s.session.Incoming(s.session, fixIn{bytes: bytes.NewBuffer(order.build()), receiveTime: time.Now()})

	s.Contains(log.String(), "554=***")
	s.Contains(log.String(), "Invalid Session State")
	s.NotContains(log.String(), testSecret)
}

// The credentials ToAdmin adds to an outbound Logon are sent, but neither
// logged nor stored.
func (s *SessionSuite) TestOutgoingLogonCredentialsRedacted() {
	log := &textLog{}
	s.session.redactor = newRedactor(defaultRedactedTags)
	s.session.log = newRedactingLog(log, s.session.redactor)
	s.session.HeartBtInt = 30 * time.Second
	s.session.InitiateLogon = true
	s.MockApp.On("ToAdmin")
	s.MockApp.decorateToAdmin = func(msg *Message) {
		msg.Body.SetField(554, FIXString(testSecret))
	}

	s.session.onAdmin(connect{messageOut: s.Receiver.sendChannel})
	s.State(logonState{})

	sent, ok := s.Receiver.LastMessage()
	s.Require().True(ok)
	s.Contains(string(sent), "554="+testSecret)
	s.NotContains(log.String(), testSecret)

	stored, err := s.session.store.GetMessages(1, 1)
	s.Require().NoError(err)
	s.Require().Len(stored, 1)
	s.Contains(string(stored[0]), "554=***")
	s.NotContains(string(stored[0]), testSecret)
	s.NoError(ParseMessage(NewMessage(), bytes.NewBuffer(stored[0])), "stored Logon must still parse for a resend")
}

// Application messages are stored as they are, because they are resent.
func (s *SessionSuite) TestOutgoingAppMessageStoredWithCredentials() {
	s.session.redactor = newRedactor(defaultRedactedTags)
	s.MockApp.On("ToApp").Return(nil)

	msg := s.NewOrderSingle()
	msg.Body.SetField(554, FIXString(testSecret))
	_, err := s.session.prepMessageForSend(msg, nil)
	s.Require().NoError(err)

	stored, err := s.session.store.GetMessages(1, 1)
	s.Require().NoError(err)
	s.Require().Len(stored, 1)
	s.Contains(string(stored[0]), "554="+testSecret)
}

// A Logon for an unknown session is logged to the acceptor's global log
// before anyone is authenticated, so its credentials must be redacted there.
func TestAcceptorGlobalLogRedactsCredentials(t *testing.T) {
	sessionSettings := NewSessionSettings()
	sessionSettings.Set(config.BeginString, BeginStringFIX42)
	sessionSettings.Set(config.SenderCompID, "SERVER")
	sessionSettings.Set(config.TargetCompID, "CLIENT")

	settings := NewSettings()
	settings.GlobalSettings().Set(config.SocketAcceptPort, "5004")
	settings.GlobalSettings().Set(config.HeartBtInt, "30")
	_, err := settings.AddSession(sessionSettings)
	require.NoError(t, err)

	log := &textLog{}
	acceptor, err := NewAcceptor(&MockApp{}, NewMemoryStoreFactory(), settings, textLogFactory{log}, WithRegistry(NewRegistry()))
	require.NoError(t, err)
	require.NoError(t, acceptor.Start())
	t.Cleanup(acceptor.Stop)

	conn, err := net.Dial("tcp", "127.0.0.1:5004")
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	_, err = conn.Write([]byte(wireMessage("8=FIX.4.2", "35=A", "49=CLIENTTYPO", "56=SERVER", "34=1",
		"52="+time.Now().UTC().Format("20060102-15:04:05"), "98=0", "108=30", "554="+testSecret)))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return strings.Contains(log.String(), "not found for incoming message")
	}, 5*time.Second, 10*time.Millisecond)
	assert.Contains(t, log.String(), "554=***")
	assert.NotContains(t, log.String(), testSecret)
}

func (s *SessionFactorySuite) TestRedactedTags() {
	s.LogFactory = textLogFactory{&textLog{}}

	session, err := s.newSession(s.SessionID, s.MessageStoreFactory, s.SessionSettings, s.LogFactory, s.App)
	s.Require().NoError(err)
	s.Equal(defaultRedactedTags, session.redactor.tags)
	s.IsType(redactingLog{}, session.log)

	s.SessionSettings.Set(config.RedactedTags, " 553, 554 ,96")
	session, err = s.newSession(s.SessionID, s.MessageStoreFactory, s.SessionSettings, s.LogFactory, s.App)
	s.Require().NoError(err)
	s.Equal([]Tag{553, 554, 96}, session.redactor.tags)

	s.SessionSettings.Set(config.RedactedTags, "")
	session, err = s.newSession(s.SessionID, s.MessageStoreFactory, s.SessionSettings, s.LogFactory, s.App)
	s.Require().NoError(err)
	s.Nil(session.redactor)
	s.IsType(&textLog{}, session.log)
}

func (s *SessionFactorySuite) TestInvalidRedactedTags() {
	for _, setting := range []string{"a", "554,x", "0", "-1", "1.5"} {
		s.SetupTest()
		s.SessionSettings.Set(config.RedactedTags, setting)
		_, err := s.newSession(s.SessionID, s.MessageStoreFactory, s.SessionSettings, s.LogFactory, s.App)
		s.Error(err, "RedactedTags=%q", setting)
	}
}
