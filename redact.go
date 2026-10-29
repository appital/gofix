package gofix

import (
	"bytes"
	"strconv"
	"strings"
)

// redactedValue replaces the value of each redacted field.
const redactedValue = "***"

// defaultRedactedTags are the fields redacted unless the RedactedTags setting
// says otherwise: Password, NewPassword, EncryptedPassword and
// EncryptedNewPassword.
var defaultRedactedTags = []Tag{554, 925, 1402, 1404}

// redactor masks the values of sensitive fields, such as passwords, in raw
// FIX messages before they are logged or stored. A nil redactor redacts
// nothing.
type redactor struct {
	tags []Tag
	// small has a bit set for each tag up to maxRedactedTag, and large lists
	// any tags above it.
	small [maxRedactedTag/64 + 1]uint64
	large []Tag
}

// maxRedactedTag is the largest tag mayRedact finds with a bit set, rather
// than by searching a list.
const maxRedactedTag = 4095

// newRedactor returns a redactor for tags, or nil if there are none.
func newRedactor(tags []Tag) *redactor {
	if len(tags) == 0 {
		return nil
	}
	r := &redactor{tags: tags}
	for _, tag := range tags {
		if tag <= maxRedactedTag {
			r.small[tag/64] |= 1 << (tag % 64)
		} else {
			r.large = append(r.large, tag)
		}
	}
	return r
}

func (r *redactor) redacts(tag Tag) bool {
	if tag >= 0 && tag <= maxRedactedTag {
		return r.small[tag/64]&(1<<(tag%64)) != 0
	}
	for _, t := range r.large {
		if t == tag {
			return true
		}
	}
	return false
}

// redactsSmall is redacts for a tag mayRedact has parsed, which it stops
// parsing once it is above maxRedactedTag.
func (r *redactor) redactsSmall(tag int) bool {
	if tag > maxRedactedTag {
		return len(r.large) > 0
	}
	return r.small[tag/64]&(1<<(tag%64)) != 0
}

// redact returns msg with the value of each redacted field replaced by
// redactedValue, along with the length field of a redacted data field. If
// msg is a whole message, its BodyLength and CheckSum are updated to match,
// so the result still parses. msg is never modified: it is returned as it is
// if there is nothing to redact, and copied otherwise.
//
// msg need not be a valid message. Any text that is not a field, such as an
// event's own wording around a message, is kept as it is.
func (r *redactor) redact(msg []byte) []byte {
	if r == nil || !r.mayRedact(msg) {
		return msg
	}

	var (
		edits = make([]redaction, 0, 2)

		// The last length field, until the data field it describes.
		dataTag    Tag
		dataLen    int
		dataLenEnd int
		dataLenAt  int
	)
	for start := 0; start < len(msg); {
		eq := bytes.IndexByte(msg[start:], '=')
		if eq < 0 {
			break
		}
		if soh := bytes.IndexByte(msg[start:start+eq], '\001'); soh >= 0 {
			// Not a field, but one may follow.
			start += soh + 1
			continue
		}
		tag, tagErr := parseUInt(msg[start : start+eq])
		valueStart := start + eq + 1

		valueEnd := len(msg)
		if dataTag != 0 && Tag(tag) == dataTag && valueStart+dataLen <= len(msg) &&
			(valueStart+dataLen == len(msg) || msg[valueStart+dataLen] == '\001') {
			valueEnd = valueStart + dataLen
		} else if soh := bytes.IndexByte(msg[valueStart:], '\001'); soh >= 0 {
			valueEnd = valueStart + soh
		}

		if tagErr == nil && r.redacts(Tag(tag)) {
			if dataTag != 0 && Tag(tag) == dataTag {
				edits = append(edits, redaction{dataLenAt, dataLenEnd, strconv.Itoa(len(redactedValue))})
			}
			edits = append(edits, redaction{valueStart, valueEnd, redactedValue})
		}

		dataTag = 0
		if tagErr == nil {
			if next := dataTagFor(Tag(tag)); next != 0 {
				if n, err := parseUInt(msg[valueStart:valueEnd]); err == nil {
					dataTag, dataLen, dataLenAt, dataLenEnd = next, n, valueStart, valueEnd
				}
			}
		}

		start = valueEnd + 1
	}

	if len(edits) == 0 {
		return msg
	}

	out := make([]byte, 0, len(msg))
	last := 0
	for _, e := range edits {
		out = append(out, msg[last:e.start]...)
		out = append(out, e.value...)
		last = e.end
	}
	out = append(out, msg[last:]...)

	return reframe(out)
}

