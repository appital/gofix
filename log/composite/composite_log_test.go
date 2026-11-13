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

package composite

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/appital/gofix"
	"github.com/appital/gofix/log/file"
	"github.com/appital/gofix/log/screen"
	"github.com/appital/gofix/log/sql"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// CompositeLogTestSuite runs tests for the composite impl of Log.
type CompositeLogTestSuite struct {
	suite.Suite
	sqlLogRootPath string
	settings       *gofix.Settings
	sessionID      gofix.SessionID
}

func (suite *CompositeLogTestSuite) SetupTest() {
	// create settings
	sessionID := gofix.SessionID{BeginString: "FIX.4.4", SenderCompID: "SENDER", TargetCompID: "TARGET"}
	logPath := path.Join(os.TempDir(), fmt.Sprintf("TestLogStore-%d", os.Getpid()))
	suite.sqlLogRootPath = path.Join(os.TempDir(), fmt.Sprintf("SQLLogTestSuite-%d", os.Getpid()))
	err := os.MkdirAll(suite.sqlLogRootPath, os.ModePerm)
	require.Nil(suite.T(), err)
	sqlDriver := "sqlite3"
	sqlDsn := path.Join(suite.sqlLogRootPath, fmt.Sprintf("%d.db", time.Now().UnixNano()))

	settings, err := gofix.ParseSettings(strings.NewReader(fmt.Sprintf(`
[DEFAULT]
FileLogPath=%s
SQLLogDriver=%s
SQLLogDataSourceName=%s
SQLLogConnMaxLifetime=14400s

[SESSION]
BeginString=%s
SenderCompID=%s
TargetCompID=%s`, logPath, sqlDriver, sqlDsn, sessionID.BeginString, sessionID.SenderCompID, sessionID.TargetCompID)))
	require.Nil(suite.T(), err)

	suite.sessionID = sessionID
	suite.settings = settings
}

func (suite *CompositeLogTestSuite) TestCreateLogNoSession() {

	screenLogFactory := screen.NewLogFactory()
	sqlLogFactory := sql.NewLogFactory()
	// create log
	_, err := NewLogFactory([]gofix.LogFactory{screenLogFactory, sqlLogFactory}).Create(suite.settings.GlobalSettings())
	require.Nil(suite.T(), err)
}

func (suite *CompositeLogTestSuite) TestCreateLogSession() {

	screenLogFactory := screen.NewLogFactory()
	fileLogFactory := file.NewLogFactory()

	// create log
	_, err := NewLogFactory([]gofix.LogFactory{screenLogFactory, fileLogFactory}).CreateSessionLog(suite.sessionID, suite.settings.SessionSettings()[suite.sessionID])
	require.Nil(suite.T(), err)
}

func (suite *CompositeLogTestSuite) TearDownTest() {
	os.RemoveAll(suite.sqlLogRootPath)
}

func TestCompositeLogTestSuite(t *testing.T) {
	suite.Run(t, new(CompositeLogTestSuite))
}

// recordingLog records the events it is handed so a test can assert on what the
// composite log forwarded to its wrapped logs.
type recordingLog struct {
	events []string
}

func (r *recordingLog) OnIncoming([]byte) {}

func (r *recordingLog) OnOutgoing([]byte) {}

func (r *recordingLog) OnEvent(s string) { r.events = append(r.events, s) }

func (r *recordingLog) OnEventf(format string, a ...any) {
	r.events = append(r.events, fmt.Sprintf(format, a...))
}

// recordingLogFactory hands out one pre-built recordingLog, and records the
// settings the global log was created with.
type recordingLogFactory struct {
	log            *recordingLog
	globalSettings **gofix.SessionSettings
}

func (f recordingLogFactory) Create(settings *gofix.SessionSettings) (gofix.Log, error) {
	if f.globalSettings != nil {
		*f.globalSettings = settings
	}
	return f.log, nil
}

func (f recordingLogFactory) CreateSessionLog(gofix.SessionID, *gofix.SessionSettings) (gofix.Log, error) {
	return f.log, nil
}

