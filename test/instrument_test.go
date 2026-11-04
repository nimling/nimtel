package test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/nimling/nimtel"
)

func recordProviders(t *testing.T) (*tracetest.SpanRecorder, *sdkmetric.ManualReader) {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	reader := sdkmetric.NewManualReader()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return spans, reader
}

func newQueryDB(t *testing.T) (*bun.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqldb, mock, err := sqlmock.New()
	require.NoError(t, err)
	db := bun.NewDB(sqldb, pgdialect.New())
	t.Cleanup(func() { db.Close() })
	db.AddQueryHook(nimtel.QueryHook("unit"))
	return db, mock
}

func TestQueryHookRecordsSpanUnderParentAndDuration(t *testing.T) {
	spans, reader := recordProviders(t)
	db, mock := newQueryDB(t)
	mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"one"}).AddRow(1))

	ctx, parent := otel.Tracer("unit").Start(context.Background(), "parent")
	var one int
	require.NoError(t, db.NewSelect().ColumnExpr("1").Scan(ctx, &one))
	parent.End()
	require.NoError(t, mock.ExpectationsWereMet())

	ended := spans.Ended()
	require.Len(t, ended, 2)
	query := ended[0]
	require.Equal(t, "SELECT", query.Name())
	require.Equal(t, trace.SpanKindClient, query.SpanKind())
	require.Equal(t, parent.SpanContext().SpanID(), query.Parent().SpanID())
	require.Contains(t, query.Attributes(), attribute.String("db.name", "unit"))
	require.Contains(t, query.Attributes(), attribute.String("db.operation", "SELECT"))
	require.Contains(t, query.Attributes(), attribute.String("db.system", "postgresql"))
	require.Contains(t, query.Attributes(), attribute.String("db.statement", "SELECT 1"))
	for _, kv := range query.Attributes() {
		require.NotContains(t, []attribute.Key{"code.filepath", "code.lineno", "code.function"}, kv.Key)
	}

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	var names []string
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			names = append(names, m.Name)
		}
	}
	require.Contains(t, names, "go.sql.query_timing")
}

func TestQueryHookSkipsRootQueries(t *testing.T) {
	spans, _ := recordProviders(t)
	db, mock := newQueryDB(t)
	mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"one"}).AddRow(1))

	var one int
	require.NoError(t, db.NewSelect().ColumnExpr("1").Scan(context.Background(), &one))
	require.NoError(t, mock.ExpectationsWereMet())
	require.Empty(t, spans.Ended())
}

func TestQueryHookCapsStatement(t *testing.T) {
	spans, _ := recordProviders(t)
	db, mock := newQueryDB(t)
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"one"}).AddRow(1))

	ctx, parent := otel.Tracer("unit").Start(context.Background(), "parent")
	var one string
	require.NoError(t, db.NewSelect().ColumnExpr("'"+strings.Repeat("ø", 2000)+"'").Scan(ctx, &one))
	parent.End()

	var stmt string
	for _, kv := range spans.Ended()[0].Attributes() {
		if kv.Key == "db.statement" {
			stmt = kv.Value.AsString()
		}
	}
	require.LessOrEqual(t, len(stmt), nimtel.StatementLimit)
	require.Greater(t, len(stmt), nimtel.StatementLimit-4)
	require.True(t, utf8.ValidString(stmt))
}

func TestQueryHookRecordsRowsAndErrors(t *testing.T) {
	spans, _ := recordProviders(t)
	db, mock := newQueryDB(t)
	mock.ExpectExec("UPDATE").WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectQuery("SELECT").WillReturnError(errors.New("boom"))

	ctx, parent := otel.Tracer("unit").Start(context.Background(), "parent")
	_, err := db.ExecContext(ctx, "UPDATE t SET a = 1")
	require.NoError(t, err)
	var one int
	require.Error(t, db.NewSelect().ColumnExpr("1").Scan(ctx, &one))
	parent.End()

	ended := spans.Ended()
	require.Len(t, ended, 3)
	require.Contains(t, ended[0].Attributes(), attribute.Int64("db.rows_affected", 3))
	require.Contains(t, ended[0].Attributes(), attribute.String("db.statement", "UPDATE t SET a = 1"))
	require.Equal(t, codes.Error, ended[1].Status().Code)
	require.Equal(t, "boom", ended[1].Status().Description)
	require.NotEmpty(t, ended[1].Events())
}

func TestTransportProducesClientSpan(t *testing.T) {
	spans, _ := recordProviders(t)

	var traceparent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: nimtel.Transport(nil)}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	res, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, res.Body.Close())

	ended := spans.Ended()
	require.Len(t, ended, 1)
	require.Equal(t, trace.SpanKindClient, ended[0].SpanKind())
	require.NotEmpty(t, traceparent)
	require.Contains(t, traceparent, ended[0].SpanContext().TraceID().String())
}
