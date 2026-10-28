package gofix

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/appital/gofix/datadictionary"
)

func replaceBodyLength(raw, value string) string {
	index := strings.Index(raw, "\x019=")
	if index < 0 {
		return raw
	}
	end := strings.IndexByte(raw[index+3:], '\x01') + index + 3
	return raw[:index] + "\x019=" + value + raw[end:]
}

func TestParseMessageRejectsBadCheckSum(t *testing.T) {
	raw := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=1", "52=20260101-00:00:00")

	bad := []byte(raw)
	index := bytes.LastIndex(bad, []byte("\x0110="))
	require.GreaterOrEqual(t, index, 0)
	copy(bad[index+4:index+7], "999")

	err := ParseMessage(NewMessage(), bytes.NewBuffer(bad))
	require.ErrorContains(t, err, "CheckSum")
}

func TestParseMessageRejectsDuplicateSingletonTags(t *testing.T) {
	tests := []struct {
		name   string
		fields []string
	}{
		{"BodyLength", []string{"35=0", "9=5"}},
		{"MsgType", []string{"35=0", "35=D"}},
		{"MsgSeqNum", []string{"35=0", "34=1", "34=2"}},
		{"SenderCompID", []string{"35=0", "49=A", "49=B"}},
		{"SendingTime", []string{"35=0", "52=20260101-00:00:00", "52=20260102-00:00:00"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := wireMessage("8=FIX.4.4", test.fields...)

			err := ParseMessage(NewMessage(), bytes.NewBufferString(raw))
			require.ErrorContains(t, err, "duplicate tag")
		})
	}
}

func TestReadMessageRejectsBodyLengthNotEndingAtCheckSum(t *testing.T) {
	first := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=1", "52=20260101-00:00:00")
	second := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=2", "52=20260101-00:00:01")

	index := strings.Index(first, "\x019=")
	end := strings.IndexByte(first[index+3:], '\x01') + index + 3
	length, err := strconv.Atoi(first[index+3 : end])
	require.NoError(t, err)

	// One byte past the real trailer is enough to show the boundary is exact.
	bad := replaceBodyLength(first, strconv.Itoa(length+1)) + second
	parser := newParser(strings.NewReader(bad))

	// The malformed frame is ignored and the parser resynchronises on the
	// next message rather than returning the bad one.
	msg, err := parser.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, second, msg.String())
}

func TestReadMessageWithoutResyncRejectsBadFrame(t *testing.T) {
	first := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=1", "52=20260101-00:00:00")
	second := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=2", "52=20260101-00:00:01")

	index := strings.Index(first, "\x019=")
	end := strings.IndexByte(first[index+3:], '\x01') + index + 3
	length, err := strconv.Atoi(first[index+3 : end])
	require.NoError(t, err)

	bad := replaceBodyLength(first, strconv.Itoa(length+1)) + second
	parser := newParser(strings.NewReader(bad))
	parser.resync = false

	msg, err := parser.ReadMessage()
	require.Nil(t, msg)
	require.Error(t, err)
}

func TestReadMessageResyncDoesNotMatchFieldValue(t *testing.T) {
	bad := "8=FIX.4.4\x019=30\x0135=D\x0134=2\x0149=TW\x0152=20260101-00:00:00\x0156=ISLD\x0111=ID\x0121=3\x0138=100\x0140=1\x0154=1\x0155=INTC\x01"
	good := wireMessage("8=FIX.4.4", "35=D", "34=3", "49=TW", "52=20260101-00:00:00", "56=ISLD", "11=ID", "21=3", "38=100", "40=1", "54=1", "55=INTC")

	parser := newParser(strings.NewReader(bad + good))
	msg, err := parser.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, good, msg.String())
}

func TestParserRejectsMessageOverLimit(t *testing.T) {
	raw := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=1", "52=20260101-00:00:00")
	parser := newParserWithLimit(strings.NewReader(raw), len(raw)-1)

	msg, err := parser.ReadMessage()
	require.Nil(t, msg)
	require.ErrorIs(t, err, errMessageTooLarge)
}

func TestParseMessageRejectsTooManyFields(t *testing.T) {
	raw := strings.Repeat("\x01", maxParsedFields+1)

	err := ParseMessage(NewMessage(), bytes.NewBufferString(raw))
	require.ErrorContains(t, err, "too many fields")
}

