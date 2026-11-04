package test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"go.opentelemetry.io/contrib/instrumentation/host"
	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/nimling/nimtel"
)

func TestInitRequiresServiceName(t *testing.T) {
	_, err := nimtel.Init(context.Background(), nimtel.Config{})
	require.ErrorIs(t, err, nimtel.ErrServiceName)
}

func TestInitWithoutEndpointLogsOnly(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := nimtel.Init(context.Background(), nimtel.Config{ServiceName: "unit"})
	require.NoError(t, err)
	require.NotNil(t, slog.Default())
	require.NoError(t, shutdown(context.Background()))
}

func TestInitWithEndpointInstallsProviders(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	shutdown, err := nimtel.Init(context.Background(), nimtel.Config{ServiceName: "unit", Environment: "test"})
	require.NoError(t, err)

	_, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	require.True(t, ok)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		shutdown(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not return")
	}
}

func TestOrganizationSetsBaggage(t *testing.T) {
	root := uuid.New()
	owner := uuid.New()
	ctx := nimtel.Organization(context.Background(), root, owner)
	bag := baggage.FromContext(ctx)
	require.Equal(t, root.String(), bag.Member(nimtel.RootKey).Value())
	require.Equal(t, owner.String(), bag.Member(nimtel.OwnerKey).Value())
}

type metricsReceiver struct {
	collectormetrics.UnimplementedMetricsServiceServer
	mu            sync.Mutex
	scopes        []string
	names         []string
	temporalities []metricspb.AggregationTemporality
}

func (r *metricsReceiver) Export(_ context.Context, req *collectormetrics.ExportMetricsServiceRequest) (*collectormetrics.ExportMetricsServiceResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rm := range req.ResourceMetrics {
		for _, sm := range rm.ScopeMetrics {
			r.scopes = append(r.scopes, sm.Scope.GetName())
			for _, m := range sm.Metrics {
				r.names = append(r.names, m.Name)
				if sum := m.GetSum(); sum != nil && sum.IsMonotonic {
					r.temporalities = append(r.temporalities, sum.AggregationTemporality)
				}
			}
		}
	}
	return &collectormetrics.ExportMetricsServiceResponse{}, nil
}

func startMetricsReceiver(t *testing.T) *metricsReceiver {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	rec := &metricsReceiver{}
	collectormetrics.RegisterMetricsServiceServer(srv, rec)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+lis.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")
	return rec
}

func TestInitWithMetricsRegistersRuntimeAndHost(t *testing.T) {
	rec := startMetricsReceiver(t)
	shutdown, err := nimtel.Init(context.Background(), nimtel.Config{ServiceName: "unit"})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, shutdown(ctx))

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Contains(t, rec.scopes, otelruntime.ScopeName)
	require.Contains(t, rec.scopes, host.ScopeName)
}

func TestInitWithoutMetricsSkipsRuntimeAndHost(t *testing.T) {
	rec := startMetricsReceiver(t)
	shutdown, err := nimtel.Init(context.Background(), nimtel.Config{ServiceName: "unit", WithoutMetrics: true})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, shutdown(ctx))

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.NotContains(t, rec.scopes, otelruntime.ScopeName)
	require.NotContains(t, rec.scopes, host.ScopeName)
}

type httpReceiver struct {
	mu         sync.Mutex
	paths      []string
	attributes map[string]string
	spans      []string
}

func (r *httpReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, req.URL.Path)
	if req.URL.Path == "/v1/traces" {
		var msg collectortrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &msg); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, rs := range msg.ResourceSpans {
			for _, kv := range rs.Resource.GetAttributes() {
				r.attributes[kv.Key] = kv.Value.GetStringValue()
			}
			for _, ss := range rs.ScopeSpans {
				for _, span := range ss.Spans {
					r.spans = append(r.spans, span.Name)
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}

func TestInitHTTPProtobufExportsWithResource(t *testing.T) {
	rec := &httpReceiver{attributes: map[string]string{}}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	t.Setenv(nimtel.EndpointEnv, srv.URL)
	t.Setenv(nimtel.ProtocolEnv, nimtel.ProtocolHTTP)
	t.Setenv(nimtel.ReplicaEnv, "unit--rev1-abc")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "team=samna")
	t.Setenv(nimtel.SamplerEnv, "always_on")

	shutdown, err := nimtel.Init(context.Background(), nimtel.Config{ServiceName: "unit", ServiceVersion: "v1.2.3", Environment: "test"})
	require.NoError(t, err)

	_, span := otel.Tracer("unit").Start(context.Background(), "probe")
	span.End()
	slog.InfoContext(context.Background(), "probe")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, shutdown(ctx))

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Contains(t, rec.paths, "/v1/traces")
	require.Contains(t, rec.paths, "/v1/metrics")
	require.Contains(t, rec.paths, "/v1/logs")
	require.Contains(t, rec.spans, "probe")
	require.Equal(t, "unit", rec.attributes["service.name"])
	require.Equal(t, "v1.2.3", rec.attributes["service.version"])
	require.Equal(t, "unit--rev1-abc", rec.attributes["service.instance.id"])
	require.Equal(t, "test", rec.attributes[nimtel.EnvironmentKey])
	require.Equal(t, "samna", rec.attributes["team"])
}

