package telemetry

import (
 "net/http"

 "github.com/prometheus/client_golang/prometheus"
 "github.com/prometheus/client_golang/prometheus/collectors"
 "github.com/prometheus/client_golang/prometheus/promhttp"
)

type metrics struct{
 registry *prometheus.Registry
 requests *prometheus.CounterVec
 duration *prometheus.HistogramVec
 inflight *prometheus.GaugeVec
 failures *prometheus.CounterVec
 dropped *prometheus.CounterVec
}
func newMetrics()*metrics{
 m:=&metrics{registry:prometheus.NewRegistry(),
 requests:prometheus.NewCounterVec(prometheus.CounterOpts{Name:"raytrain_http_requests_total",Help:"Completed control-plane HTTP requests."},[]string{"method","route","status_class"}),
 duration:prometheus.NewHistogramVec(prometheus.HistogramOpts{Name:"raytrain_http_request_duration_seconds",Help:"Control-plane HTTP duration in seconds.",Buckets:[]float64{.005,.01,.025,.05,.1,.25,.5,1,2.5,5,10,30,120}},[]string{"method","route","status_class"}),
 inflight:prometheus.NewGaugeVec(prometheus.GaugeOpts{Name:"raytrain_http_requests_inflight",Help:"Currently executing control-plane HTTP requests."},[]string{"method","route"}),
 failures:prometheus.NewCounterVec(prometheus.CounterOpts{Name:"raytrain_telemetry_export_failures_total",Help:"Failed OTLP export batches."},[]string{"signal"}),
 dropped:prometheus.NewCounterVec(prometheus.CounterOpts{Name:"raytrain_telemetry_dropped_total",Help:"Telemetry records discarded without affecting business requests."},[]string{"signal","reason"}),}
 m.registry.MustRegister(m.requests,m.duration,m.inflight,m.failures,m.dropped,collectors.NewGoCollector(),collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}));return m
}
func(s *Service)metricsHandler()http.Handler{
 handler:=promhttp.HandlerFor(s.metrics.registry,promhttp.HandlerOpts{MaxRequestsInFlight:2,Timeout:5e9})
 return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){if r.URL.Path!="/metrics"||r.URL.RawQuery!=""{http.NotFound(w,r);return};if r.Method!=http.MethodGet{w.WriteHeader(http.StatusMethodNotAllowed);return};handler.ServeHTTP(w,r)})
}