func TestCompositeLogOnEventfExpandsFormatArgs(t *testing.T) {
	tests := []struct {
		name     string
		format   string
		args     []any
		expected string
	}{
		{"single string arg", "session %s", []any{"FIX.4.4"}, "session FIX.4.4"},
		{"mixed args", "session %s seq %d", []any{"FIX.4.4", 7}, "session FIX.4.4 seq 7"},
		{"slice arg", "tags %v count %d", []any{[]int{7, 9}, 2}, "tags [7 9] count 2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, second := &recordingLog{}, &recordingLog{}
			l, err := NewLogFactory([]gofix.LogFactory{
				recordingLogFactory{log: first},
				recordingLogFactory{log: second},
			}).Create(nil)
			require.NoError(t, err)

			l.OnEventf(tt.format, tt.args...)

			require.Equal(t, []string{tt.expected}, first.events)
			require.Equal(t, []string{tt.expected}, second.events)
		})
	}
}

// closingLog is a log that records whether it has been closed.
type closingLog struct {
	recordingLog
	closed bool
	err    error
}

func (l *closingLog) Close() error {
	l.closed = true
	return l.err
}

func TestCloseClosesLogs(t *testing.T) {
	failure := errors.New("cannot close")
	first, second := &closingLog{}, &closingLog{err: failure}
	l, err := NewLogFactory([]gofix.LogFactory{
		recordingLogFactory{log: &first.recordingLog},
		closingLogFactory{first},
		recordingLogFactory{log: &recordingLog{}},
		closingLogFactory{second},
	}).CreateSessionLog(gofix.SessionID{}, nil)
	require.NoError(t, err)

	closer, ok := l.(io.Closer)
	require.True(t, ok, "composite log should implement io.Closer")
	require.ErrorIs(t, closer.Close(), failure)
	require.True(t, first.closed)
	require.True(t, second.closed, "every log should be closed even if one fails")
}

type closingLogFactory struct {
	log *closingLog
}

func (f closingLogFactory) Create(*gofix.SessionSettings) (gofix.Log, error) { return f.log, nil }

func (f closingLogFactory) CreateSessionLog(gofix.SessionID, *gofix.SessionSettings) (gofix.Log, error) {
	return f.log, nil
}

// failingLogFactory fails to create any log.
type failingLogFactory struct {
	err error
}

func (f failingLogFactory) Create(*gofix.SessionSettings) (gofix.Log, error) { return nil, f.err }

func (f failingLogFactory) CreateSessionLog(gofix.SessionID, *gofix.SessionSettings) (gofix.Log, error) {
	return nil, f.err
}

// TestCreateClosesLogsOnFailure checks that when one factory fails, the logs
// the earlier factories created are closed rather than leaked.
func TestCreateClosesLogsOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		create func(gofix.LogFactory) (gofix.Log, error)
	}{
		{name: "Create", create: func(f gofix.LogFactory) (gofix.Log, error) {
			return f.Create(gofix.NewSessionSettings())
		}},
		{name: "CreateSessionLog", create: func(f gofix.LogFactory) (gofix.Log, error) {
			return f.CreateSessionLog(gofix.SessionID{}, gofix.NewSessionSettings())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := errors.New("cannot create")
			opened := &closingLog{}
			l, err := tc.create(NewLogFactory([]gofix.LogFactory{
				closingLogFactory{opened},
				failingLogFactory{failure},
			}))
			require.ErrorIs(t, err, failure)
			require.Nil(t, l)
			require.True(t, opened.closed, "logs created before the failure should be closed")
		})
	}
}

func TestCreatePassesGlobalSettings(t *testing.T) {
	settings := gofix.NewSessionSettings()
	var first, second *gofix.SessionSettings
	_, err := NewLogFactory([]gofix.LogFactory{
		recordingLogFactory{log: &recordingLog{}, globalSettings: &first},
		recordingLogFactory{log: &recordingLog{}, globalSettings: &second},
	}).Create(settings)
	require.NoError(t, err)

	require.Same(t, settings, first)
	require.Same(t, settings, second)
}
