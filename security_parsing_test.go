package gofix

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParserRejectsMessageOverLimit(t *testing.T) {
	raw := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=1", "52=20260101-00:00:00")
	parser := newParserWithLimit(strings.NewReader(raw), len(raw)-1)

	msg, err := parser.ReadMessage()
	require.Nil(t, msg)
	require.ErrorIs(t, err, errMessageTooLarge)
}

func TestParserLowerLimitWithBufferedMessages(t *testing.T) {
	var stream strings.Builder
	var messages []string
	for i := 1; i <= 10; i++ {
		msg := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34="+strconv.Itoa(i), "52=20260101-00:00:00")
		messages = append(messages, msg)
		stream.WriteString(msg)
	}

	// One read takes in every message, as when a Logon arrives with others
	// behind it before the acceptor applies the session's limit.
	parser := newParser(strings.NewReader(stream.String()))
	msg, err := parser.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, messages[0], msg.String())

	parser.maxMessageSize = 2 * len(messages[0])
	require.Greater(t, len(parser.buffer), parser.maxMessageSize)

	for _, want := range messages[1:] {
		msg, err = parser.ReadMessage()
		require.NoError(t, err)
		require.Equal(t, want, msg.String())
	}
}
