package gofix

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// runner is an Acceptor or an Initiator.
type runner interface {
	Run(ctx context.Context) error
	Ready() <-chan struct{}
}

// testSource is a SettingsSource whose Settings a test replaces, and which
// fails with err while it is set. It counts how often it has been called.
type testSource struct {
	mu       sync.Mutex
	settings *Settings
	err      error
	loads    int
}

func newTestSource(settings *Settings) *testSource {
	return &testSource{settings: settings}
}

func (s *testSource) load(context.Context) (*Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	return s.settings, s.err
}

func (s *testSource) set(settings *Settings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = settings
}

func (s *testSource) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *testSource) loaded() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads
}

// reloader is an Acceptor or an Initiator.
type reloader interface {
	Reload(ctx context.Context) error
}

// reloadWith makes source return settings, then reloads r from it.
func reloadWith(r reloader, source *testSource, settings *Settings) error {
	source.set(settings)
	return r.Reload(context.Background())
}

// running is a runner whose Run was called by runForTest.
type running struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	// handled is set when err has been returned to the test, so that the
	// cleanup does not report it again.
	handled bool
}

// stop cancels Run's context and returns Run's error once it has returned. It
// may be called more than once.
func (r *running) stop() error {
	r.cancel()
	<-r.done
	return r.err
}

// runForTest calls r.Run in the background with a context that is cancelled
// when the test ends, and fails the test if Run has not then returned nil
// within ten seconds.
func runForTest(t *testing.T, r runner) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	run := &running{cancel: cancel, done: make(chan struct{})}
	go func() {
		run.err = r.Run(ctx)
		close(run.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-run.done:
			if run.err != nil && !run.handled {
				t.Errorf("Run returned %v", run.err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after its context was cancelled")
		}
	})
	return run
}

// startForTest runs a as runForTest does and returns once it is ready, or
// with the error that stopped it starting. stop shuts it down early and waits
// for Run to return; it may be called more than once.
func startForTest(t *testing.T, a runner) (stop func(), err error) {
	t.Helper()
	run := runForTest(t, a)
	select {
	case <-a.Ready():
		return func() { _ = run.stop() }, nil
	case <-run.done:
		run.handled = true
		return func() {}, run.err
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
