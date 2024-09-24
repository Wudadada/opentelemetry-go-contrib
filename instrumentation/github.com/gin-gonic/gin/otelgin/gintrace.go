package otelgin

import (
	"context"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc" // 引入gRPC导出器
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/log"
	semconv "go.opentelemetry.io/otel/semconv/v1.20.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	tracerKey = "笑死了"
	ScopeName = "otelgin"
)

func Middleware(service string, opts ...Option) gin.HandlerFunc {
	ctx := context.Background()

	// 初始化 gRPC 日志导出器
	logExporter, err := otlploggrpc.New(ctx, otlploggrpc.WithInsecure())
	if err != nil {
		panic("无法初始化 gRPC 日志导出器")
	}

	lp := log.NewLoggerProvider(
		log.WithProcessor(
			log.NewBatchProcessor(logExporter),
		),
	)
	defer lp.Shutdown(ctx)
	global.SetLoggerProvider(lp)

	logger := otelslog.NewLogger(tracerKey)

	cfg := config{}
	for _, opt := range opts {
		opt.apply(&cfg)
	}
	if cfg.TracerProvider == nil {
		cfg.TracerProvider = otel.GetTracerProvider()
	}
	tracer := cfg.TracerProvider.Tracer(
		ScopeName,
		oteltrace.WithInstrumentationVersion(Version()),
	)
	if cfg.Propagators == nil {
		cfg.Propagators = otel.GetTextMapPropagator()
	}

	return func(c *gin.Context) {
		for _, f := range cfg.Filters {
			if !f(c.Request) {
				c.Next()
				return
			}
		}
		for _, f := range cfg.GinFilters {
			if !f(c) {
				c.Next()
				return
			}
		}
		c.Set(tracerKey, tracer)
		savedCtx := c.Request.Context()
		defer func() {
			c.Request = c.Request.WithContext(savedCtx)
		}()
		ctx := cfg.Propagators.Extract(savedCtx, propagation.HeaderCarrier(c.Request.Header))
		opts := []oteltrace.SpanStartOption{
			oteltrace.WithAttributes(
				semconv.ServiceNameKey.String(service),
				semconv.RPCSystemKey.String("grpc"),           // 使用 gRPC 系统
				semconv.RPCServiceKey.String(service),         // 服务名称
				semconv.RPCMethodKey.String(c.Request.Method), // gRPC 方法名称
				semconv.NetPeerNameKey.String(c.Request.Host), // 对端的主机名
			),
			oteltrace.WithSpanKind(oteltrace.SpanKindServer),
		}
		spanName := c.FullPath()
		ctx, span := tracer.Start(ctx, spanName, opts...)
		defer span.End()

		// 将追踪 ID 和 span ID 添加到日志中
		traceID := span.SpanContext().TraceID().String()
		spanID := span.SpanContext().SpanID().String()
		logger.With("traceID", traceID, "spanID", spanID).Debug("收到请求: ", c.Request.Method, " ", c.FullPath())

		c.Request = c.Request.WithContext(ctx)

		c.Next()

		status := c.Writer.Status()
		if status > 0 {
			span.SetAttributes(semconv.RPCGRPCStatusCodeKey.Int(status))
		}
		if len(c.Errors) > 0 {
			span.SetAttributes(attribute.String("gin.errors", c.Errors.String()))
			logger.With("traceID", traceID, "spanID", spanID).Error("请求出错: ", c.Errors.String())
		}
		logger.With("traceID", traceID, "spanID", spanID, "status", status).Info("请求完成: ", c.Request.Method, " ", c.FullPath())
	}
}
