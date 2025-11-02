package fswatcher

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LogLevel defines the logging verbosity
type LogLevel int

// Logging levels
const (
	LogLevelNone LogLevel = iota
	LogLevelError
	LogLevelWarn
	LogLevelInfo
	LogLevelDebug
)

// String returns the string representation of a LogLevel
func (l LogLevel) String() string {
	switch l {
	case LogLevelError:
		return "ERROR"
	case LogLevelWarn:
		return "WARN"
	case LogLevelInfo:
		return "INFO"
	case LogLevelDebug:
		return "DEBUG"
	default:
		return "UNKNOWN"
	}
}

// Emoji returns a simple emoji for a LogLevel
func (l LogLevel) Emoji() string {
	switch l {
	case LogLevelError:
		return "🚨"
	case LogLevelWarn:
		return "⚠️"
	case LogLevelInfo:
		return "ℹ️"
	case LogLevelDebug:
		return "🐛"
	default:
		return "❓"
	}
}

// log handles leveled logging
func (w *watcher) log(level LogLevel, format string, args ...any) {
	if level > w.logLevel {
		return
	}

	w.logMu.Lock()
	defer w.logMu.Unlock()

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	prefix := fmt.Sprintf("[%s] %s [%s]", timestamp, level.Emoji(), filepath.Base(w.path))
	msg := fmt.Sprintf(format, args...)

	logLine := fmt.Sprintf("%s %s", prefix, msg)

	// Route to stderr for errors and warnings
	switch level {
	case LogLevelError, LogLevelWarn:
		fmt.Fprintln(os.Stderr, logLine)
	default:
		fmt.Println(logLine)
	}

	// Write to a log file if configured
	if w.logger != nil {
		w.logger.Println(logLine)
	}
}

// logError logs an Error level message
func (w *watcher) logError(format string, args ...any) {
	w.log(LogLevelError, format, args...)
}

// logWarn logs a Warn level message
func (w *watcher) logWarn(format string, args ...any) {
	w.log(LogLevelWarn, format, args...)
}

// logInfo logs an Info level message
func (w *watcher) logInfo(format string, args ...any) {
	w.log(LogLevelInfo, format, args...)
}

// logDebug logs a Debug level message
func (w *watcher) logDebug(format string, args ...any) {
	w.log(LogLevelDebug, format, args...)
}
