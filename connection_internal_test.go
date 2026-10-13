// Copyright (c) quickfixengine.org  All rights reserved.
//
// This file may be distributed under the terms of the quickfixengine.org
// license as defined by quickfixengine.org and appearing in the file
// LICENSE included in the packaging of this file.
//
// This file is provided AS IS with NO WARRANTY OF ANY KIND, INCLUDING
// THE WARRANTY OF DESIGN, MERCHANTABILITY AND FITNESS FOR A
// PARTICULAR PURPOSE.
//
// See http://www.quickfixengine.org/LICENSE for licensing information.
//
// Contact ask@quickfixengine.org if any conditions of this licensing
// are not clear to you.

package gofix

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWriteLoop(t *testing.T) {
	writer := bytes.NewBufferString("")
	msgOut := make(chan []byte)

	go func() {
		msgOut <- []byte("test msg 1 ")
		msgOut <- []byte("test msg 2 ")
		msgOut <- []byte("test msg 3")
		close(msgOut)
	}()
	writeLoop(writer, msgOut, nullLog{}, 0, "sessionID", nil)

	expected := "test msg 1 test msg 2 test msg 3"

	if writer.String() != expected {
		t.Errorf("expected %v got %v", expected, writer.String())
	}
}

func TestReadLoop(t *testing.T) {
	msgIn := make(chan fixIn)
	stream := "hello8=FIX.4.09=5blah10=103garbage8=FIX.4.09=4foo10=103"

	parser := newParser(strings.NewReader(stream))
	go readLoop(parser, msgIn, nullLog{}, "sessionID")

	var tests = []struct {
		expectedMsg   string
		channelClosed bool
	}{
		{expectedMsg: "8=FIX.4.09=5blah10=103"},
		{expectedMsg: "8=FIX.4.09=4foo10=103"},
		{channelClosed: true},
	}

	for _, test := range tests {
		msg, ok := <-msgIn
		switch {
		case !ok && !test.channelClosed:
			t.Error("Channel unexpectedly closed")
			fallthrough
		case !ok && test.channelClosed:
			continue
		}

		if msg.bytes.String() != test.expectedMsg {
			t.Errorf("Expected %v got %v", test.expectedMsg, msg.bytes.String())
		}
	}
}

// A session that finds messageOut full must not wake itself to retry, which
// spun it in a busy loop while the write loop was busy; the write loop wakes it
// once it takes a message.
func TestSendQueuedWaitsForWriter(t *testing.T) {
	msgOut := make(chan []byte, messageOutCapacity)
	s := &session{log: nullLog{}, messageOut: msgOut, messageEvent: make(chan bool, 1)}
	woken := func() bool {
		select {
		case <-s.messageEvent:
			return true
		default:
			return false
		}
	}

	for range messageOutCapacity {
		msgOut <- []byte("earlier")
	}
	s.toSend = [][]byte{[]byte("b"), []byte("c")}

	s.sendQueued(false)
	assert.Len(t, s.toSend, 2, "nothing fits while messageOut is full")
	assert.False(t, woken(), "a full messageOut must not wake the session")

	<-msgOut
	s.writerReady()
	assert.True(t, woken(), "taking a message must wake the waiting session")
	s.sendQueued(false)
	assert.Equal(t, [][]byte{[]byte("c")}, s.toSend, "one slot was freed, so one message is sent")
	assert.False(t, woken())

	<-msgOut
	s.writerReady()
	assert.True(t, woken(), "taking a message must wake the waiting session")
	s.sendQueued(false)
	assert.Empty(t, s.toSend)

	for len(msgOut) > 0 {
		<-msgOut
		s.writerReady()
	}
	assert.False(t, woken(), "nothing was waiting, so the session must not be woken")
}

// With MaxMessagesPerSecond set, messages are paced, and what is already
// written is flushed before waiting rather than held back.
func TestWriteLoopRateLimited(t *testing.T) {
	msgOut := make(chan []byte, messageOutCapacity)
	for _, m := range []string{"a", "b", "c"} {
		msgOut <- []byte(m)
	}
	close(msgOut)

	w := &countingWriter{}
	start := time.Now()
	writeLoop(w, msgOut, nullLog{}, 20, "sessionID", nil)

	assert.Equal(t, "abc", w.buf.String())
	assert.Equal(t, 3, w.writes, "each paced message should be flushed before the next wait")
	assert.GreaterOrEqual(t, time.Since(start), 90*time.Millisecond, "two waits of 50ms at 20 msg/s")
}

// Messages already waiting when the write loop takes one are written with a
// single flush, and the loop still writes everything when messageOut closes.
func TestWriteLoopBatches(t *testing.T) {
	msgOut := make(chan []byte, messageOutCapacity)
	for _, m := range []string{"a", "b", "c"} {
		msgOut <- []byte(m)
	}
	close(msgOut)

	w := &countingWriter{}
	readies := 0
	writeLoop(w, msgOut, nullLog{}, 0, "sessionID", func() { readies++ })

	assert.Equal(t, "abc", w.buf.String())
	assert.Equal(t, 1, w.writes, "waiting messages should go out in one write")
	assert.Equal(t, 3, readies, "ready is called for every message taken")
}

type countingWriter struct {
	buf    bytes.Buffer
	writes int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.buf.Write(p)
}
