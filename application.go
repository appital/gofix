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

// Application is implemented by FIX applications to receive application
// messages from a session. FromApp is the only callback an application must
// implement.
//
// An Application may also implement SessionHandler, to be told about session
// lifecycle events, and MessageInterceptor, to inspect, modify or veto messages
// as they cross the session. The engine checks for these once, when it creates
// each session. Leaving one out has the same effect as implementing it with
// empty methods: inbound admin messages are accepted and every send goes ahead.
//
// Because these interfaces are optional, a method whose signature does not
// match exactly still compiles, but the engine never calls it. For example, an
// acceptor that authenticates Logons in FromAdmin, but declares FromAdmin to
// return error instead of MessageRejectError, would accept every Logon without
// checking it. Guard against this with a compile-time assertion for each
// interface the application is meant to implement, using the same type that is
// passed to NewAcceptor or NewInitiator:
//
//	var (
//		_ gofix.Application        = (*myApp)(nil)
//		_ gofix.SessionHandler     = (*myApp)(nil)
//		_ gofix.MessageInterceptor = (*myApp)(nil)
//	)
type Application interface {
	// FromApp notification of app message being received from target.
	FromApp(message *Message, sessionID SessionID) MessageRejectError
}

// SessionHandler is implemented by an Application that wants to be told about
// session lifecycle events. It is optional; see Application.
type SessionHandler interface {
	// OnCreate notification of a session being created.
	OnCreate(sessionID SessionID)

	// OnLogon notification of a session successfully logging on.
	OnLogon(sessionID SessionID)

	// OnLogout notification of a session logging off or disconnecting.
	OnLogout(sessionID SessionID)
}

// MessageInterceptor is implemented by an Application that wants to inspect,
// modify or veto messages as they cross the session: admin messages in both
// directions, and application messages being sent. It is optional; see
// Application.
type MessageInterceptor interface {
	// ToAdmin notification of admin message being sent to target. The message
	// may be modified before it is sent, for example to add credentials to a
	// Logon.
	ToAdmin(message *Message, sessionID SessionID)

	// FromAdmin notification of admin message being received from target.
	// Returning a MessageRejectError rejects the message; for a Logon, this
	// refuses the logon.
	FromAdmin(message *Message, sessionID SessionID) MessageRejectError

	// ToApp notification of app message being sent to target. Returning an
	// error, such as ErrDoNotSend, stops the message from being sent.
	ToApp(message *Message, sessionID SessionID) error
}

// noopSessionHandler stands in for a SessionHandler the Application does not
// implement.
type noopSessionHandler struct{}

func (noopSessionHandler) OnCreate(SessionID) {}
func (noopSessionHandler) OnLogon(SessionID)  {}
func (noopSessionHandler) OnLogout(SessionID) {}

// noopMessageInterceptor stands in for a MessageInterceptor the Application
// does not implement.
type noopMessageInterceptor struct{}

func (noopMessageInterceptor) ToAdmin(*Message, SessionID)                      {}
func (noopMessageInterceptor) FromAdmin(*Message, SessionID) MessageRejectError { return nil }
func (noopMessageInterceptor) ToApp(*Message, SessionID) error                  { return nil }
