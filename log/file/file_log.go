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
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/appital/gofix"
	"github.com/appital/gofix/config"
)

type fileLog struct {
	eventLogger   *log.Logger
	messageLogger *log.Logger
	eventFile     *os.File
	messageFile   *os.File
}

// Close closes the log's files.
func (l fileLog) Close() error {
	return errors.Join(l.eventFile.Close(), l.messageFile.Close())
}

func (l fileLog) OnIncoming(msg []byte) {
	l.messageLogger.Print(string(msg))
}

func (l fileLog) OnOutgoing(msg []byte) {
	l.messageLogger.Print(string(msg))
}

func (l fileLog) OnEvent(msg string) {
	l.eventLogger.Print(msg)
}

func (l fileLog) OnEventf(format string, v ...any) {
	l.eventLogger.Printf(format, v...)
}

type fileLogFactory struct{}

// NewLogFactory creates an instance of LogFactory that writes messages and events to file.
// The location of global and session log files is configured via FileLogPath.
// The global log is written to the global FileLogPath, and each session's log
// to the FileLogPath in its settings.
func NewLogFactory() gofix.LogFactory {
	return fileLogFactory{}
}

func newFileLog(prefix string, logPath string) (fileLog, error) {
	l := fileLog{}

	eventLogName := path.Join(logPath, prefix+".event.current.log")
	messageLogName := path.Join(logPath, prefix+".messages.current.log")

	if err := os.MkdirAll(logPath, os.ModePerm); err != nil {
		return l, err
	}

	fileFlags := os.O_RDWR | os.O_CREATE | os.O_APPEND
	eventFile, err := os.OpenFile(eventLogName, fileFlags, os.ModePerm)
	if err != nil {
		return l, err
	}

	messageFile, err := os.OpenFile(messageLogName, fileFlags, os.ModePerm)
	if err != nil {
		_ = eventFile.Close()
		return l, err
	}

	l.eventFile = eventFile
	l.messageFile = messageFile
	logFlag := log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC
	l.eventLogger = log.New(eventFile, "", logFlag)
	l.messageLogger = log.New(messageFile, "", logFlag)

	return l, nil
}

func (f fileLogFactory) Create(settings *gofix.SessionSettings) (gofix.Log, error) {
	logPath, err := settings.Setting(config.FileLogPath)
	if err != nil {
		return nil, err
	}
	return newFileLog("GLOBAL", logPath)
}

func (f fileLogFactory) CreateSessionLog(sessionID gofix.SessionID, settings *gofix.SessionSettings) (gofix.Log, error) {
	logPath, err := settings.Setting(config.FileLogPath)
	if err != nil {
		return nil, err
	}

	prefix := sessionIDFilenamePrefix(sessionID)
	// An acceptor with DynamicSessions takes the SessionID from the peer, so
	// it must not be able to name a file outside logPath.
	if strings.ContainsAny(prefix, `/\`) || !filepath.IsLocal(prefix) {
		return nil, fmt.Errorf("session ID cannot be used in a file name: %q", prefix)
	}
	return newFileLog(prefix, logPath)
}
