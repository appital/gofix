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

package gofix_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/appital/gofix"
)

func TestFIXUTCTimestampWrite(t *testing.T) {
	ts := time.Date(2016, time.February, 8, 22, 7, 16, 954123123, time.UTC)

	var tests = []struct {
		precision gofix.TimestampPrecision
		val       []byte
	}{
		{gofix.Millis, []byte("20160208-22:07:16.954")},
		{gofix.Seconds, []byte("20160208-22:07:16")},
		{gofix.Micros, []byte("20160208-22:07:16.954123")},
		{gofix.Nanos, []byte("20160208-22:07:16.954123123")},
	}

	for _, test := range tests {
		var f gofix.FIXUTCTimestamp
		f.Time = ts
		f.Precision = test.precision

		b := f.Write()

		if !bytes.Equal(b, test.val) {
			t.Errorf("got %s; want %s", b, test.val)
		}
	}
}

func TestFIXUTCTimestampRead(t *testing.T) {
	var tests = []struct {
		timeStr           string
		expectedTime      time.Time
		expectedPrecision gofix.TimestampPrecision
	}{
		{"20160208-22:07:16.310", time.Date(2016, time.February, 8, 22, 7, 16, 310000000, time.UTC), gofix.Millis},
		{"20160208-22:07:16", time.Date(2016, time.February, 8, 22, 7, 16, 0, time.UTC), gofix.Seconds},
		{"20160208-22:07:16.123455", time.Date(2016, time.February, 8, 22, 7, 16, 123455000, time.UTC), gofix.Micros},
		{"20160208-22:07:16.954123123", time.Date(2016, time.February, 8, 22, 7, 16, 954123123, time.UTC), gofix.Nanos},
	}

	for _, test := range tests {
		var f gofix.FIXUTCTimestamp
		if err := f.Read([]byte(test.timeStr)); err != nil {
			t.Errorf("Unexpected error: %v", err)
		}

		if !f.Time.Equal(test.expectedTime) {
			t.Errorf("For Time expected %v got %v", test.expectedTime, f.Time)
		}

		if f.Precision != test.expectedPrecision {
			t.Errorf("For NoMillis expected %v got %v", test.expectedPrecision, f.Precision)
		}
	}
}
