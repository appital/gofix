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

	// resync makes ReadMessage ignore a malformed frame and look for the next
	// one. It is off for the acceptor's first message, where an invalid logon
	// must close the connection.
	resync bool

	// log, when set, is told about each malformed message resync skips.
	log Log
}

func newParser(reader io.Reader) *parser {
	return newParserWithLimit(reader, defaultMaxMessageSize)
}

func newParserWithLimit(reader io.Reader, maxMessageSize int) *parser {
	return &parser{reader: reader, maxMessageSize: maxMessageSize, resync: true}
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
	if offset < 0 {
		return -1, errors.New("invalid offset")
	}

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

// ensureBytes reads until the buffer holds at least n bytes, or the reader
// ends. The caller must check the returned error before indexing the buffer.
func (p *parser) ensureBytes(n int) error {
	for len(p.buffer) < n {
		read, err := p.readMore()
		if read == 0 && err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) findStart() (int, error) {
	// A BeginString can only start a message at a tag boundary, so skip an
	// "8=" that ends a longer tag such as 38=. Any other bytes before it, such
	// as a line break some engines send between messages, are ignored.
	offset := 0
	for {
		index, err := p.findIndexAfterOffset(offset, []byte("8="))
		if err != nil {
			return -1, err
		}
		if index == 0 || !isDecimal(p.buffer[index-1]) {
			return index, nil
		}
		offset = index + 1
	}
}

// jumpLength returns the index just past the CheckSum field of the message
// that starts at the beginning of the buffer. BodyLength determines that
// boundary exactly: the checksum must begin where BodyLength says it does.
func (p *parser) jumpLength() (int, error) {
	// BodyLength must be the second field. Searching further could find the
	// BodyLength of the next message and frame both as one.
	beginStringEnd, err := p.findIndex([]byte("\x01"))
	if err != nil {
		return 0, err
	}
	if err = p.ensureBytes(beginStringEnd + 3); err != nil {
		return 0, err
	}
	if !bytes.HasPrefix(p.buffer[beginStringEnd:], []byte("\x019=")) {
		return 0, errors.New("BodyLength must be the second field")
	}

	lengthIndex := beginStringEnd + 3

	offset, err := p.findIndexAfterOffset(lengthIndex, []byte("\x01"))
	if err != nil {
		return 0, err
	}

	if offset == lengthIndex {
		return 0, errors.New("no length given")
	}

	length, err := atoi(p.buffer[lengthIndex:offset])
	if err != nil {
		return 0, err
	}

	if length <= 0 || length > math.MaxInt-offset {
		return 0, errors.New("invalid length")
	}
	if p.maxMessageSize > 0 && length > p.maxMessageSize {
		return 0, errMessageTooLarge
	}

	// BodyLength counts from just after the BodyLength field through the SOH
	// that precedes CheckSum. That SOH must be exactly here, followed by 10=.
	checksumDelim := offset + length
	delim, err := p.findIndexAfterOffset(checksumDelim, []byte("\x01"))
	if err != nil {
		return 0, err
	}
	if delim != checksumDelim {
		return 0, errors.New("BodyLength does not end at a field delimiter")
	}

	if err = p.ensureBytes(checksumDelim + 4); err != nil {
		return 0, err
	}
	if !bytes.HasPrefix(p.buffer[checksumDelim+1:], []byte("10=")) {
		return 0, errors.New("BodyLength does not end at CheckSum")
	}

	end, err := p.findIndexAfterOffset(checksumDelim+4, []byte("\x01"))
	if err != nil {
		return 0, err
	}
	if end != checksumDelim+7 {
		return 0, errors.New("invalid CheckSum")
	}

	return end + 1, nil
}

func (p *parser) ReadMessage() (*bytes.Buffer, error) {
	for attempt := 0; ; attempt++ {
		start, err := p.findStart()
		if err != nil {
			return nil, err
		}
		p.buffer = p.buffer[start:]

		index, err := p.jumpLength()
		if err != nil {
			if !p.resync || errors.Is(err, errMessageTooLarge) || errors.Is(err, io.EOF) {
				return nil, err
			}
			if attempt >= maxResyncAttempts {
				return nil, errors.New("too many invalid messages")
			}

			// Ignore the malformed message and look for the next one, as FIX
			// requires. Skipping the claimed frame also drops a valid message
			// that a too-large BodyLength would otherwise swallow.
			skipped, skipErr := p.skipInvalidMessage()
			if skipErr != nil {
				return nil, skipErr
			}
			// The skipped bytes are not logged: they may hold credentials or
			// line breaks a peer chose.
			if p.log != nil {
				p.log.OnEventf("Invalid Message Skipped: %v (%d bytes)", err, skipped)
			}
			continue
		}
		if p.maxMessageSize > 0 && index > p.maxMessageSize {
			return nil, errMessageTooLarge
		}

		msgBytes := rawBufferPool.Get().(*bytes.Buffer)
		msgBytes.Reset()
		msgBytes.Write(p.buffer[:index])
		p.buffer = p.buffer[index:]

		return msgBytes, nil
	}
}

// maxResyncAttempts bounds how many malformed messages ReadMessage skips
// before it gives up and lets the connection be closed.
const maxResyncAttempts = 64

// skipInvalidMessage advances past a message whose framing is invalid and
// returns how many bytes it dropped. It uses the claimed BodyLength when the
// message has one as its second field, so a too-large length consumes the
// bytes it claims rather than exposing the start of the next message.
func (p *parser) skipInvalidMessage() (int, error) {
	skip := 2

	if index := bytes.IndexByte(p.buffer, '\x01'); index >= 0 && bytes.HasPrefix(p.buffer[index:], []byte("\x019=")) {
		lengthIndex := index + 3
		if end := bytes.IndexByte(p.buffer[lengthIndex:], '\x01'); end >= 0 {
			base := lengthIndex + end + 1
			if length, err := atoi(p.buffer[lengthIndex : lengthIndex+end]); err == nil && length > 0 && length < math.MaxInt-base {
				candidate := base + length
				if candidate > skip && (p.maxMessageSize <= 0 || candidate <= p.maxMessageSize) {
					skip = candidate
				}
			}
		}
	}

	// Leave the buffer at a field boundary so the next BeginString search
	// cannot match "8=" inside a value such as 38=100. Reading on also makes
	// the amount skipped independent of how the stream was split into reads.
	end, err := p.findIndexAfterOffset(skip, []byte("\x01"))
	if err != nil {
		if errors.Is(err, io.EOF) {
			skipped := len(p.buffer)
			p.buffer = p.buffer[:0]
			return skipped, nil
		}
		return 0, err
	}

	p.buffer = p.buffer[end+1:]
	return end + 1, nil
}
