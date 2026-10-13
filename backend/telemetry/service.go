// Package telemetry instruments control-plane HTTP only. It has no dependency
// on business authorization, repositories, or training lifecycle controllers.
package telemetry

import (
 "context"
 "crypto/tls"
 "crypto/x509"
 "errors"
 "io"
 "log"
 "net"
 "net/http"
 "time"

 "go.opentelemetry.io/otel/attribute"
 sdklog "go.opentelemetry.io/otel/sdk/log"
 "go.opentelemetry.io/otel/sdk/resource"
 sdktrace "go.opentelemetry.io/otel/sdk/trace"
 "ray-train-platform-backend/config"
)

type Service struct{
 cfg config.TelemetryConfig
 metrics *metrics
 metricsTLS *tls.Config
 server *http.Server
 traces *sdktrace.TracerProvider
 logs *sdklog.LoggerProvider
}
func New(cfg config.TelemetryConfig)(*Service,error){
 if err:=cfg.Validate();err!=nil{return nil,err};if !cfg.Enabled{return &Service{},nil}
 serverTLS,err:=metricsServerTLS(cfg);if err!=nil{return nil,err}
 if _,_,err:=loadIdentity(cfg.CAFile,cfg.OTLPCertFile,cfg.OTLPKeyFile,x509.ExtKeyUsageClientAuth);err!=nil{return nil,err}
 traces,logs,err:=exporters(cfg,&rotatingTransport{cfg:cfg});if err!=nil{return nil,err}
 s:=newService(traces,logs,1024);s.cfg=cfg;s.metricsTLS=serverTLS;return s,nil
}
func newService(traces sdktrace.SpanExporter,logs sdklog.Exporter,queueSize int)*Service{
 m:=newMetrics();res:=resource.NewSchemaless(attribute.String("service.name","ray-train-backend"))
 traceQueue:=newQueue(queueSize,"traces",traces.ExportSpans,traces.Shutdown,m);logQueue:=newQueue(queueSize,"logs",logs.Export,logs.Shutdown,m)
 return &Service{metrics:m,traces:sdktrace.NewTracerProvider(sdktrace.WithResource(res),sdktrace.WithSampler(sdktrace.AlwaysSample()),sdktrace.WithSpanProcessor(&spanProcessor{traceQueue})),logs:sdklog.NewLoggerProvider(sdklog.WithResource(res),sdklog.WithProcessor(&logProcessor{logQueue}))}
}
// Start binds only the separate, mutually authenticated metrics listener. An
// unavailable OTLP destination never prevents starting the business listener.
func(s *Service)Start()error{
 if !s.cfg.Enabled{return nil}
 listener,err:=net.Listen("tcp",s.cfg.MetricsAddr);if err!=nil{return errors.New("cannot bind telemetry metrics listener")}
 s.server=&http.Server{Handler:s.metricsHandler(),TLSConfig:s.metricsTLS,ReadHeaderTimeout:5*time.Second,ReadTimeout:10*time.Second,WriteTimeout:10*time.Second,IdleTimeout:10*time.Second,MaxHeaderBytes:8<<10,ErrorLog:log.New(io.Discard,"",0)}
 go func(){if err:=s.server.Serve(tls.NewListener(listener,s.metricsTLS));err!=nil&&!errors.Is(err,http.ErrServerClosed){s.metrics.failures.WithLabelValues("metrics").Inc()}}()
 return nil
}
func(s *Service)flush(ctx context.Context)error{
 if s.traces==nil{return nil};return errors.Join(s.traces.ForceFlush(ctx),s.logs.ForceFlush(ctx))
}
// Shutdown is bounded even when callers omit a deadline. Metrics shutdown,
// queued traces and request events share one five-second budget.
func(s *Service)Shutdown(ctx context.Context)error{
 ctx,cancel:=context.WithTimeout(ctx,5*time.Second);defer cancel();var errs []error
 if s.server!=nil{errs=append(errs,s.server.Shutdown(ctx))}
 if s.traces!=nil{errs=append(errs,s.traces.Shutdown(ctx),s.logs.Shutdown(ctx))}
 return errors.Join(errs...)
}
