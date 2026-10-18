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
	"io"
	"time"

	"golang.org/x/time/rate"
)

const maxBurst = 1

// messageOutCapacity is the buffer of a connection's outbound channel. A
// session that finds it full waits for the write loop to take a message. The
// buffer lets a session hand over a backlog in one go, and the write loop then
// writes it with a single flush.
const messageOutCapacity = 64

// writeLoop writes each message from messageOut to connection, calling ready,
// if set, as it takes each one and recording each one to metrics. Messages
// already waiting are written together and flushed once.
func writeLoop(connection io.Writer, messageOut chan []byte, log Log, maxMessagesPerSecond int, metrics Metrics, ready func()) {
	limiter := rate.NewLimiter(rate.Limit(maxMessagesPerSecond), maxBurst)
	w := bufio.NewWriter(connection)

	flush := func() {
		if err := w.Flush(); err != nil {
			log.OnEvent(err.Error())
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

			if maxMessagesPerSecond > 0 {
				// Flush what is written before waiting for the limiter, so
				// that earlier messages are not held back too.
				if delay := limiter.Reserve().Delay(); delay > 0 {
					flush()
					time.Sleep(delay)
				}
			}

			metrics.MessageSent(len(msg))

			if _, err := w.Write(msg); err != nil {
				log.OnEvent(err.Error())
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
