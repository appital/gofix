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

package tape

import (
	"bytes"
	"math/rand"
	"strconv"
	"testing"
)

func TestGenIsDeterministic(t *testing.T) {
	for name, mix := range Mixes {
		a := Stream(Gen(42, 200, mix), 1, DefaultSession)
		b := Stream(Gen(42, 200, mix), 1, DefaultSession)

		if !bytes.Equal(a, b) {
			t.Errorf("mix %q: two Gen(42, 200) runs produced different streams", name)
		}
	}
}

func TestGenSeedChangesTape(t *testing.T) {
	a := Stream(Gen(1, 200, MixRealistic), 1, DefaultSession)
	b := Stream(Gen(2, 200, MixRealistic), 1, DefaultSession)

	if bytes.Equal(a, b) {
		t.Error("different seeds produced identical tapes")
	}
}

func TestGenRespectsMix(t *testing.T) {
	entries := Gen(7, 10000, MixRealistic)

	counts := map[string]int{}
	for _, e := range entries {
		counts[e.MsgType]++
	}

	for msgType, weight := range MixRealistic.Weights {
		// Weights sum to 100, so weight is also the expected percentage.
		got := float64(counts[msgType]) / float64(len(entries)) * 100
		if got < float64(weight)*0.8 || got > float64(weight)*1.2 {
			t.Errorf("MsgType %q: got %.1f%% of tape, want ~%d%%", msgType, got, weight)
		}
	}
}

// TestWireFraming checks the two header fields the parser relies on to frame a
// message off the wire: BodyLength and CheckSum.
func TestWireFraming(t *testing.T) {
	for _, e := range Gen(3, 500, MixRealistic) {
		msg := Wire(e, 1, DefaultSession, BaseTime)

		if !bytes.HasPrefix(msg, []byte("8=FIX.4.4\x01")) {
			t.Fatalf("message does not start with BeginString: %q", msg[:20])
		}
		if msg[len(msg)-1] != SOH {
			t.Fatalf("message does not end with SOH: %q", msg[len(msg)-8:])
		}

		bodyStart := bytes.Index(msg, []byte("\x0135=")) + 1
		checkSumStart := bytes.LastIndex(msg, []byte("\x0110=")) + 1

		wantLen := checkSumStart - bodyStart
		gotLen := bodyLengthField(t, msg)
		if gotLen != wantLen {
			t.Fatalf("BodyLength: got %d, want %d, msg %q", gotLen, wantLen, msg)
		}

		sum := 0
		for _, b := range msg[:checkSumStart] {
			sum += int(b)
		}
		want := formatCheckSum(sum % 256)
		got := string(msg[checkSumStart+3 : len(msg)-1])
		if got != want {
			t.Fatalf("CheckSum: got %q, want %q", got, want)
		}
	}
}

func TestWireGroupsLeadWithDelimiter(t *testing.T) {
	// The first tag of every group entry must be the template's first tag,
	// otherwise no FIX parser can find the entry boundaries.
	e := genParties(rand.New(rand.NewSource(1)), 3)
	got := string(appendGroup(nil, e))

	want := "453=3\x01"
	if !bytes.HasPrefix([]byte(got), []byte(want)) {
		t.Fatalf("group does not start with count tag: %q", got)
	}
	if bytes.Count([]byte(got), []byte("\x01448=")) != 3 {
		t.Fatalf("expected 3 PartyID delimiters, got %q", got)
	}
}

func TestFieldCountMatchesWire(t *testing.T) {
	const sessionFields = 5 // 35, 49, 56, 34, 52 added by Wire

	for _, e := range Gen(11, 300, MixRealistic) {
		msg := Wire(e, 1, DefaultSession, BaseTime)
		// 8, 9 and 10 bracket the body and are not counted by FieldCount.
		onWire := bytes.Count(msg, []byte{SOH}) - 3 - sessionFields

		if got := e.FieldCount(); got != onWire {
			t.Errorf("MsgType %q: FieldCount()=%d, wire has %d", e.MsgType, got, onWire)
		}
	}
}

func bodyLengthField(t *testing.T, msg []byte) int {
	t.Helper()

	start := bytes.Index(msg, []byte("\x019=")) + 3
	end := bytes.IndexByte(msg[start:], SOH) + start

	n, err := strconv.Atoi(string(msg[start:end]))
	if err != nil {
		t.Fatalf("parsing BodyLength: %v", err)
	}
	return n
}
