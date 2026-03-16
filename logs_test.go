package fswatcher

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

// mockHandler is a simple slog.Handler that writes to a buffer
type mockHandler struct {
	buf *bytes.Buffer
}

func (h *mockHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (h *mockHandler) Handle(ctx context.Context, r slog.Record) error {
	h.buf.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		h.buf.WriteString(" ")
		h.buf.WriteString(a.Key)
		h.buf.WriteString("=")
		h.buf.WriteString(a.Value.String())
		return true
	})
	h.buf.WriteString("\n")
	return nil
}

func (h *mockHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *mockHandler) WithGroup(name string) slog.Handler {
	return h
}

func TestLogger_Output(t *testing.T) {
	buf := &bytes.Buffer{}
	handler := &mockHandler{buf: buf}
	logger := slog.New(handler)

	w := &watcher{
		logger:   logger,
		severity: SeverityDebug,
	}

	t.Run("logInfo", func(t *testing.T) {
		buf.Reset()
		w.logInfo("info message", "key", "value")
		assert.Contains(t, buf.String(), "info message key=value")
	})

	t.Run("logError", func(t *testing.T) {
		buf.Reset()
		w.logError("error message", "err", "some error")
		assert.Contains(t, buf.String(), "error message err=some error")
	})

	t.Run("logWarn", func(t *testing.T) {
		buf.Reset()
		w.logWarn("warn message", "retry", 1)
		assert.Contains(t, buf.String(), "warn message retry=1")
	})

	t.Run("logDebug", func(t *testing.T) {
		buf.Reset()
		w.logDebug("debug message", "detail", true)
		assert.Contains(t, buf.String(), "debug message detail=true")
	})
}

func TestLogger_LevelFiltering(t *testing.T) {
	// Use a real TextHandler to test actual level filtering
	buf := &bytes.Buffer{}
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}
	handler := slog.NewTextHandler(buf, opts)
	logger := slog.New(handler)

	w := &watcher{
		logger:   logger,
		severity: SeverityInfo,
	}

	w.logInfo("should be logged")
	w.logDebug("should be ignored")

	assert.Contains(t, buf.String(), "level=INFO msg=\"should be logged\"")
	assert.NotContains(t, buf.String(), "should be ignored")
}