func TestParseMessageRejectsWrongBodyLengthWithXmlData(t *testing.T) {
	msg := NewMessage()
	msg.Header.SetString(tagBeginString, "FIX.4.4")
	msg.Header.SetString(tagMsgType, "0")
	msg.Header.SetInt(tagXMLDataLen, 3)
	msg.Header.SetBytes(tagXMLData, []byte("abc"))
	raw := msg.build()

	bad := replaceBodyLength(string(raw), "999999")
	err := ParseMessage(NewMessage(), bytes.NewBufferString(bad))
	require.ErrorContains(t, err, "Incorrect Message Length")
}

func TestParseMessageRequiresXmlDataTagAfterLength(t *testing.T) {
	raw := wireMessage("8=FIX.4.4", "35=0", "212=3", "58=abc")

	err := ParseMessage(NewMessage(), bytes.NewBufferString(raw))
	require.ErrorContains(t, err, "expected tag 213")
}

func TestParseMessageDataFieldCanContainSOH(t *testing.T) {
	body := "35=B\x0195=5\x0196=a\x01b\x01c\x01"
	raw := wireMessageBody("8=FIX.4.4", body)

	msg := NewMessage()
	require.NoError(t, ParseMessage(msg, bytes.NewBufferString(raw)))

	value, reject := msg.Body.GetBytes(tagRawData)
	require.Nil(t, reject)
	require.Equal(t, "a\x01b\x01c", string(value))
}

func TestParseMessageEveryLengthTaggedDataFieldCanContainSOH(t *testing.T) {
	// One length/data pair from each part of the table, including the last
	// one added to the spec.
	for _, pair := range [][2]Tag{
		{tagSecureDataLen, tagSecureData},
		{tagSignatureLength, tagSignature},
		{tagRawDataLength, tagRawData},
		{tagXMLDataLen, tagXMLData},
		{354, 355},   // EncodedTextLen, EncodedText
		{1401, 1402}, // EncryptedPasswordLen, EncryptedPassword
		{1620, 1621}, // RiskEncodedSecurityDescLen, RiskEncodedSecurityDesc
	} {
		t.Run(strconv.Itoa(int(pair[1])), func(t *testing.T) {
			body := fmt.Sprintf("35=B\x01%d=5\x01%d=a\x01b\x01c\x01", pair[0], pair[1])
			raw := wireMessageBody("8=FIX.4.4", body)

			msg := NewMessage()
			require.NoError(t, ParseMessage(msg, bytes.NewBufferString(raw)))

			// SecureData and XmlData are header fields, Signature a trailer one.
			section := &msg.Body.FieldMap
			switch {
			case pair[1].IsHeader():
				section = &msg.Header.FieldMap
			case pair[1].IsTrailer():
				section = &msg.Trailer.FieldMap
			}
			value, reject := section.GetBytes(pair[1])
			require.Nil(t, reject)
			require.Equal(t, "a\x01b\x01c", string(value))
		})
	}
}

func TestParseMessageDataFieldInGroupCanContainSOH(t *testing.T) {
	dd, err := datadictionary.Parse("spec/FIX44.xml")
	require.NoError(t, err)

	// News with two LinesOfText, each carrying EncodedText with SOH in it.
	body := "35=B\x01148=headline\x0133=2\x01" +
		"58=one\x01354=3\x01355=a\x01b\x01" +
		"58=two\x01354=3\x01355=c\x01d\x01"
	raw := wireMessageBody("8=FIX.4.4", body)

	msg := NewMessage()
	require.NoError(t, ParseMessageWithDataDictionary(msg, bytes.NewBufferString(raw), nil, dd))

	group := NewRepeatingGroup(33, GroupTemplate{GroupElement(58), GroupElement(354), GroupElement(355)})
	require.Nil(t, msg.Body.GetGroup(group))
	require.Equal(t, 2, group.Len())
	for i, want := range []string{"a\x01b", "c\x01d"} {
		value, reject := group.Get(i).GetBytes(355)
		require.Nil(t, reject)
		require.Equal(t, want, string(value))
	}
}

func TestReadMessageSkipsBytesBetweenMessages(t *testing.T) {
	first := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=1", "52=20260101-00:00:00")
	second := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=2", "52=20260101-00:00:01")

	parser := newParser(strings.NewReader(first + "\r\n" + second))

	msg, err := parser.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, first, msg.String())

	msg, err = parser.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, second, msg.String())
}

