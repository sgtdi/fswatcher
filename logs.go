package fswatcher

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LogSeverity defines the logging verbosity
type LogSeverity int

// Logging levels
const (
	SeverityNone LogSeverity = iota
	SeverityError
	SeverityWarn
	SeverityInfo
	SeverityDebug
)

// String returns the string representation of a LogSeverity
func (l LogSeverity) String() string {
	switch l {
	case SeverityError:
		return "ERROR"
	case SeverityWarn:
		return "WARN"
	case SeverityInfo:
		return "INFO"
	case SeverityDebug:
		return "DEBUG"
	default:
		return "UNKNOWN"
	}
}

// Emoji returns a simple emoji for a LogSeverity
func (l LogSeverity) Emoji() string {
	switch l {
	case SeverityError:
		return "🚨"
	case SeverityWarn:
		return "⚠️"
	case SeverityInfo:
		return "ℹ️"
	case SeverityDebug:
		return "🐛"
	default:
		return "❓"
	}
}

// log handles leveled logging
func (w *watcher) log(level LogSeverity, format string, args ...any) {
	if level > w.severity {
		return
	}

	w.logMu.Lock()
	defer w.logMu.Unlock()

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	prefix := fmt.Sprintf("[%s] %s [%s]", timestamp, level.Emoji(), filepath.Base(w.path))
	msg := fmt.Sprintf(format, args...)

	logLine := fmt.Sprintf("%s %s", prefix, msg)

	// Write to a log file if configured
	if w.logger != nil {
		w.logger.Println(logLine)
	} else {
		// Route to stderr for errors and warnings
		switch level {
		case SeverityError, SeverityWarn:
			fmt.Fprintln(os.Stderr, logLine)
		default:
			fmt.Println(logLine)
		}
	}
}

// logError logs an Error level message
func (w *watcher) logError(format string, args ...any) {
	w.log(SeverityError, format, args...)
}

// logWarn logs a Warn level message
func (w *watcher) logWarn(format string, args ...any) {
	w.log(SeverityWarn, format, args...)
}

// logInfo logs an Info level message
func (w *watcher) logInfo(format string, args ...any) {
	w.log(SeverityInfo, format, args...)
}

// logDebug logs a Debug level message
func (w *watcher) logDebug(format string, args ...any) {
	w.log(SeverityDebug, format, args...)
}
