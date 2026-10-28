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
	"errors"
	"io"
	"math"
	"sync"
	"time"
)

const (
	defaultBufSize = 4096

	// defaultMaxMessageSize bounds an inbound frame unless a session sets
	// MaxMessageSize. It is deliberately generous for FIX traffic but small
	// enough that a peer cannot make the parser allocate without limit.
	defaultMaxMessageSize = 1 << 20

	// maxPooledRawBuffer is the largest buffer returned to rawBufferPool, so
	// that one outsized message does not stay pinned in the pool.
	maxPooledRawBuffer = 64 << 10
)

// errMessageTooLarge is returned when an inbound frame or its claimed
// BodyLength exceeds the parser's limit.
var errMessageTooLarge = errors.New("message exceeds maximum size")

// rawBufferPool holds the buffers inbound messages are read into. Only
// sessions with ReuseIncomingMessages enabled return buffers to it; otherwise
// every Get allocates, as before.
var rawBufferPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// releaseRawBuffer returns an inbound message buffer to rawBufferPool. The
// caller must not use buf, or anything parsed from it, afterwards.
func releaseRawBuffer(buf *bytes.Buffer) {
	if buf.Cap() <= maxPooledRawBuffer {
		rawBufferPool.Put(buf)
	}
}

type parser struct {
	// Buffer is a slice of bigBuffer.
	bigBuffer, buffer []byte
	reader            io.Reader
	lastRead          time.Time

	// maxMessageSize bounds the bytes a single frame may occupy. Zero means
	// unbounded, which callers should not use for untrusted peers.
	maxMessageSize int
}

func newParser(reader io.Reader) *parser {
	return newParserWithLimit(reader, defaultMaxMessageSize)
}

func newParserWithLimit(reader io.Reader, maxMessageSize int) *parser {
	return &parser{reader: reader, maxMessageSize: maxMessageSize}
}

func (p *parser) readMore() (int, error) {
	if len(p.buffer) == cap(p.buffer) {
		var newBuffer []byte
		switch {
		// Initialize the parser.
		case len(p.bigBuffer) == 0:
			p.bigBuffer = make([]byte, defaultBufSize)
			newBuffer = p.bigBuffer[0:0]

		// Shift buffer back to the start of bigBuffer.
		case 2*len(p.buffer) <= len(p.bigBuffer):
			newBuffer = p.bigBuffer[0:len(p.buffer)]

		// Reallocate big buffer with enough space to shift buffer.
		default:
			p.bigBuffer = make([]byte, 2*len(p.buffer))
			newBuffer = p.bigBuffer[0:len(p.buffer)]
		}

		copy(newBuffer, p.buffer)
		p.buffer = newBuffer
	}

	space := cap(p.buffer) - len(p.buffer)
	if p.maxMessageSize > 0 {
		remaining := p.maxMessageSize - len(p.buffer)
		if remaining <= 0 {
			return 0, errMessageTooLarge
		}
		if space > remaining {
			space = remaining
		}
	}

	n, e := p.reader.Read(p.buffer[len(p.buffer) : len(p.buffer)+space])
	p.lastRead = time.Now()
	p.buffer = p.buffer[:len(p.buffer)+n]
	return n, e
}

func (p *parser) findIndex(delim []byte) (int, error) {
	return p.findIndexAfterOffset(0, delim)
}

func (p *parser) findIndexAfterOffset(offset int, delim []byte) (int, error) {
	// The size limit is enforced by readMore, only when more input is needed.
	// The buffer may already hold more than the limit in whole messages, for
	// example when the acceptor lowers it to the session's after the Logon.
	for {
		if offset > len(p.buffer) {
			if n, err := p.readMore(); n == 0 && err != nil {
				return -1, err
			}

			continue
		}

		if index := bytes.Index(p.buffer[offset:], delim); index != -1 {
			return index + offset, nil
		}

		n, err := p.readMore()

		if n == 0 && err != nil {
			return -1, err
		}
	}
}

func (p *parser) findStart() (int, error) {
	return p.findIndex([]byte("8="))
}

func (p *parser) findEndAfterOffset(offset int) (int, error) {
	index, err := p.findIndexAfterOffset(offset, []byte("\00110="))
	if err != nil {
		return index, err
	}

	index, err = p.findIndexAfterOffset(index+1, []byte("\001"))
	if err != nil {
		return index, err
	}

	return index + 1, nil
}

func (p *parser) jumpLength() (int, error) {
	lengthIndex, err := p.findIndex([]byte("\x019="))
	if err != nil {
		return 0, err
	}

	lengthIndex += 3

	offset, err := p.findIndexAfterOffset(lengthIndex, []byte("\001"))
	if err != nil {
		return 0, err
	}

	if offset == lengthIndex {
		return 0, errors.New("no length given")
	}

	length, err := atoi(p.buffer[lengthIndex:offset])
	if err != nil {
		return length, err
	}

	if length <= 0 || length > math.MaxInt-offset {
		return length, errors.New("invalid length")
	}
	if p.maxMessageSize > 0 && length > p.maxMessageSize {
		return 0, errMessageTooLarge
	}

	return offset + length, nil
}

func (p *parser) ReadMessage() (msgBytes *bytes.Buffer, err error) {
	start, err := p.findStart()
	if err != nil {
		return
	}
	p.buffer = p.buffer[start:]

	index, err := p.jumpLength()
	if err != nil {
		return
	}

	index, err = p.findEndAfterOffset(index)
	if err != nil {
		return
	}
	if p.maxMessageSize > 0 && index > p.maxMessageSize {
		return nil, errMessageTooLarge
	}

	msgBytes = rawBufferPool.Get().(*bytes.Buffer)
	msgBytes.Reset()
	msgBytes.Write(p.buffer[:index])
	p.buffer = p.buffer[index:]

	return
}