func TestInitRejectsUnknownProtocol(t *testing.T) {
	t.Setenv(nimtel.EndpointEnv, "http://127.0.0.1:1")
	t.Setenv(nimtel.ProtocolEnv, "http/json")
	_, err := nimtel.Init(context.Background(), nimtel.Config{ServiceName: "unit"})
	require.ErrorIs(t, err, nimtel.ErrProtocol)
}

func shutdownWithin(t *testing.T, shutdown func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, shutdown(ctx))
}

func TestInitExportsDeltaTemporality(t *testing.T) {
	rec := startMetricsReceiver(t)
	shutdown, err := nimtel.Init(context.Background(), nimtel.Config{ServiceName: "unit"})
	require.NoError(t, err)
	shutdownWithin(t, shutdown)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.NotEmpty(t, rec.temporalities)
	for _, temporality := range rec.temporalities {
		require.Equal(t, metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA, temporality)
	}
}

func TestInitTemporalityFollowsEnv(t *testing.T) {
	rec := startMetricsReceiver(t)
	t.Setenv(nimtel.TemporalityEnv, "cumulative")
	shutdown, err := nimtel.Init(context.Background(), nimtel.Config{ServiceName: "unit"})
	require.NoError(t, err)
	shutdownWithin(t, shutdown)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.NotEmpty(t, rec.temporalities)
	for _, temporality := range rec.temporalities {
		require.Equal(t, metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE, temporality)
	}
}

func TestInitIntervalFollowsEnv(t *testing.T) {
	rec := startMetricsReceiver(t)
	t.Setenv(nimtel.IntervalEnv, "100")
	shutdown, err := nimtel.Init(context.Background(), nimtel.Config{ServiceName: "unit"})
	require.NoError(t, err)
	defer shutdownWithin(t, shutdown)

	require.Eventually(t, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.names) > 0
	}, 3*time.Second, 50*time.Millisecond)
}

func runQuery(t *testing.T) {
	t.Helper()
	sqldb, mock, err := sqlmock.New()
	require.NoError(t, err)
	mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"one"}).AddRow(1))
	db := bun.NewDB(sqldb, pgdialect.New())
	t.Cleanup(func() { db.Close() })
	db.AddQueryHook(nimtel.QueryHook("unit"))
	var one int
	require.NoError(t, db.NewSelect().ColumnExpr("1").Scan(context.Background(), &one))
}

func sqlMetrics(names []string) []string {
	var found []string
	for _, name := range names {
		if strings.HasPrefix(name, "go.sql.") || strings.HasPrefix(name, "db.") {
			found = append(found, name)
		}
	}
	return found
}

func TestInitDropsQueryMetrics(t *testing.T) {
	rec := startMetricsReceiver(t)
	shutdown, err := nimtel.Init(context.Background(), nimtel.Config{ServiceName: "unit", WithoutMetrics: true})
	require.NoError(t, err)
	runQuery(t)
	shutdownWithin(t, shutdown)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Empty(t, sqlMetrics(rec.names))
}

func TestInitQueryMetricsExportsTimingAndStats(t *testing.T) {
	rec := startMetricsReceiver(t)
	shutdown, err := nimtel.Init(context.Background(), nimtel.Config{ServiceName: "unit", WithoutMetrics: true, QueryMetrics: true})
	require.NoError(t, err)
	runQuery(t)
	shutdownWithin(t, shutdown)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Contains(t, rec.names, "go.sql.query_timing")
	require.Contains(t, rec.scopes, nimtel.StatsScope)
}

func sampledRoots(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(&httpReceiver{attributes: map[string]string{}})
	t.Cleanup(srv.Close)
	t.Setenv(nimtel.EndpointEnv, srv.URL)
	t.Setenv(nimtel.ProtocolEnv, nimtel.ProtocolHTTP)
	shutdown, err := nimtel.Init(context.Background(), nimtel.Config{ServiceName: "unit", WithoutMetrics: true})
	require.NoError(t, err)
	defer shutdownWithin(t, shutdown)

	sampled := 0
	for range 1000 {
		_, span := otel.Tracer("unit").Start(context.Background(), "root")
		if span.SpanContext().IsSampled() {
			sampled++
		}
		span.End()
	}
	return sampled
}

func TestInitSamplesRootTracesByRatio(t *testing.T) {
	sampled := sampledRoots(t)
	require.Greater(t, sampled, 40)
	require.Less(t, sampled, 200)
}

func TestInitSamplerFollowsEnv(t *testing.T) {
	t.Setenv(nimtel.SamplerEnv, "always_on")
	require.Equal(t, 1000, sampledRoots(t))
}

func TestInitSamplerFollowsParent(t *testing.T) {
	srv := httptest.NewServer(&httpReceiver{attributes: map[string]string{}})
	t.Cleanup(srv.Close)
	t.Setenv(nimtel.EndpointEnv, srv.URL)
	t.Setenv(nimtel.ProtocolEnv, nimtel.ProtocolHTTP)
	shutdown, err := nimtel.Init(context.Background(), nimtel.Config{ServiceName: "unit", WithoutMetrics: true})
	require.NoError(t, err)
	defer shutdownWithin(t, shutdown)

	parent := trace.ContextWithRemoteSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		SpanID:     trace.SpanID{1},
		TraceFlags: trace.FlagsSampled,
	}))
	_, span := otel.Tracer("unit").Start(parent, "child")
	require.True(t, span.SpanContext().IsSampled())
	span.End()
}
