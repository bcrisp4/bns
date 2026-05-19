// Package logging builds slog loggers from BNS config.
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"github.com/bcrisp4/bns/internal/config"
)

// New returns a slog.Logger configured per cfg, writing to w.
func New(cfg config.Logging, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.Level)}

	var h slog.Handler
	switch strings.ToLower(cfg.Format) {
	case "text":
		h = slog.NewTextHandler(w, opts)
	default:
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// QueryLog is the interface the resolver chain uses to emit per-query lines.
// When query logging is disabled, LogQuery is a no-op.
type QueryLog interface {
	LogQuery(attrs ...slog.Attr)
}

// QueryLogger returns a QueryLog that writes to w iff cfg.Enabled, else a no-op.
func QueryLogger(cfg config.QueryLog, w io.Writer) QueryLog {
	if !cfg.Enabled {
		return noopQuery{}
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	return &slogQuery{logger: slog.New(h)}
}

type noopQuery struct{}

func (noopQuery) LogQuery(_ ...slog.Attr) {}

type slogQuery struct{ logger *slog.Logger }

func (s *slogQuery) LogQuery(attrs ...slog.Attr) {
	s.logger.LogAttrs(context.Background(), slog.LevelInfo, "query", attrs...)
}
