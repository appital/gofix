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
	"context"
	"io"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/time/rate"
)

const maxBurst = 1

var (
	outgoingMessageCount = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gofix",
		Name:      "outgoing_message_count",
		Help:      "Count of messages sent inside gofix writeLoop",
	}, []string{"sessionID"})

	outgoingMessageBytes = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gofix",
		Name:      "outgoing_message_bytes",
		Help:      "Count of bytes sent inside gofix writeLoop",
	}, []string{"sessionID"})

	incomingMessageCount = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gofix",
		Name:      "incoming_message_count",
		Help:      "Count of messages received inside gofix readLoop",
	}, []string{"sessionID"})

	incomingMessageBytes = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gofix",
		Name:      "incoming_message_bytes",
		Help:      "Count of bytes received inside gofix readLoop",
	}, []string{"sessionID"})
)

// messageOutCapacity is the buffer of a connection's outbound channel. A
// session that finds it full waits for the write loop to take a message, and
// one slot guarantees the write loop takes another after any failed send.
const messageOutCapacity = 1

// writeLoop writes each message from messageOut to connection, calling ready,
// if set, as it takes each one.
func writeLoop(connection io.Writer, messageOut chan []byte, log Log, maxMessagesPerSecond int, sessionID string, ready func()) {
	limiter := rate.NewLimiter(rate.Limit(maxMessagesPerSecond), maxBurst)
	ctx := context.Background()
	messageCount := outgoingMessageCount.WithLabelValues(sessionID)
	messageBytes := outgoingMessageBytes.WithLabelValues(sessionID)

	for {
		msg, ok := <-messageOut
		if !ok {
			return
		}
		if ready != nil {
			ready()
		}

		if maxMessagesPerSecond > 0 {
			err := limiter.Wait(ctx)
			// if we get an error from the limiter we log, but continue on to
			// attempt to write the message to the remote party
			if err != nil {
				log.OnEvent(err.Error())
			}
		}

		messageCount.Inc()
		messageBytes.Add(float64(len(msg)))

		if _, err := connection.Write(msg); err != nil {
			log.OnEvent(err.Error())
		}
	}
}

func readLoop(parser *parser, msgIn chan fixIn, log Log, sessionID string) {
	defer close(msgIn)

	messageCount := incomingMessageCount.WithLabelValues(sessionID)
	messageBytes := incomingMessageBytes.WithLabelValues(sessionID)

	for {
		msg, err := parser.ReadMessage()
		if err != nil {
			log.OnEvent(err.Error())
			return
		}

		messageCount.Inc()
		messageBytes.Add(float64(msg.Len()))

		msgIn <- fixIn{bytes: msg, receiveTime: parser.lastRead, pooled: true}
	}
}
