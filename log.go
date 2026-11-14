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

// Log is a generic interface for logging FIX messages and events.
//
// A session log that holds resources, such as open files or a database
// connection, should also implement io.Closer. The engine closes a session's
// log when Reload removes or restarts the session, or discards a session it
// built for a Run or Reload that failed.
type Log interface {
	// OnIncoming log incoming fix message.
	OnIncoming([]byte)

	// OnOutgoing log outgoing fix message.
	OnOutgoing([]byte)

	// OnEvent log fix event.
	OnEvent(string)

	// OnEventf log fix event according to format specifier.
	OnEventf(string, ...any)
}

// The LogFactory interface creates global and session specific Log instances.
type LogFactory interface {
	// Create creates the global log, which records events that do not
	// belong to a session. settings is the global settings, and is never
	// nil when the engine calls Create. A factory
	// should read anything it needs for the global log, such as where to
	// write it, from settings.
	Create(settings *SessionSettings) (Log, error)

	// CreateSessionLog creates the log for the session sessionID. settings
	// is the session's effective settings: the global settings with the
	// session's own on top, or the global settings alone for a session an
	// acceptor creates for DynamicSessions. A factory should read anything
	// it needs for the session, such as where to write the log, from
	// settings, so that it can create logs for sessions added by Reload.
	CreateSessionLog(sessionID SessionID, settings *SessionSettings) (Log, error)
}
