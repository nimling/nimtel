package nimtel

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	"github.com/uptrace/bun/extra/bunotel"
	"github.com/uptrace/bun/schema"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/host"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	dbconv "go.opentelemetry.io/otel/semconv/v1.20.0"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/nimling/nimtel/logger"
)

const (
	RootKey  = "samna.sa_root_id"
	OwnerKey = "samna.sa_owner_id"

	EnvironmentKey = "deployment.environment.name"

	EndpointEnv = "OTEL_EXPORTER_OTLP_ENDPOINT"
	ProtocolEnv = "OTEL_EXPORTER_OTLP_PROTOCOL"
	ReplicaEnv  = "CONTAINER_APP_REPLICA_NAME"

	SamplerEnv     = "OTEL_TRACES_SAMPLER"
	IntervalEnv    = "OTEL_METRIC_EXPORT_INTERVAL"
	TemporalityEnv = "OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE"

	ProtocolGRPC = "grpc"
	ProtocolHTTP = "http/protobuf"

	SampleRatio    = 0.1
	ExportInterval = 5 * time.Minute
	StatementLimit = 512

	QueryScope = "github.com/uptrace/bun"
	StatsScope = "github.com/uptrace/opentelemetry-go-extra/otelsql"
)

var (
	ErrServiceName = errors.New("nimtel: service name required")
	ErrProtocol    = errors.New("nimtel: unsupported " + ProtocolEnv)
)

type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
	Debug          bool
	WithoutMetrics bool
	QueryMetrics   bool
}

func Init(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	if cfg.ServiceName == "" {
		return nil, ErrServiceName
	}
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	if os.Getenv(EndpointEnv) == "" {
		slog.SetDefault(logger.New(os.Stdout, cfg.Debug))
		return func(context.Context) error { return nil }, nil
	}

	instance := os.Getenv(ReplicaEnv)
	if instance == "" {
		name, err := os.Hostname()
		if err != nil {
			return nil, err
		}
		instance = name
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(cfg.ServiceVersion),
		semconv.ServiceInstanceID(instance),
		attribute.String(EnvironmentKey, cfg.Environment),
	))
	if err != nil {
		return nil, err
	}

	var (
		traceExp  sdktrace.SpanExporter
		metricExp sdkmetric.Exporter
		logExp    sdklog.Exporter
	)
	delta := os.Getenv(TemporalityEnv) == ""
	switch os.Getenv(ProtocolEnv) {
	case "", ProtocolGRPC:
		if traceExp, err = otlptracegrpc.New(ctx); err != nil {
			return nil, err
		}
		var opts []otlpmetricgrpc.Option
		if delta {
			opts = append(opts, otlpmetricgrpc.WithTemporalitySelector(sdkmetric.DeltaTemporalitySelector))
		}
		if metricExp, err = otlpmetricgrpc.New(ctx, opts...); err != nil {
			return nil, err
		}
		if logExp, err = otlploggrpc.New(ctx); err != nil {
			return nil, err
		}
	case ProtocolHTTP:
		if traceExp, err = otlptracehttp.New(ctx); err != nil {
			return nil, err
		}
		var opts []otlpmetrichttp.Option
		if delta {
			opts = append(opts, otlpmetrichttp.WithTemporalitySelector(sdkmetric.DeltaTemporalitySelector))
		}
		if metricExp, err = otlpmetrichttp.New(ctx, opts...); err != nil {
			return nil, err
		}
		if logExp, err = otlploghttp.New(ctx); err != nil {
			return nil, err
		}
	default:
		return nil, ErrProtocol
	}

	traceOpts := []sdktrace.TracerProviderOption{sdktrace.WithBatcher(traceExp), sdktrace.WithResource(res)}
	if os.Getenv(SamplerEnv) == "" {
		traceOpts = append(traceOpts, sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(SampleRatio))))
	}
	var readerOpts []sdkmetric.PeriodicReaderOption
	if os.Getenv(IntervalEnv) == "" {
		readerOpts = append(readerOpts, sdkmetric.WithInterval(ExportInterval))
	}
	meterOpts := []sdkmetric.Option{sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, readerOpts...)), sdkmetric.WithResource(res)}
	if !cfg.QueryMetrics {
		for _, scope := range []string{QueryScope, StatsScope} {
			meterOpts = append(meterOpts, sdkmetric.WithView(sdkmetric.NewView(
				sdkmetric.Instrument{Scope: instrumentation.Scope{Name: scope}},
				sdkmetric.Stream{Aggregation: sdkmetric.AggregationDrop{}},
			)))
		}
	}

	tp := sdktrace.NewTracerProvider(traceOpts...)
	mp := sdkmetric.NewMeterProvider(meterOpts...)
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)), sdklog.WithResource(res))

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	if !cfg.WithoutMetrics {
		if err := otelruntime.Start(otelruntime.WithMeterProvider(mp)); err != nil {
			return nil, err
		}
		if err := host.Start(host.WithMeterProvider(mp)); err != nil {
			return nil, err
		}
	}
	global.SetLoggerProvider(lp)
	slog.SetDefault(logger.New(os.Stdout, cfg.Debug, otelslog.NewHandler(cfg.ServiceName, otelslog.WithLoggerProvider(lp))))

	return func(ctx context.Context) error {
		return errors.Join(
			tp.ForceFlush(ctx), mp.ForceFlush(ctx), lp.ForceFlush(ctx),
			tp.Shutdown(ctx), mp.Shutdown(ctx), lp.Shutdown(ctx),
		)
	}, nil
}