func TestReadMessageResyncKeepsFollowingMessage(t *testing.T) {
	first := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=1", "52=20260101-00:00:00")
	second := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=2", "52=20260101-00:00:01")

	for name, junk := range map[string]string{
		"BeginString without BodyLength": "8=X\x01",
		"stray field":                    "89=abc\x01",
	} {
		t.Run(name, func(t *testing.T) {
			parser := newParser(strings.NewReader(first + junk + second))

			msg, err := parser.ReadMessage()
			require.NoError(t, err)
			require.Equal(t, first, msg.String())

			// The junk has no BodyLength of its own, so the next message's
			// BodyLength must not be used to frame or skip it.
			msg, err = parser.ReadMessage()
			require.NoError(t, err)
			require.Equal(t, second, msg.String())
		})
	}
}

// Framing and validation must both use the BodyLength in the second field. A
// 9= anywhere else must not move the frame boundary or be validated instead.
func TestReadMessageFramesOnlyFromSecondFieldBodyLength(t *testing.T) {
	next := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=2", "52=20260101-00:00:01")

	rest := "56=ISLD\x0134=1\x0152=20260101-00:00:00\x01"
	notSecond := withValidCheckSum("8=FIX.4.4\x0135=0\x0149=TW\x019=" + strconv.Itoa(len(rest)) + "\x01" + rest + "10=000\x01")

	duplicate := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=1", "52=20260101-00:00:00", "9=3")

	// The first BodyLength covers only MsgType. The second covers the rest
	// exactly, so framing from it would produce a well-formed message.
	rest = "49=TW\x0156=ISLD\x0134=1\x0152=20260101-00:00:00\x01"
	shortFirst := withValidCheckSum("8=FIX.4.4\x019=5\x0135=0\x019=" + strconv.Itoa(len(rest)) + "\x01" + rest + "10=000\x01")

	tests := []struct {
		name string
		bad  string
		// framed is true when the bad message is framed by its first BodyLength
		// and only rejected when it is parsed.
		framed bool
	}{
		{name: "BodyLength not second", bad: notSecond},
		{name: "duplicate BodyLength", bad: duplicate, framed: true},
		{name: "first BodyLength short", bad: shortFirst},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parser := newParser(strings.NewReader(test.bad + next))

			if test.framed {
				msg, err := parser.ReadMessage()
				require.NoError(t, err)
				require.Equal(t, test.bad, msg.String())

				err = ParseMessage(NewMessage(), msg)
				require.ErrorContains(t, err, "duplicate tag 9")
			}

			msg, err := parser.ReadMessage()
			require.NoError(t, err)
			require.Equal(t, next, msg.String())
			require.NoError(t, ParseMessage(NewMessage(), msg))
		})
	}
}

type eventLog struct {
	nullLog
	events []string
}

func (l *eventLog) OnEvent(s string) { l.events = append(l.events, s) }
func (l *eventLog) OnEventf(format string, a ...any) {
	l.OnEvent(fmt.Sprintf(format, a...))
}

func TestReadMessageLogsSkippedMessage(t *testing.T) {
	first := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=1", "52=20260101-00:00:00")
	second := wireMessage("8=FIX.4.4", "35=0", "49=TW", "56=ISLD", "34=2", "52=20260101-00:00:01")

	index := strings.Index(first, "\x019=")
	end := strings.IndexByte(first[index+3:], '\x01') + index + 3
	length, err := strconv.Atoi(first[index+3 : end])
	require.NoError(t, err)

	log := &eventLog{}
	parser := newParser(strings.NewReader(replaceBodyLength(first, strconv.Itoa(length+1)) + second))
	parser.log = log

	msg, err := parser.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, second, msg.String())

	require.Len(t, log.events, 1)
	require.Contains(t, log.events[0], "BodyLength does not end")
	// The skipped bytes may hold credentials, so only their count is logged.
	require.NotContains(t, log.events[0], "ISLD")
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

func TestDataTagForCoversSpecLengthFields(t *testing.T) {
	specs, err := filepath.Glob("spec/*.xml")
	require.NoError(t, err)
	require.NotEmpty(t, specs)

	for _, spec := range specs {
		dd, err := datadictionary.Parse(spec)
		require.NoError(t, err)

		for tag, field := range dd.FieldTypeByTag {
			// BodyLength and MaxMessageSize are LENGTH fields with no data.
			if field.Type != "LENGTH" || tag == int(tagBodyLength) || tag == 383 {
				continue
			}
			dataTag := dataTagFor(Tag(tag))
			require.NotZero(t, dataTag, "%s: length field %d has no data field", spec, tag)

			data, ok := dd.FieldTypeByTag[int(dataTag)]
			require.True(t, ok, "%s: data field %d for length field %d is not defined", spec, dataTag, tag)
			require.Contains(t, []string{"DATA", "XMLDATA"}, data.Type, "%s: field %d", spec, dataTag)
		}
	}
}

