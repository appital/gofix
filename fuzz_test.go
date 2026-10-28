package gofix

import (
	"bytes"
	"io"
	"math"
	"regexp"
	"strconv"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"
)

var fuzzSeedFrames = []string{
	"8=FIX.4.4\x019=49\x0135=0\x0149=TW\x0156=ISLD\x0134=1\x0152=20260101-00:00:00\x0110=239\x01",
	"8=FIX.4.4\x0110=000\x01",
	"8=FIX.4.4\x019=5\x0135=0\x019=\x0110=000\x01",
	"8=FIX.4.4\x019=12\x0135=0\x01212=50\x0110=000\x01",
	"8=FIX.4.4\x019=9223372036854775807\x0135=0\x0110=000\x01",
	"garbage8=FIX.4.4\x019=5\x0135=0\x0110=161\x018=FIX.4.4\x019=",
}

var fixIntPattern = regexp.MustCompile(`^-?[0-9]+$`)

// FuzzAtoi checks atoi against strconv.Atoi, restricted to the FIX int
// syntax of an optional minus sign followed by digits.
func FuzzAtoi(f *testing.F) {
	for _, s := range []string{
		"", "-", "0", "-0", "15", "+15", "1-5", "0000000000000000000000015",
		strconv.Itoa(math.MaxInt), strconv.Itoa(-math.MaxInt), strconv.Itoa(math.MinInt),
		"9223372036854775808", "99999999999999999999",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		got, err := atoi([]byte(s))
		if !fixIntPattern.MatchString(s) {
			require.Error(t, err, "%q", s)
			return
		}

		want, wantErr := strconv.Atoi(s)
		switch {
		case want == math.MinInt && wantErr == nil:
			// atoi rejects the one value whose magnitude does not fit in an int.
			if err == nil {
				require.Equal(t, want, got, "%q", s)
			}
		case wantErr != nil:
			require.Error(t, err, "%q", s)
		default:
			require.NoError(t, err, "%q", s)
			require.Equal(t, want, got, "%q", s)
		}
	})
}

// chunkReader returns at most size bytes from each Read, and io.EOF together
// with the last of them.
type chunkReader struct {
	data []byte
	size int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	n := copy(p, r.data[:min(len(r.data), r.size)])
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, io.EOF
	}
	return n, nil
}

// readFrames reads frames from r until ReadMessage returns an error.
func readFrames(r io.Reader) (frames []string, err error) {
	p := newParser(r)
	for {
		msg, err := p.ReadMessage()
		if err != nil {
			return frames, err
		}
		frames = append(frames, msg.String())
	}
}

var frameTrailer = regexp.MustCompile("\x0110=[^\x01]*\x01$")

// FuzzReadMessageChunked checks that the stream parser frames the same input
// identically however the reads are split, and that each returned frame is a
// run of the input ending with a CheckSum field. Malformed messages may be
// skipped, so frames need not be adjacent.
func FuzzReadMessageChunked(f *testing.F) {
	for _, s := range fuzzSeedFrames {
		f.Add([]byte(s), uint16(1))
		f.Add([]byte(s+s), uint16(7))
	}

	f.Fuzz(func(t *testing.T, data []byte, size uint16) {
		frames, err := readFrames(bytes.NewReader(data))

		oneByte, oneByteErr := readFrames(iotest.OneByteReader(bytes.NewReader(data)))
		require.Equal(t, frames, oneByte)
		require.EqualError(t, oneByteErr, err.Error())

		chunked, chunkedErr := readFrames(&chunkReader{data: data, size: int(size) + 1})
		require.Equal(t, frames, chunked)
		require.EqualError(t, chunkedErr, err.Error())

		rest := data
		for _, frame := range frames {
			start := bytes.Index(rest, []byte(frame))
			require.GreaterOrEqual(t, start, 0, "frame %q is not in the input", frame)
			require.Regexp(t, frameTrailer, frame)
			rest = rest[start+len(frame):]
		}
	})
}

