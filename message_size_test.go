package gofix

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// endlessReader returns header, then filler bytes that never complete the
// message, counting everything it hands out.
type endlessReader struct {
	header string
	sent   int
}

func (r *endlessReader) Read(p []byte) (int, error) {
	if r.sent < len(r.header) {
		n := copy(p, r.header[r.sent:])
		r.sent += n
		return n, nil
	}
	if r.sent > 64<<20 {
		return 0, io.ErrUnexpectedEOF
	}
	for i := range p {
		p[i] = 'a'
	}
	r.sent += len(p)
	return len(p), nil
}

func TestParserBoundsUnterminatedMessage(t *testing.T) {
	const limit = 64 << 10

	for name, header := range map[string]string{
		// The claimed BodyLength alone is over the limit.
		"BodyLength over limit": "8=FIX.4.4\x019=2000000000\x0135=A\x01",
		// The BodyLength is small, but the message never ends.
		"no end": "8=FIX.4.4\x019=5\x0135=A\x01",
	} {
		t.Run(name, func(t *testing.T) {
			reader := &endlessReader{header: header}
			parser := newParserWithLimit(reader, limit)

			msg, err := parser.ReadMessage()
			require.Nil(t, msg)
			require.ErrorIs(t, err, errMessageTooLarge)
			require.LessOrEqual(t, reader.sent, limit)
			require.LessOrEqual(t, cap(parser.bigBuffer), 2*limit)
		})
	}
}
