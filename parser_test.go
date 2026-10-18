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
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

// BenchmarkParser_ReadMessage measures steady-state framing: one parser reading
// message after message from a stream that never runs dry, which is what a live
// connection looks like. Constructing a parser per iteration, as this benchmark
// used to, measured the parser's own allocation instead.
//
// See BenchmarkParserReadMessage in bench_parse_test.go for the same measurement
// over the realistic message corpus.
func BenchmarkParser_ReadMessage(b *testing.B) {
	stream := "8=FIXT.1.1\x019=111\x0135=D\x0134=4\x0149=TW\x0152=20140511-23:10:34\x0156=ISLD\x0111=ID\x0121=3\x0140=1\x0154=1\x0155=INTC\x0160=20140511-23:10:34\x0110=234\x018=FIXT.1.1\x019=95\x0135=D\x0134=5\x0149=TW\x0152=20140511-23:10:34\x0156=ISLD\x0111=ID\x0121=3\x0140=1\x0154=1\x0155=INTC\x0160=20140511-23:10:34\x0110=198\x01\x01"

	p := newParser(&loopReader{data: []byte(stream)})

	b.ReportAllocs()

	for b.Loop() {
		msg, err := p.ReadMessage()
		if err != nil {
			b.Fatal(err)
		}
		sinkInt = msg.Len()
	}
}

type ParserSuite struct {
	suite.Suite
	*parser
}

func TestParserSuite(t *testing.T) {
	suite.Run(t, new(ParserSuite))
}

func (s *ParserSuite) SetupTest() {
	s.parser = new(parser)
}

func (s *ParserSuite) TestInvalidNilLength() {
	stream := "8=\x019=\x01"
	s.reader = strings.NewReader(stream)
	_, err := s.ReadMessage()
	s.NotNil(err)
}

func (s *ParserSuite) TestOverflowLength() {
	stream := "8=\x019=9300000000000000000\x01"
	s.reader = strings.NewReader(stream)
	_, err := s.ReadMessage()
	s.NotNil(err)
}

func (s *ParserSuite) TestJumpLength() {
	stream := "8=FIXT.1.1\x019=111\x0135=D\x0134=4\x0149=TW\x0152=20140511-23:10:34\x0156=ISLD\x0111=ID\x0121=3\x0140=1\x0154=1\x0155=INTC\x0160=20140511-23:10:34\x0110=234\x018=FIXT.1.1\x019=95\x0135=D\x0134=5\x0149=TW\x0152=20140511-23:10:34\x0156=ISLD\x0111=ID\x0121=3\x0140=1\x0154=1\x0155=INTC\x0160=20140511-23:10:34\x0110=198\x01"

	s.reader = strings.NewReader(stream)
	index, err := s.parser.jumpLength()
	s.Nil(err)

	expectedIndex := 111 + 17 - 1
	s.Equal(expectedIndex, index)
}

func (s *ParserSuite) TestBadLength() {
	stream := "8=FIXT.1.1\x019=111\x0135=D\x0134=4\x0149=TW\x0152=20140511-23:10:34\x0156=ISLD\x0111=ID\x0121=3\x0140=1\x0154=1\x0155=INTC\x0160=20140511-23:10:34\x0110=234\x018=FIXT.1.1\x019=95\x0135=D\x0134=5\x0149=TW\x0152=20140511-23:10:34\x0156=ISLD\x0111=ID\x0121=3\x0140=1\x0154=1\x0155=INTC\x0160=20140511-23:10:34\x0110=198\x01"

	s.reader = strings.NewReader(stream)
	bytes, _ := s.parser.ReadMessage()

	s.Equal(stream, bytes.String())
}

func (s *ParserSuite) TestFindStart() {
	var testCases = []struct {
		stream        string
		expectError   bool
		expectedStart int
	}{
		{stream: "", expectError: true},
		{stream: "nostarthere", expectError: true},
		{stream: "hello8=FIX.4.0", expectedStart: 5},
	}
	for _, tc := range testCases {
		s.SetupTest()

		s.reader = strings.NewReader(tc.stream)

		start, err := s.parser.findStart()
		if tc.expectError {
			s.NotNil(err)
			continue
		}

		s.Nil(err)
		s.Equal(tc.expectedStart, start)
	}
}