// FuzzBuildParseRoundTrip builds a message from fuzzed body fields and XmlData,
// parses it back, and checks the fields survive and the message rebuilds to
// the same bytes. body is read as SOH-separated tag=value pairs; XmlData may
// contain SOH.
func FuzzBuildParseRoundTrip(f *testing.F) {
	f.Add("D", []byte("11=ID1\x0155=EUR/USD\x0158=\x0144=1.5"), []byte(""))
	f.Add("0", []byte("58=a=b"), []byte("<x>\x0110=000\x01</x>"))
	f.Add("A", []byte("98=0\x01108=30"), []byte("="))

	f.Fuzz(func(t *testing.T, msgType string, body, xml []byte) {
		if msgType == "" || bytes.IndexByte([]byte(msgType), '\001') != -1 {
			t.Skip()
		}

		msg := NewMessage()
		msg.Header.SetString(tagBeginString, "FIX.4.4")
		msg.Header.SetString(tagMsgType, msgType)
		if len(xml) > 0 {
			msg.Header.SetInt(tagXMLDataLen, len(xml))
			msg.Header.SetBytes(tagXMLData, xml)
		}

		want := map[Tag][]byte{}
		for field := range bytes.SplitSeq(body, []byte{'\001'}) {
			tagBytes, value, ok := bytes.Cut(field, []byte("="))
			if !ok {
				continue
			}
			tag, err := atoi(tagBytes)
			if err != nil || tag <= 0 || Tag(tag).IsHeader() || Tag(tag).IsTrailer() {
				continue
			}
			msg.Body.SetBytes(Tag(tag), value)
			want[Tag(tag)] = value
		}

		raw := msg.build()

		parsed := NewMessage()
		require.NoError(t, ParseMessage(parsed, bytes.NewBuffer(bytes.Clone(raw))), "%q", raw)

		gotType, err := parsed.Header.GetBytes(tagMsgType)
		require.Nil(t, err)
		require.Equal(t, msgType, string(gotType))
		if len(xml) > 0 {
			gotXML, err := parsed.Header.GetBytes(tagXMLData)
			require.Nil(t, err)
			require.Equal(t, xml, gotXML)
		}
		require.Len(t, parsed.Body.Tags(), len(want))
		for tag, value := range want {
			got, err := parsed.Body.GetBytes(tag)
			require.Nil(t, err, "tag %d", tag)
			require.Equal(t, value, got, "tag %d", tag)
		}

		require.Equal(t, raw, parsed.build())
	})
}

// FuzzFieldValueRead reads arbitrary bytes as each field value type. A value
// that reads must write back to bytes that read as the same value.
func FuzzFieldValueRead(f *testing.F) {
	for _, s := range []string{
		"", "-", "Y", "N", "15", "-1.50", "+1", "1e5", ".5", "-0", "1.", "123456789012345678901234567890.123",
		"20260101-12:34:56", "20260101-12:34:56.789", "20260101-12:34:56.789012", "20260101-12:34:56.789012345",
		"20261301-12:34:56", "20260101-24:00:00",
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		var b, b2 FIXBoolean
		if b.Read(data) == nil {
			require.NoError(t, b2.Read(b.Write()))
			require.Equal(t, b, b2)
		}

		var i, i2 FIXInt
		if i.Read(data) == nil {
			require.NoError(t, i2.Read(i.Write()))
			require.Equal(t, i, i2)
		}

		var fl, fl2 FIXFloat
		if fl.Read(data) == nil {
			require.NoError(t, fl2.Read(fl.Write()), "%q", data)
			require.Equal(t, fl, fl2, "%q", data)
		}

		var d, d2 FIXDecimal
		if d.Read(data) == nil {
			d.Scale = max(0, -d.Exponent())
			require.NoError(t, d2.Read(d.Write()), "%q", data)
			require.True(t, d.Equal(d2.Decimal), "%q: %s != %s", data, d.Decimal, d2.Decimal)
		}

		var u, u2 FIXUDecimal
		if u.Read(data) == nil {
			u.Scale = u.PrecUint()
			require.NoError(t, u2.Read(u.Write()), "%q", data)
			require.True(t, u.Equal(u2.Decimal), "%q: %s != %s", data, u.Decimal, u2.Decimal)
		}

		var ts, ts2 FIXUTCTimestamp
		if ts.Read(data) == nil {
			require.NoError(t, ts2.Read(ts.Write()), "%q", data)
			require.True(t, ts.Equal(ts2.Time), "%q: %v != %v", data, ts.Time, ts2.Time)
			require.Equal(t, ts.Precision, ts2.Precision)
		}
	})
}