type queryHook struct {
	*bunotel.QueryHook
	tracer trace.Tracer
	name   attribute.KeyValue
}

func QueryHook(dbName string) bun.QueryHook {
	return &queryHook{
		QueryHook: bunotel.NewQueryHook(bunotel.WithDBName(dbName)),
		tracer:    otel.Tracer(QueryScope),
		name:      dbconv.DBNameKey.String(dbName),
	}
}

func (h *queryHook) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	ctx, _ = h.tracer.Start(ctx, event.Operation(), trace.WithSpanKind(trace.SpanKindClient))
	return ctx
}

func (h *queryHook) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	h.QueryHook.AfterQuery(trace.ContextWithSpanContext(ctx, trace.SpanContext{}), event)

	span := trace.SpanFromContext(ctx)
	defer span.End()
	if !span.IsRecording() {
		return
	}

	attrs := []attribute.KeyValue{h.name, dbconv.DBOperationKey.String(event.Operation()), dbconv.DBStatementKey.String(statement(event))}
	switch event.DB.Dialect().Name() {
	case dialect.PG:
		attrs = append(attrs, dbconv.DBSystemPostgreSQL)
	case dialect.MySQL:
		attrs = append(attrs, dbconv.DBSystemMySQL)
	case dialect.SQLite:
		attrs = append(attrs, dbconv.DBSystemSqlite)
	case dialect.MSSQL:
		attrs = append(attrs, dbconv.DBSystemMSSQL)
	}
	if event.Result != nil {
		rows, _ := event.Result.RowsAffected()
		attrs = append(attrs, attribute.Int64("db.rows_affected", rows))
	}
	span.SetAttributes(attrs...)

	if event.Err != nil && !errors.Is(event.Err, sql.ErrNoRows) && !errors.Is(event.Err, sql.ErrTxDone) {
		span.RecordError(event.Err)
		span.SetStatus(codes.Error, event.Err.Error())
	}
}

func statement(event *bun.QueryEvent) string {
	query := string(event.QueryTemplate)
	if event.IQuery != nil {
		if b, err := event.IQuery.AppendQuery(schema.NewNopQueryGen(), nil); err == nil {
			query = string(b)
		}
	}
	if len(query) > StatementLimit {
		query = strings.ToValidUTF8(query[:StatementLimit], "")
	}
	return query
}

func Transport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base)
}

func Organization(ctx context.Context, rootID, ownerID uuid.UUID) context.Context {
	root, err := baggage.NewMember(RootKey, rootID.String())
	if err != nil {
		return ctx
	}
	owner, err := baggage.NewMember(OwnerKey, ownerID.String())
	if err != nil {
		return ctx
	}
	bag, err := baggage.FromContext(ctx).SetMember(root)
	if err != nil {
		return ctx
	}
	bag, err = bag.SetMember(owner)
	if err != nil {
		return ctx
	}
	trace.SpanFromContext(ctx).SetAttributes(
		attribute.String(RootKey, rootID.String()),
		attribute.String(OwnerKey, ownerID.String()),
	)
	return baggage.ContextWithBaggage(ctx, bag)
}