func (s *ParserSuite) TestReadEOF() {
	var testCases = []struct {
		stream string
	}{
		{""},
		{"hello8=FIX.4.0"},
	}

	for _, tc := range testCases {
		s.SetupTest()

		s.reader = strings.NewReader(tc.stream)
		bytes, err := s.parser.ReadMessage()
		s.Nil(bytes)

		s.NotNil(err)
		s.Equal("EOF", err.Error())
	}
}

func (s *ParserSuite) TestReadMessage() {
	stream := "hello8=FIX.4.0\x019=5\x01blah\x0110=103\x018=FIX.4.0\x019=4\x01foo\x0110=103\x01"
	s.reader = strings.NewReader(stream)

	var testCases = []struct {
		expectedBytes     string
		expectedBufferLen int
		expectedBufferCap int
	}{
		{expectedBytes: "8=FIX.4.0\x019=5\x01blah\x0110=103\x01", expectedBufferCap: defaultBufSize - 31, expectedBufferLen: len(stream) - 31},
		{expectedBytes: "8=FIX.4.0\x019=4\x01foo\x0110=103\x01", expectedBufferCap: defaultBufSize - 31 - 25, expectedBufferLen: 0},
	}

	for _, tc := range testCases {
		msg, err := s.parser.ReadMessage()
		s.Nil(err)

		s.Equal(tc.expectedBytes, msg.String())
		s.Equal(tc.expectedBufferCap, cap(s.parser.buffer))
		s.Equal(tc.expectedBufferLen, len(s.parser.buffer))
	}
}

func (s *ParserSuite) TestReadMessageGrowBuffer() {
	stream := "hello8=FIX.4.0\x019=5\x01blah\x0110=103\x018=FIX.4.0\x019=4\x01foo\x0110=103\x01"

	var testCases = []struct {
		initialBufCap        int
		expectedBytes        string
		expectedBufferLen    int
		expectedBufferCap    int
		expectedBigBufferLen int
	}{
		{initialBufCap: 0, expectedBufferCap: defaultBufSize - 31, expectedBufferLen: len(stream) - 31, expectedBigBufferLen: defaultBufSize},
		{initialBufCap: 4, expectedBufferCap: 6, expectedBufferLen: 6, expectedBigBufferLen: 32},
		{initialBufCap: 8, expectedBufferCap: 6, expectedBufferLen: 6, expectedBigBufferLen: 32},
		{initialBufCap: 14, expectedBufferCap: 10, expectedBufferLen: 10, expectedBigBufferLen: 36},
		{initialBufCap: 16, expectedBufferCap: 18, expectedBufferLen: 18, expectedBigBufferLen: 44},
		{initialBufCap: 23, expectedBufferCap: 10, expectedBufferLen: 10, expectedBigBufferLen: 36},
		{initialBufCap: 30, expectedBufferCap: 24, expectedBufferLen: 24, expectedBigBufferLen: 50},
		{initialBufCap: 31, expectedBufferCap: 0, expectedBufferLen: 0, expectedBigBufferLen: 31},
		{initialBufCap: 40, expectedBufferCap: 9, expectedBufferLen: 9, expectedBigBufferLen: 40},
		{initialBufCap: 60, expectedBufferCap: 29, expectedBufferLen: 25, expectedBigBufferLen: 60},
		{initialBufCap: 80, expectedBufferCap: 49, expectedBufferLen: 25, expectedBigBufferLen: 80},
	}

	for _, tc := range testCases {
		s.SetupTest()

		s.reader = strings.NewReader(stream)
		s.parser.bigBuffer = make([]byte, tc.initialBufCap)
		s.parser.buffer = s.parser.bigBuffer[0:0]
		msg, err := s.parser.ReadMessage()

		s.Nil(err)
		s.Equal("8=FIX.4.0\x019=5\x01blah\x0110=103\x01", msg.String())
		s.Equal(tc.expectedBigBufferLen, len(s.parser.bigBuffer))
		s.Equal(tc.expectedBufferCap, cap(s.parser.buffer))
		s.Equal(tc.expectedBufferLen, len(s.parser.buffer))
	}
}
