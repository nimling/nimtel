package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	colorGreen  = "\033[32m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorReset  = "\033[0m"
)

type colorHandler struct {
	out       io.Writer
	addSource bool
	level     slog.Level
}

func (h *colorHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *colorHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *colorHandler) WithGroup(name string) slog.Handler       { return h }

func (h *colorHandler) Handle(_ context.Context, r slog.Record) error {
	color := levelColor(r.Level)
	level := fmt.Sprintf("%s%s%s", color, r.Level.String(), colorReset)
	time := r.Time.Format(time.RFC3339)

	line := fmt.Sprintf("time=%s level=%s msg=%q", time, level, r.Message)

	if h.addSource && r.PC != 0 {
		pwd, _ := os.Getwd()
		frames := runtime.CallersFrames([]uintptr{r.PC})
		f, _ := frames.Next()
		cleanFile, _ := strings.CutPrefix(f.File, pwd)

		line += fmt.Sprintf(" source=%s:%d", cleanFile, f.Line)
	}

	r.Attrs(func(a slog.Attr) bool {
		line += fmt.Sprintf(" %s=%v", a.Key, a.Value)
		return true
	})

	_, err := fmt.Fprintln(h.out, line)
	return err
}

func levelColor(level slog.Level) string {
	switch level {
	case slog.LevelDebug:
		return colorBlue
	case slog.LevelInfo:
		return colorGreen
	case slog.LevelWarn:
		return colorYellow
	case slog.LevelError:
		return colorRed
	default:
		return colorReset
	}
}
