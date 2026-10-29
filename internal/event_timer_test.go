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

package internal

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestEventTimer_Stop_idempotent(*testing.T) {
	t := NewEventTimer(func() {})

	t.Stop()
	t.Stop()
}

// A timeout that is not positive stops the timer rather than running the task
// at once, which would make a session with HeartBtInt 0 send heartbeats
// continuously.
func TestEventTimer_ResetNotPositiveStops(t *testing.T) {
	var runs atomic.Int32
	timer := NewEventTimer(func() { runs.Add(1) })
	defer timer.Stop()

	timer.Reset(0)
	timer.Reset(-time.Second)
	timer.Reset(20 * time.Millisecond)
	timer.Reset(0)

	time.Sleep(60 * time.Millisecond)
	if n := runs.Load(); n != 0 {
		t.Fatalf("task ran %d times", n)
	}

	timer.Reset(time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	if n := runs.Load(); n != 1 {
		t.Fatalf("task ran %d times after a positive Reset, expected 1", n)
	}
}
