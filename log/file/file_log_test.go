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

package file

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"testing"

	"github.com/appital/gofix"
	"github.com/appital/gofix/config"
)

// TestFileLog_CreateUsesGlobalSettings checks that the global log is written
// to the FileLogPath in the settings it is created with.
func TestFileLog_CreateUsesGlobalSettings(t *testing.T) {
	factory := NewLogFactory()

	if _, err := factory.Create(gofix.NewSessionSettings()); err == nil {
		t.Error("Should expect error when global settings have no file log path")
	}

	logDir := t.TempDir()
	settings := gofix.NewSessionSettings()
	settings.Set(config.FileLogPath, logDir)
	log, err := factory.Create(settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.(io.Closer).Close() })

	expected := path.Join(logDir, "GLOBAL.event.current.log")
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("%v should exist: %v", expected, err)
	}
}

type fileLogHelper struct {
	LogPath string
	Prefix  string
	Log     gofix.Log
}

func newFileLogHelper(t *testing.T) *fileLogHelper {
	prefix := "myprefix"
	logPath := path.Join(os.TempDir(), fmt.Sprintf("TestLogStore-%d", os.Getpid()))

	log, err := newFileLog(prefix, logPath)
	if err != nil {
		t.Error("Unexpected error", err)
	}

	return &fileLogHelper{
		LogPath: logPath,
		Prefix:  prefix,
		Log:     log,
	}
}

func TestNewFileLog(t *testing.T) {
	helper := newFileLogHelper(t)

	tests := []struct {
		expectedPath string
	}{
		{path.Join(helper.LogPath, fmt.Sprintf("%v.messages.current.log", helper.Prefix))},
		{path.Join(helper.LogPath, fmt.Sprintf("%v.event.current.log", helper.Prefix))},
	}

	for _, test := range tests {
		if _, err := os.Stat(test.expectedPath); os.IsNotExist(err) {
			t.Errorf("%v does not exist", test.expectedPath)
		}
	}
}

func TestFileLog_Append(t *testing.T) {
	helper := newFileLogHelper(t)

	messageLogFile, err := os.Open(path.Join(helper.LogPath, fmt.Sprintf("%v.messages.current.log", helper.Prefix)))
	if err != nil {
		t.Error("Unexpected error", err)
	}
	defer messageLogFile.Close()

	eventLogFile, err := os.Open(path.Join(helper.LogPath, fmt.Sprintf("%v.event.current.log", helper.Prefix)))
	if err != nil {
		t.Error("Unexpected error", err)
	}
	defer eventLogFile.Close()

	messageScanner := bufio.NewScanner(messageLogFile)
	eventScanner := bufio.NewScanner(eventLogFile)

	helper.Log.OnIncoming([]byte("incoming"))
	if !messageScanner.Scan() {
		t.Error("Unexpected EOF")
	}

	helper.Log.OnEvent("Event")
	if !eventScanner.Scan() {
		t.Error("Unexpected EOF")
	}

	newHelper := newFileLogHelper(t)
	newHelper.Log.OnIncoming([]byte("incoming"))
	if !messageScanner.Scan() {
		t.Error("Unexpected EOF")
	}

	newHelper.Log.OnEvent("Event")
	if !eventScanner.Scan() {
		t.Error("Unexpected EOF")
	}
}

// TestFileLog_CreateSessionLogUsesSessionSettings checks that a session's log
// is written to the FileLogPath in the settings it is created with.
func TestFileLog_CreateSessionLogUsesSessionSettings(t *testing.T) {
	factory := NewLogFactory()

	sessionDir := t.TempDir()
	sessionSettings := gofix.NewSessionSettings()
	sessionSettings.Set("FileLogPath", sessionDir)
	sessionID := gofix.SessionID{BeginString: "FIX.4.2", SenderCompID: "SENDER", TargetCompID: "ADDED"}
	if _, err := factory.CreateSessionLog(sessionID, sessionSettings); err != nil {
		t.Fatal(err)
	}
	expected := path.Join(sessionDir, sessionIDFilenamePrefix(sessionID)+".event.current.log")
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("%v should exist: %v", expected, err)
	}

	if _, err := factory.CreateSessionLog(sessionID, gofix.NewSessionSettings()); err == nil {
		t.Error("Should expect error when session settings have no file log path")
	}

	unsafe := gofix.SessionID{BeginString: "FIX.4.2", SenderCompID: "..", TargetCompID: "x/y"}
	if _, err := factory.CreateSessionLog(unsafe, sessionSettings); err == nil {
		t.Error("Should expect error for a SessionID that names a path")
	}
}

func TestFileLog_Close(t *testing.T) {
	settings := gofix.NewSessionSettings()
	settings.Set(config.FileLogPath, t.TempDir())
	factory := NewLogFactory()
	sessionID := gofix.SessionID{BeginString: "FIX.4.2", SenderCompID: "SENDER", TargetCompID: "TARGET"}
	log, err := factory.CreateSessionLog(sessionID, settings)
	if err != nil {
		t.Fatal(err)
	}

	closer, ok := log.(io.Closer)
	if !ok {
		t.Fatal("file log should implement io.Closer")
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := closer.Close(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("closing again should fail with os.ErrClosed, got %v", err)
	}
}
