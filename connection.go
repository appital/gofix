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
	"bufio"
	"errors"
	"io"
	"net"
	"runtime/debug"
	"time"

	"golang.org/x/time/rate"
)

const maxBurst = 1

// messageOutCapacity is the buffer of a connection's outbound channel. A
// session that finds it full waits for the write loop to take a message. The
// buffer lets a session hand over a backlog in one go, and the write loop then
// writes it with a single flush.
const messageOutCapacity = 64

// defaultWriteTimeout bounds each write to a connection unless a session
// sets SocketWriteTimeout.
const defaultWriteTimeout = 30 * time.Second

// writeDeadliner is the part of a net.Conn writeLoop uses to bound its writes.
type writeDeadliner interface {
	SetWriteDeadline(t time.Time) error
}

// writeLoop writes each message from messageOut to connection, calling ready,
// if set, as it takes each one and recording each one to metrics. Messages
// already waiting are written together and flushed once.
//
// If connection has a write deadline, each write to it must finish within
// writeTimeout, so that a peer that stops reading cannot block the loop. Once
// a write fails, writeLoop closes connection, if it can be closed, which
// ends the session's read loop and so disconnects the session. It then takes
// and discards messages until messageOut is closed, so that the session is
// never left waiting on a full messageOut.
func writeLoop(connection io.Writer, messageOut chan []byte, log Log, maxMessagesPerSecond int, writeTimeout time.Duration, metrics Metrics, ready func()) {
	limiter := rate.NewLimiter(rate.Limit(maxMessagesPerSecond), maxBurst)
	w := bufio.NewWriter(connection)

	deadliner, _ := connection.(writeDeadliner)
	if writeTimeout <= 0 {
		deadliner = nil
	}

	failed := false
	fail := func(err error) {
		log.OnEvent(err.Error())
		failed = true
		if closer, ok := connection.(io.Closer); ok {
			if err := closer.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				log.OnEvent(err.Error())
			}
		}
	}

	// setDeadline is called before each write that reaches connection.
	setDeadline := func() {
		if deadliner == nil {
			return
		}
		if err := deadliner.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
			fail(err)
		}
	}

	flush := func() {
		if failed || w.Buffered() == 0 {
			return
		}
		if setDeadline(); failed {
			return
		}
		if err := w.Flush(); err != nil {
			fail(err)
		}
	}

	for {
		msg, ok := <-messageOut
		if !ok {
			return
		}

		for {
			if ready != nil {
				ready()
			}

			if maxMessagesPerSecond > 0 && !failed {
				// Flush what is written before waiting for the limiter, so
				// that earlier messages are not held back too.
				if delay := limiter.Reserve().Delay(); delay > 0 {
					flush()
					time.Sleep(delay)
				}
			}

			// The bufio.Writer writes to connection only once msg does not
			// fit in what is left of its buffer.
			if !failed && len(msg) > w.Available() {
				setDeadline()
			}

			if !failed {
				metrics.MessageSent(len(msg))
				if _, err := w.Write(msg); err != nil {
					fail(err)
				}
			}

			select {
			case msg, ok = <-messageOut:
				if ok {
					continue
				}
			default:
			}
			break
		}

		flush()
		if !ok {
			return
		}
	}
}

func readLoop(parser *parser, msgIn chan fixIn, log Log, metrics Metrics) {
	defer close(msgIn)
	parser.log = log

	// Nothing above this goroutine recovers, so a panic here would end the
	// process. Stop reading instead, which disconnects the session.
	defer func() {
		if r := recover(); r != nil {
			log.OnEventf("Read Loop Terminated with Panic: %v\n%s", r, debug.Stack())
		}
	}()

	for {
		msg, err := parser.ReadMessage()
		if err != nil {
			log.OnEvent(err.Error())
			return
		}

		metrics.MessageReceived(msg.Len())

		msgIn <- fixIn{bytes: msg, receiveTime: parser.lastRead, pooled: true}
	}
}
