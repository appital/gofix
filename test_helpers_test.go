package gofix

import (
	"fmt"
	"strconv"
	"strings"
)

// withValidCheckSum recomputes CheckSum(10) for a complete FIX frame used as a
// test fixture. Tests that are about something other than wire integrity can
// use it to keep their fixtures valid without repeating the arithmetic.
func withValidCheckSum(raw string) string {
	index := strings.LastIndex(raw, "\x0110=")
	if index < 0 {
		return raw
	}

	start := index + 1
	end := strings.IndexByte(raw[start:], '\x01')
	if end < 0 {
		return raw
	}

	total := 0
	for i := 0; i < start; i++ {
		total += int(raw[i])
	}

	return raw[:start] + fmt.Sprintf("10=%03d", total%256) + raw[start+end:]
}

// wireMessageBody builds a frame around body, which must already be terminated
// by SOH. It computes BodyLength and CheckSum from body.
func wireMessageBody(beginString, body string) string {
	msg := beginString + "\x019=" + strconv.Itoa(len(body)) + "\x01" + body
	total := 0
	for i := 0; i < len(msg); i++ {
		total += int(msg[i])
	}
	return msg + fmt.Sprintf("10=%03d", total%256) + "\x01"
}

// wireMessage builds a frame from fields, adding the delimiters, BodyLength and
// CheckSum.
func wireMessage(beginString string, fields ...string) string {
	var body strings.Builder
	for _, field := range fields {
		body.WriteString(field)
		body.WriteByte('\x01')
	}
	return wireMessageBody(beginString, body.String())
}
