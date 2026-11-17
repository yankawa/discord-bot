// Package logging provides JSON logs with a final token-redaction boundary.
package logging

import (
	"bytes"
	"io"
	"log/slog"
	"sync"
)

type redactingWriter struct {
	mu    sync.Mutex
	out   io.Writer
	token []byte
}

func (w *redactingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	safe := p
	if len(w.token) > 0 {
		safe = bytes.ReplaceAll(p, w.token, []byte("[redacted]"))
	}
	_, err := w.out.Write(safe)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func New(out io.Writer, token string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(&redactingWriter{out: out, token: []byte(token)}, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
