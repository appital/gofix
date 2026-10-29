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
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	writeLoop(writer, msgOut, nullLog{}, 0, 0, nullMetrics{}, nil)

	expected := "test msg 1 test msg 2 test msg 3"

	if writer.String() != expected {
		t.Errorf("expected %v got %v", expected, writer.String())
	}
}

func TestReadLoop(t *testing.T) {
	msgIn := make(chan fixIn)
	stream := "hello8=FIX.4.0\x019=5\x01blah\x0110=103\x018=FIX.4.0\x019=4\x01foo\x0110=103\x01"

	parser := newParser(strings.NewReader(stream))
	go readLoop(parser, msgIn, nullLog{}, nullMetrics{})

	var tests = []struct {
		expectedMsg   string
		channelClosed bool
	}{
		{expectedMsg: "8=FIX.4.0\x019=5\x01blah\x0110=103\x01"},
		{expectedMsg: "8=FIX.4.0\x019=4\x01foo\x0110=103\x01"},
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

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("read failed") }

func TestReadLoopRecoversPanic(t *testing.T) {
	msgIn := make(chan fixIn)
	done := make(chan struct{})
	go func() {
		defer close(done)
		readLoop(newParser(panicReader{}), msgIn, nullLog{}, nullMetrics{})
	}()

	_, ok := <-msgIn
	assert.False(t, ok, "msgIn should be closed")
	<-done
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
	writeLoop(w, msgOut, nullLog{}, 20, 0, nullMetrics{}, nil)

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
	writeLoop(w, msgOut, nullLog{}, 0, 0, nullMetrics{}, func() { readies++ })

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

// The write loop records every message it writes, and the read loop every
// message it parses, with their lengths.
func TestLoopsRecordMetrics(t *testing.T) {
	msgOut := make(chan []byte, messageOutCapacity)
	for _, m := range []string{"a", "bb", "ccc"} {
		msgOut <- []byte(m)
	}
	close(msgOut)

	sent := &recordingMetrics{}
	writeLoop(&countingWriter{}, msgOut, nullLog{}, 0, 0, sent, nil)
	assert.Equal(t, 3, sent.sentCount)
	assert.Equal(t, 6, sent.sentBytes)

	msgIn := make(chan fixIn)
	received := &recordingMetrics{}
	stream := "8=FIX.4.0\x019=5\x01blah\x0110=103\x018=FIX.4.0\x019=4\x01foo\x0110=103\x01"
	go readLoop(newParser(strings.NewReader(stream)), msgIn, nullLog{}, received)
	for range msgIn { //nolint:revive // drain the channel until readLoop closes it.
	}
	assert.Equal(t, 2, received.receivedCount)
	assert.Equal(t, len(stream), received.receivedBytes)
	assert.Zero(t, received.sentCount)
}

type recordingMetrics struct {
	sentCount, sentBytes         int
	receivedCount, receivedBytes int
}

func (m *recordingMetrics) MessageSent(n int) {
	m.sentCount++
	m.sentBytes += n
}

func (m *recordingMetrics) MessageReceived(n int) {
	m.receivedCount++
	m.receivedBytes += n
}

// A write to a peer that has stopped reading times out. The write loop then
// closes the connection, and takes the messages the session still sends
// without writing them until messageOut is closed, so that the session is
// never left waiting on it.
func TestWriteLoopTimesOutStalledWrite(t *testing.T) {
	local, peer := net.Pipe() // Each write waits until peer reads it.
	defer peer.Close()

	log := &eventLog{}
	msgOut := make(chan []byte)
	done := make(chan struct{})
	go func() {
		defer close(done)
		writeLoop(local, msgOut, log, 0, 50*time.Millisecond, nullMetrics{}, nil)
	}()

	start := time.Now()
	for range 1000 {
		select {
		case msgOut <- []byte("msg"):
		case <-time.After(5 * time.Second):
			t.Fatal("the write loop stopped taking messages")
		}
	}
	assert.Less(t, time.Since(start), 5*time.Second)

	// The peer sees the connection closed.
	_, err := peer.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)

	close(msgOut)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the write loop did not return once messageOut closed")
	}
	require.NotEmpty(t, log.events)
	assert.Contains(t, log.events[0], "i/o timeout")
	assert.Len(t, log.events, 1, "only the failed write is logged: %v", log.events)
}

// A write that finishes in time is not affected by the timeout, however long
// the loop then waits for the next message.
func TestWriteLoopTimeoutAllowsIdleConnection(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()

	msgOut := make(chan []byte)
	done := make(chan struct{})
	go func() {
		defer close(done)
		writeLoop(local, msgOut, nullLog{}, 0, 50*time.Millisecond, nullMetrics{}, nil)
	}()

	buf := make([]byte, 3)
	for range 2 {
		msgOut <- []byte("msg")
		_, err := io.ReadFull(peer, buf)
		require.NoError(t, err)
		assert.Equal(t, "msg", string(buf))
		time.Sleep(100 * time.Millisecond)
	}

	close(msgOut)
	<-done
}
