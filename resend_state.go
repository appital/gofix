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

import "github.com/appital/gofix/internal"

type resendState struct {
	loggedOn
	// messageStash holds the messages received above the gap being resent,
	// by MsgSeqNum, to be processed once the resend catches up to them.
	messageStash map[int]*Message
	// stashBytes is the size of the messages in messageStash, and
	// stashFull is set once one has been dropped because it was full.
	stashBytes            int
	stashFull             bool
	currentResendRangeEnd int
	resendRangeEnd        int
}

// stash holds msg, received with MsgSeqNum seqNum above the gap being resent,
// unless MaxResendQueueMessages or MaxResendQueueBytes would be exceeded. A
// message that is dropped is not lost: once the resend catches up, the next
// message is too high again, and the session asks for the rest.
func (s resendState) stash(session *session, seqNum int, msg *Message) (resendState, error) {
	if s.messageStash == nil {
		s.messageStash = make(map[int]*Message)
	}

	size := messageSize(msg)
	held := s.messageStash[seqNum]
	if held != nil {
		size -= messageSize(held)
	}
	if held == nil && len(s.messageStash) >= session.MaxResendQueueMessages ||
		s.stashBytes+size > session.MaxResendQueueBytes {
		if !s.stashFull {
			session.log.OnEventf("Resend queue full (%d messages, %d bytes); dropping MsgSeqNum %d until the resend catches up",
				len(s.messageStash), s.stashBytes, seqNum)
			s.stashFull = true
		}
		return s, nil
	}

	kept, err := session.retainIncoming(msg)
	if err != nil {
		return s, err
	}
	s.messageStash[seqNum] = kept
	s.stashBytes += size
	return s, nil
}

func (s resendState) String() string { return "Resend" }

func (s resendState) Timeout(session *session, event internal.Event) (nextState sessionState) {
	nextState = inSession{}.Timeout(session, event)
	switch nextState.(type) {
	case inSession:
		nextState = s
	case pendingTimeout:
		// Wrap pendingTimeout in resend. prevents us falling back to inSession if recovering
		// from pendingTimeout.
		nextState = pendingTimeout{s}
	}

	return
}

func (s resendState) FixMsgIn(session *session, msg *Message) (nextState sessionState) {
	nextState = inSession{}.FixMsgIn(session, msg)

	if !nextState.IsLoggedOn() {
		return
	}
	// A message above the gap has been added to the stash.
	if stashed, ok := nextState.(resendState); ok {
		s = stashed
	}

	if s.currentResendRangeEnd != 0 && s.currentResendRangeEnd < session.store.NextTargetMsgSeqNum() {
		nextResendState, err := session.sendResendRequest(session.store.NextTargetMsgSeqNum(), s.resendRangeEnd)
		if err != nil {
			return handleStateError(session, err)
		}
		nextResendState.messageStash, nextResendState.stashBytes, nextResendState.stashFull = s.messageStash, s.stashBytes, s.stashFull
		return nextResendState
	}

	var gapFillFlag FIXBoolean
	if msg.Body.Has(tagGapFillFlag) {
		if err := msg.Body.GetField(tagGapFillFlag, &gapFillFlag); err != nil {
			return handleStateError(session, err)
		}
	}

	if bool(gapFillFlag) && s.currentResendRangeEnd != 0 && s.currentResendRangeEnd == session.store.NextTargetMsgSeqNum() {
		nextResendState, err := session.sendResendRequest(session.store.NextTargetMsgSeqNum(), s.resendRangeEnd)
		if err != nil {
			return handleStateError(session, err)
		}
		nextResendState.messageStash, nextResendState.stashBytes, nextResendState.stashFull = s.messageStash, s.stashBytes, s.stashFull
		return nextResendState
	}

	if s.resendRangeEnd >= session.store.NextTargetMsgSeqNum() {
		return s
	}

	for len(s.messageStash) > 0 {
		targetSeqNum := session.store.NextTargetMsgSeqNum()
		msg, ok := s.messageStash[targetSeqNum]
		if !ok {
			break
		}

		delete(s.messageStash, targetSeqNum)

		nextState = inSession{}.FixMsgIn(session, msg)
		if !nextState.IsLoggedOn() {
			return
		}
	}

	return
}

// messageSize is the size of msg as received, or as built if it was not
// parsed from bytes.
func messageSize(msg *Message) int {
	if msg.rawMessage != nil {
		return msg.rawMessage.Len()
	}
	return len(msg.String())
}