// redactString is redact for a string, such as a log event.
func (r *redactor) redactString(s string) string {
	if r == nil || strings.IndexByte(s, '\001') < 0 {
		return s
	}
	return string(r.redact([]byte(s)))
}

// mayRedact reports whether a field that starts msg, or follows a field
// delimiter, has a redacted tag. It may report a field inside the value of a
// data field, which redact then leaves alone.
//
// It runs for every message logged, so it scans msg once, byte by byte,
// rather than searching it for each tag.
func (r *redactor) mayRedact(msg []byte) bool {
	for i := 0; i < len(msg); i++ {
		start, tag := i, 0
		for ; i < len(msg) && msg[i]-'0' <= 9 && tag <= maxRedactedTag; i++ {
			tag = tag*10 + int(msg[i]-'0')
		}
		if i > start && i < len(msg) && msg[i] == '=' && r.redactsSmall(tag) {
			return true
		}
		for i < len(msg) && msg[i] != '\001' {
			i++
		}
	}
	return false
}

// redaction replaces msg[start:end] with value.
type redaction struct {
	start, end int
	value      string
}

// reframe sets the BodyLength and CheckSum of msg to match its content, if it
// is a whole message: BeginString, then BodyLength, through to a CheckSum
// field at the end. Anything else is returned as it is.
func reframe(msg []byte) []byte {
	if !bytes.HasPrefix(msg, []byte("8=")) {
		return msg
	}
	beginEnd := bytes.IndexByte(msg, '\001') + 1
	if beginEnd == 0 || !bytes.HasPrefix(msg[beginEnd:], []byte("9=")) {
		return msg
	}
	bodyLengthEnd := bytes.IndexByte(msg[beginEnd:], '\001') + 1
	if bodyLengthEnd == 0 {
		return msg
	}
	bodyStart := beginEnd + bodyLengthEnd

	const checkSumLen = len("10=000\001")
	bodyEnd := len(msg) - checkSumLen
	if bodyEnd < bodyStart || msg[len(msg)-1] != '\001' ||
		!bytes.HasPrefix(msg[bodyEnd:], []byte("10=")) || (bodyEnd > 0 && msg[bodyEnd-1] != '\001') {
		return msg
	}

	out := make([]byte, 0, len(msg)+4)
	out = append(out, msg[:beginEnd]...)
	out = append(out, "9="...)
	out = strconv.AppendInt(out, int64(bodyEnd-bodyStart), 10)
	out = append(out, '\001')
	out = append(out, msg[bodyStart:bodyEnd]...)
	checkSum := checkSumDigits(bytesTotal(out) % 256)
	out = append(out, "10="...)
	out = append(out, checkSum[:]...)
	return append(out, '\001')
}

// redactingLog redacts messages, and events that include them, before passing
// them to the Log it wraps.
type redactingLog struct {
	Log
	redactor *redactor
}

// newRedactingLog wraps log so that it redacts with r. It returns log as it is
// if r redacts nothing or log is the null log.
func newRedactingLog(log Log, r *redactor) Log {
	if _, ok := log.(nullLog); ok || r == nil {
		return log
	}
	return redactingLog{Log: log, redactor: r}
}

func (l redactingLog) OnIncoming(msg []byte) { l.Log.OnIncoming(l.redactor.redact(msg)) }

func (l redactingLog) OnOutgoing(msg []byte) { l.Log.OnOutgoing(l.redactor.redact(msg)) }

func (l redactingLog) OnEvent(event string) { l.Log.OnEvent(l.redactor.redactString(event)) }

// OnEventf redacts each argument that may hold a message. The format string
// always comes from gofix, never from a peer.
func (l redactingLog) OnEventf(format string, args ...any) {
	redacted := args
	for i, arg := range args {
		var s string
		switch a := arg.(type) {
		case string:
			s = a
		case []byte:
			s = string(a)
		case *bytes.Buffer:
			s = a.String()
		case *Message:
			s = a.String()
		case error:
			s = a.Error()
		default:
			continue
		}
		if strings.IndexByte(s, '\001') < 0 {
			continue
		}
		if &redacted[0] == &args[0] {
			redacted = append([]any(nil), args...)
		}
		redacted[i] = l.redactor.redactString(s)
	}
	l.Log.OnEventf(format, redacted...)
}
