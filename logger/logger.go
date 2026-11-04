package logger

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/baggage"
)

func New(dest io.Writer, isDebug bool, handlers ...slog.Handler) *slog.Logger {
	var base slog.Handler
	if isDebug {
		base = &colorHandler{out: dest, level: slog.LevelDebug, addSource: true}
	} else {
		base = slog.NewJSONHandler(dest, &slog.HandlerOptions{Level: slog.LevelInfo, AddSource: true})
	}
	return slog.New(&fanout{next: append([]slog.Handler{base}, handlers...)})
}

func Err(err error) slog.Attr {
	return slog.Attr{
		Key:   "error",
		Value: slog.StringValue(err.Error()),
	}
}

type fanout struct {
	next []slog.Handler
}

func (f *fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.next {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f *fanout) Handle(ctx context.Context, r slog.Record) error {
	for _, m := range baggage.FromContext(ctx).Members() {
		r.AddAttrs(slog.String(m.Key(), m.Value()))
	}
	var first error
	for _, h := range f.next {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (f *fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.next))
	for i, h := range f.next {
		next[i] = h.WithAttrs(attrs)
	}
	return &fanout{next: next}
}

func (f *fanout) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(f.next))
	for i, h := range f.next {
		next[i] = h.WithGroup(name)
	}
	return &fanout{next: next}
}
