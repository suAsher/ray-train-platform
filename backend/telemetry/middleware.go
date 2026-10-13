package telemetry

import (
	"context"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
)

func requestMethod(method string) string {
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE":
		return method
	default:
		return "OTHER"
	}
}
func requestRoute(c *gin.Context) string {
	if route := c.FullPath(); route != "" {
		return route
	}
	return "unmatched"
}
func statusClass(status int) string {
	if status < 100 || status > 599 {
		return "other"
	}
	return fmt.Sprintf("%dxx", status/100)
}

// Middleware starts a fresh server trace and intentionally never extracts
// traceparent, tracestate, baggage, or request IDs supplied by a caller.
// Only HTTP spans are produced; background training is not traced by this hook.
func (s *Service) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.traces == nil {
			c.Next()
			return
		}
		started := time.Now()
		method, route := requestMethod(c.Request.Method), requestRoute(c)
		base := baggage.ContextWithoutBaggage(c.Request.Context())
		ctx, span := s.traces.Tracer("raytrain.http").Start(base, method+" "+route, trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindServer), trace.WithTimestamp(started))
		c.Request = c.Request.WithContext(ctx)
		c.Header("X-Trace-ID", span.SpanContext().TraceID().String())
		active := s.metrics.inflight.WithLabelValues(method, route)
		active.Inc()
		defer func() {
			active.Dec()
			status := c.Writer.Status()
			route = requestRoute(c)
			elapsed := time.Since(started)
			s.metrics.requests.WithLabelValues(method, route, statusClass(status)).Inc()
			s.metrics.duration.WithLabelValues(method, route, statusClass(status)).Observe(elapsed.Seconds())
			span.SetName(method + " " + route)
			span.SetAttributes(attribute.String("http.request.method", method), attribute.String("http.route", route), attribute.Int("http.response.status_code", status))
			if status >= 500 && status <= 599 {
				span.SetStatus(codes.Error, "")
			}
			s.emitRequest(ctx, method, route, status, elapsed)
			span.End()
		}()
		c.Next()
	}
}
func (s *Service) emitRequest(ctx context.Context, method, route string, status int, elapsed time.Duration) {
	var record otellog.Record
	record.SetTimestamp(time.Now())
	record.SetBody(otellog.StringValue("http.request.completed"))
	record.SetSeverity(otellog.SeverityInfo)
	record.AddAttributes(otellog.String("http.request.method", method), otellog.String("http.route", route), otellog.Int("http.response.status_code", status), otellog.Int64("duration_ms", elapsed.Milliseconds()))
	s.logs.Logger("raytrain.http").Emit(ctx, record)
}
