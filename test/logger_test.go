package test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/baggage"

	"github.com/nimling/nimtel/logger"
)

type recorder struct {
	records []slog.Record
}

func (r *recorder) Enabled(context.Context, slog.Level) bool     { return true }
func (r *recorder) Handle(_ context.Context, rec slog.Record) error { r.records = append(r.records, rec); return nil }
func (r *recorder) WithAttrs([]slog.Attr) slog.Handler           { return r }
func (r *recorder) WithGroup(string) slog.Handler                { return r }

func attrs(r slog.Record) map[string]string {
	out := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		out[a.Key] = a.Value.String()
		return true
	})
	return out
}

func TestNewWritesJSON(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(&buf, false)
	log.Info("hello", logger.Err(errors.New("boom")))

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	require.Equal(t, "hello", line["msg"])
	require.Equal(t, "INFO", line["level"])
	require.Equal(t, "boom", line["error"])
	require.Contains(t, line, "source")
}

func TestNewDebugFiltersBelowInfoWhenNotDebug(t *testing.T) {
	var buf bytes.Buffer
	logger.New(&buf, false).Debug("hidden")
	require.Empty(t, buf.String())

	logger.New(&buf, true).Debug("shown")
	require.Contains(t, buf.String(), "shown")
}

func TestNewFansOutToExtraHandlers(t *testing.T) {
	var buf bytes.Buffer
	rec := &recorder{}
	logger.New(&buf, false, rec).Warn("both")

	require.Len(t, rec.records, 1)
	require.Equal(t, "both", rec.records[0].Message)
	require.Contains(t, buf.String(), "both")
}

func TestNewAddsBaggageAsAttributes(t *testing.T) {
	member, err := baggage.NewMember("samna.sa_root_id", "org-1")
	require.NoError(t, err)
	bag, err := baggage.New(member)
	require.NoError(t, err)
	ctx := baggage.ContextWithBaggage(context.Background(), bag)

	rec := &recorder{}
	logger.New(&bytes.Buffer{}, false, rec).InfoContext(ctx, "tagged")

	require.Len(t, rec.records, 1)
	require.Equal(t, "org-1", attrs(rec.records[0])["samna.sa_root_id"])
}
