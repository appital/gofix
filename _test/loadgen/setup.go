package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"strconv"

	"github.com/quickfixgo/quickfix"
	"github.com/quickfixgo/quickfix/config"
	"github.com/quickfixgo/quickfix/store/file"
)

const (
	serverCompID = "LOADGEN_SERVER"
	clientPrefix = "LOADGEN_CLIENT_"
)

func sessionSettings(opts options, i int, sender, target string) *quickfix.SessionSettings {
	s := quickfix.NewSessionSettings()
	s.Set(config.BeginString, quickfix.BeginStringFIX44)
	s.Set(config.SenderCompID, sender)
	s.Set(config.TargetCompID, target)
	s.Set(config.HeartBtInt, "30")
	s.Set(config.ResetOnLogon, "Y")
	s.Set(config.PersistMessages, boolSetting(opts.persist))

	if opts.validate {
		s.Set(config.DataDictionary, dataDictionaryPath())
	}

	return s
}

func boolSetting(v bool) string {
	if v {
		return "Y"
	}
	return "N"
}

// dataDictionaryPath resolves spec/FIX44.xml relative to this source file, so
// the binary runs from any working directory.
func dataDictionaryPath() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "spec/FIX44.xml"
	}
	// _test/loadgen/setup.go -> repo root
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "spec", "FIX44.xml")
}

func startAcceptor(opts options, port int, app quickfix.Application, counter *byteCounter) (*quickfix.Acceptor, error) {
	settings := quickfix.NewSettings()
	settings.GlobalSettings().Set(config.SocketAcceptPort, strconv.Itoa(port))
	settings.GlobalSettings().Set(config.SocketAcceptHost, "127.0.0.1")

	for i := 0; i < opts.sessions; i++ {
		s := sessionSettings(opts, i, serverCompID, clientCompID(i))
		if _, err := settings.AddSession(s); err != nil {
			return nil, fmt.Errorf("adding acceptor session %d: %w", i, err)
		}
	}

	storeFactory, err := storeFactoryFor(opts, settings, "acceptor")
	if err != nil {
		return nil, err
	}

	acceptor, err := quickfix.NewAcceptor(app, storeFactory, settings, countingLogFactory{counter})
	if err != nil {
		return nil, fmt.Errorf("creating acceptor: %w", err)
	}
	if err := acceptor.Start(); err != nil {
		return nil, fmt.Errorf("starting acceptor: %w", err)
	}

	return acceptor, nil
}

func startInitiator(opts options, port int, app quickfix.Application, counter *byteCounter) (*quickfix.Initiator, error) {
	settings := quickfix.NewSettings()
	settings.GlobalSettings().Set(config.SocketConnectHost, "127.0.0.1")
	settings.GlobalSettings().Set(config.SocketConnectPort, strconv.Itoa(port))
	settings.GlobalSettings().Set(config.ReconnectInterval, "1")

	for i := 0; i < opts.sessions; i++ {
		s := sessionSettings(opts, i, clientCompID(i), serverCompID)
		if _, err := settings.AddSession(s); err != nil {
			return nil, fmt.Errorf("adding initiator session %d: %w", i, err)
		}
	}

	storeFactory, err := storeFactoryFor(opts, settings, "initiator")
	if err != nil {
		return nil, err
	}

	initiator, err := quickfix.NewInitiator(app, storeFactory, settings, countingLogFactory{counter})
	if err != nil {
		return nil, fmt.Errorf("creating initiator: %w", err)
	}
	if err := initiator.Start(); err != nil {
		return nil, fmt.Errorf("starting initiator: %w", err)
	}

	return initiator, nil
}

func clientCompID(i int) string { return clientPrefix + strconv.Itoa(i) }

func storeFactoryFor(opts options, settings *quickfix.Settings, side string) (quickfix.MessageStoreFactory, error) {
	switch opts.store {
	case "memory", "":
		return quickfix.NewMemoryStoreFactory(), nil

	case "file":
		dir, err := os.MkdirTemp("", "quickfix-loadgen-"+side+"-")
		if err != nil {
			return nil, fmt.Errorf("creating file store dir: %w", err)
		}
		settings.GlobalSettings().Set(config.FileStorePath, dir)

		return file.NewStoreFactory(settings), nil
	}

	return nil, fmt.Errorf("unknown store %q", opts.store)
}

// profileSet holds the profiles a run has started, so they can all be closed
// together at the end of the measurement window.
type profileSet struct {
	opts  options
	files []*os.File
	cpu   bool
	trace bool
}

func startProfiling(opts options) (*profileSet, error) {
	ps := &profileSet{opts: opts}

	if opts.cpuProfile != "" {
		f, err := ps.create(opts.cpuProfile)
		if err != nil {
			return nil, err
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			return nil, fmt.Errorf("starting CPU profile: %w", err)
		}
		ps.cpu = true
	}

	if opts.tracePath != "" {
		f, err := ps.create(opts.tracePath)
		if err != nil {
			return nil, err
		}
		if err := trace.Start(f); err != nil {
			return nil, fmt.Errorf("starting trace: %w", err)
		}
		ps.trace = true
	}

	if opts.blockProfile != "" {
		runtime.SetBlockProfileRate(1000)
	}
	if opts.mutexProfile != "" {
		runtime.SetMutexProfileFraction(100)
	}

	return ps, nil
}

func (ps *profileSet) create(path string) (*os.File, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", dir, err)
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("creating %s: %w", path, err)
	}
	ps.files = append(ps.files, f)

	return f, nil
}

func (ps *profileSet) stop() error {
	if ps.cpu {
		pprof.StopCPUProfile()
	}
	if ps.trace {
		trace.Stop()
	}

	if err := ps.writeNamed(ps.opts.memProfile, "allocs"); err != nil {
		return err
	}
	if err := ps.writeNamed(ps.opts.blockProfile, "block"); err != nil {
		return err
	}
	if err := ps.writeNamed(ps.opts.mutexProfile, "mutex"); err != nil {
		return err
	}

	for _, f := range ps.files {
		if err := f.Close(); err != nil {
			return err
		}
	}

	if ps.opts.blockProfile != "" {
		runtime.SetBlockProfileRate(0)
	}
	if ps.opts.mutexProfile != "" {
		runtime.SetMutexProfileFraction(0)
	}

	return nil
}

func (ps *profileSet) writeNamed(path, profile string) error {
	if path == "" {
		return nil
	}

	f, err := ps.create(path)
	if err != nil {
		return err
	}

	// The heap profile is only meaningful after the allocations it describes
	// have been accounted for.
	if profile == "allocs" {
		runtime.GC()
	}

	if err := pprof.Lookup(profile).WriteTo(f, 0); err != nil {
		return fmt.Errorf("writing %s profile: %w", profile, err)
	}

	return nil
}
