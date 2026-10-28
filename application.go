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
	"fmt"
	"reflect"
)

// Application is implemented by FIX applications to receive application
// messages from a session. FromApp is the only callback an application must
// implement.
//
// An Application may also implement any of the other callbacks: those in
// SessionHandler, to be told about session lifecycle events, and those in
// MessageInterceptor, to inspect, modify or veto messages as they cross the
// session. Each callback has its own interface, such as FromAdminInterceptor,
// so an application can implement only the ones it needs. The engine checks
// for them once, when it creates each session. Leaving one out has the same
// effect as implementing it with an empty method: inbound admin messages are
// accepted and every send goes ahead.
//
// NewAcceptor and NewInitiator return an error if the application has a method
// named after one of these callbacks whose signature does not match, since the
// engine would never call it. For example, an acceptor that authenticates
// Logons in FromAdmin, but declares FromAdmin to return error instead of
// MessageRejectError, would otherwise accept every Logon without checking it.
// A compile-time assertion for each interface the application is meant to
// implement catches the same mistake earlier:
//
//	var (
//		_ gofix.Application          = (*myApp)(nil)
//		_ gofix.SessionHandler       = (*myApp)(nil)
//		_ gofix.FromAdminInterceptor = (*myApp)(nil)
//	)
type Application interface {
	// FromApp notification of app message being received from target.
	FromApp(message *Message, sessionID SessionID) MessageRejectError
}

// OnCreateHandler is implemented by an Application that wants to be told when
// a session is created. It is optional; see Application.
type OnCreateHandler interface {
	// OnCreate notification of a session being created.
	OnCreate(sessionID SessionID)
}

// OnLogonHandler is implemented by an Application that wants to be told when
// a session logs on. It is optional; see Application.
type OnLogonHandler interface {
	// OnLogon notification of a session successfully logging on.
	OnLogon(sessionID SessionID)
}

// OnLogoutHandler is implemented by an Application that wants to be told when
// a session logs out. It is optional; see Application.
type OnLogoutHandler interface {
	// OnLogout notification of a session logging off or disconnecting.
	OnLogout(sessionID SessionID)
}

// SessionHandler is implemented by an Application that wants to be told about
// all session lifecycle events. It is optional; see Application.
type SessionHandler interface {
	OnCreateHandler
	OnLogonHandler
	OnLogoutHandler
}

// ToAdminInterceptor is implemented by an Application that wants to see, or
// modify, admin messages before they are sent. It is optional; see
// Application.
type ToAdminInterceptor interface {
	// ToAdmin notification of admin message being sent to target. The message
	// may be modified before it is sent, for example to add credentials to a
	// Logon.
	ToAdmin(message *Message, sessionID SessionID)
}

// FromAdminInterceptor is implemented by an Application that wants to see,
// or reject, admin messages as they are received, for example to authenticate
// Logons. It is optional; see Application.
type FromAdminInterceptor interface {
	// FromAdmin notification of admin message being received from target.
	// Returning a MessageRejectError rejects the message; for a Logon, this
	// refuses the logon.
	FromAdmin(message *Message, sessionID SessionID) MessageRejectError
}

// ToAppInterceptor is implemented by an Application that wants to see,
// modify or veto application messages before they are sent. It is optional;
// see Application.
type ToAppInterceptor interface {
	// ToApp notification of app message being sent to target. Returning an
	// error, such as ErrDoNotSend, stops the message from being sent.
	ToApp(message *Message, sessionID SessionID) error
}

// MessageInterceptor is implemented by an Application that wants to inspect,
// modify or veto messages as they cross the session: admin messages in both
// directions, and application messages being sent. It is optional; see
// Application.
type MessageInterceptor interface {
	ToAdminInterceptor
	FromAdminInterceptor
	ToAppInterceptor
}

// callbackInterfaces are the optional callback interfaces, one per method.
var callbackInterfaces = []reflect.Type{
	reflect.TypeFor[OnCreateHandler](),
	reflect.TypeFor[OnLogonHandler](),
	reflect.TypeFor[OnLogoutHandler](),
	reflect.TypeFor[ToAdminInterceptor](),
	reflect.TypeFor[FromAdminInterceptor](),
	reflect.TypeFor[ToAppInterceptor](),
}

// checkApplication returns an error if app has a method named after an
// optional callback that the engine would never call, because its signature
// does not match, or because it has a pointer receiver and app is not a
// pointer.
func checkApplication(app Application) error {
	t := reflect.TypeOf(app)
	if t == nil {
		return nil
	}
	for _, iface := range callbackInterfaces {
		name := iface.Method(0).Name
		if _, ok := t.MethodByName(name); ok {
			if !t.Implements(iface) {
				return fmt.Errorf("application %v has a %s method that does not match gofix.%s, so it would never be called", t, name, iface.Name())
			}
		} else if t.Kind() != reflect.Pointer {
			if _, ok := reflect.PointerTo(t).MethodByName(name); ok {
				return fmt.Errorf("application %v has a %s method with a pointer receiver, so it would never be called; pass a *%v", t, name, t)
			}
		}
	}
	return nil
}

// implementsOr returns app as a T if it implements T, and fallback if not.
func implementsOr[T any](app Application, fallback T) T {
	if v, ok := app.(T); ok {
		return v
	}
	return fallback
}

// splitSessionHandler is a SessionHandler made up of the callbacks an
// Application implements, with no-ops for the rest.
type splitSessionHandler struct {
	OnCreateHandler
	OnLogonHandler
	OnLogoutHandler
}

// splitMessageInterceptor is a MessageInterceptor made up of the callbacks an
// Application implements, with no-ops for the rest.
type splitMessageInterceptor struct {
	ToAdminInterceptor
	FromAdminInterceptor
	ToAppInterceptor
}

// noopSessionHandler stands in for the SessionHandler callbacks the
// Application does not implement.
type noopSessionHandler struct{}

func (noopSessionHandler) OnCreate(SessionID) {}
func (noopSessionHandler) OnLogon(SessionID)  {}
func (noopSessionHandler) OnLogout(SessionID) {}

// noopMessageInterceptor stands in for the MessageInterceptor callbacks the
// Application does not implement.
type noopMessageInterceptor struct{}

func (noopMessageInterceptor) ToAdmin(*Message, SessionID)                      {}
func (noopMessageInterceptor) FromAdmin(*Message, SessionID) MessageRejectError { return nil }
func (noopMessageInterceptor) ToApp(*Message, SessionID) error                  { return nil }
