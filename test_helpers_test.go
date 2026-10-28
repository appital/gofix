package gofix

import (
	"fmt"
	"strconv"
	"strings"
)

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
