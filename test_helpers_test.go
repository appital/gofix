package gofix

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// starter is an Acceptor or an Initiator.
type starter interface {
	Start(ctx context.Context) error
	Wait()
}

// startForTest starts s with a context that is cancelled when the test ends,
// and fails the test if s has not then shut down within ten seconds. stop
// cancels the context early and waits for s to shut down; it may be called
// more than once.
func startForTest(t *testing.T, s starter) (stop func(), err error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stop = func() {
		cancel()
		s.Wait()
	}
	if err = s.Start(ctx); err != nil {
		cancel()
		return stop, err
	}
	t.Cleanup(func() {
		cancel()
		if !waitWithin(s, 10*time.Second) {
			t.Error("did not shut down after its context was cancelled")
		}
	})
	return stop, nil
}

// waitWithin reports whether s.Wait returns within d.
func waitWithin(s starter, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

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