func TestParseMessageRejectsDuplicateAndMisplacedTags(t *testing.T) {
	dd, err := datadictionary.Parse("spec/FIX44.xml")
	require.NoError(t, err)

	header := []string{"35=D", "49=TW", "56=ISLD", "34=2", "52=20260101-00:00:00"}
	order := []string{"11=ID", "21=1", "55=INTC", "54=1", "60=20260101-00:00:00", "40=1", "38=100"}
	fields := func(groups ...[]string) []string {
		var all []string
		for _, group := range groups {
			all = append(all, group...)
		}
		return all
	}

	tests := []struct {
		name   string
		fields []string
		dict   *datadictionary.DataDictionary
		reason int
		tag    Tag
	}{
		{name: "duplicate header tag", fields: fields(header, []string{"43=N", "43=Y", "122=20260101-00:00:00"}, order), reason: rejectReasonTagAppearsMoreThanOnce, tag: tagPossDupFlag},
		{name: "header tag after body", fields: fields(header, order, []string{"43=Y"}), reason: rejectReasonTagSpecifiedOutOfRequiredOrder, tag: tagPossDupFlag},
		{name: "duplicate trailer tag", fields: fields(header, order, []string{"93=1", "89=a", "93=1", "89=b"}), reason: rejectReasonTagAppearsMoreThanOnce, tag: tagSignatureLength},
		{name: "duplicate session body tag", fields: fields([]string{"35=1", "49=TW", "56=ISLD", "34=2", "52=20260101-00:00:00", "112=A", "112=B"}), reason: rejectReasonTagAppearsMoreThanOnce, tag: tagTestReqID},
		{name: "duplicate body tag with dictionary", fields: fields(header, order, []string{"38=1000000"}), dict: dd, reason: rejectReasonTagAppearsMoreThanOnce, tag: 38},
		{name: "header tag after group", fields: fields([]string{"35=B", "49=TW", "56=ISLD", "34=2", "52=20260101-00:00:00", "148=x", "33=1", "58=a", "43=Y"}), dict: dd, reason: rejectReasonTagSpecifiedOutOfRequiredOrder, tag: tagPossDupFlag},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := wireMessage("8=FIX.4.4", test.fields...)

			msg := NewMessage()
			require.NoError(t, ParseMessageWithDataDictionary(msg, bytes.NewBufferString(raw), nil, test.dict))

			require.NotNil(t, msg.parseReject)
			require.Equal(t, test.reason, msg.parseReject.RejectReason())
			require.Equal(t, test.tag, *msg.parseReject.RefTagID())

			// A repeated header tag keeps its first value.
			if value, reject := msg.Header.GetString(tagPossDupFlag); reject == nil && test.reason == rejectReasonTagAppearsMoreThanOnce {
				require.Equal(t, "N", value)
			}
		})
	}
}

func TestParseMessageAllowsRepeatedTagsWhereValid(t *testing.T) {
	dd, err := datadictionary.Parse("spec/FIX44.xml")
	require.NoError(t, err)

	tests := []struct {
		name   string
		fields []string
		dict   *datadictionary.DataDictionary
	}{
		// Without a dictionary a repeated body tag may be a group member.
		{name: "body tag without dictionary", fields: []string{"35=D", "49=TW", "56=ISLD", "34=2", "52=20260101-00:00:00", "38=100", "38=200"}},
		{name: "group members with dictionary", fields: []string{"35=B", "49=TW", "56=ISLD", "34=2", "52=20260101-00:00:00", "148=x", "33=2", "58=a", "58=b"}, dict: dd},
		{name: "hops", fields: []string{"35=0", "49=TW", "56=ISLD", "34=2", "52=20260101-00:00:00", "627=2", "628=A", "628=B"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := wireMessage("8=FIX.4.4", test.fields...)

			msg := NewMessage()
			require.NoError(t, ParseMessageWithDataDictionary(msg, bytes.NewBufferString(raw), nil, test.dict))
			require.Nil(t, msg.parseReject)
		})
	}

	// A repeated body tag without a dictionary keeps its last value, as before.
	msg := NewMessage()
	raw := wireMessage("8=FIX.4.4", "35=D", "49=TW", "56=ISLD", "34=2", "52=20260101-00:00:00", "38=100", "38=200")
	require.NoError(t, ParseMessage(msg, bytes.NewBufferString(raw)))
	value, reject := msg.Body.GetString(38)
	require.Nil(t, reject)
	require.Equal(t, "200", value)
}
