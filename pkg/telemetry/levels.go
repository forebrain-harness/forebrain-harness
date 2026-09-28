package telemetry

import (
	"errors"
	"log"
	"sync"
)

// LevelLogger owns the common info/debug log pair used by interactive
// components. Both files use the same bounded rotation and sync policy.
type LevelLogger struct {
	mu        sync.Mutex
	infoLog   *log.Logger
	debugLog  *log.Logger
	infoFile  *File
	debugFile *File
}

func OpenLevelLogger(infoPath, debugPath string, options Options) (*LevelLogger, error) {
	options.SyncWrites = true
	infoFile, err := Open(infoPath, options)
	if err != nil {
		return nil, err
	}
	debugFile, err := Open(debugPath, options)
	if err != nil {
		_ = infoFile.Close()
		return nil, err
	}
	const flags = log.LstdFlags | log.Lmicroseconds
	return &LevelLogger{
		infoLog: log.New(infoFile, "", flags), debugLog: log.New(debugFile, "", flags),
		infoFile: infoFile, debugFile: debugFile,
	}, nil
}

func (l *LevelLogger) Infof(format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infoLog.Printf(format, args...)
}

func (l *LevelLogger) Debugf(format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.debugLog.Printf(format, args...)
}

func (l *LevelLogger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var errs []error
	if l.infoFile != nil {
		errs = append(errs, l.infoFile.Close())
		l.infoFile = nil
	}
	if l.debugFile != nil {
		errs = append(errs, l.debugFile.Close())
		l.debugFile = nil
	}
	return errors.Join(errs...)
}
