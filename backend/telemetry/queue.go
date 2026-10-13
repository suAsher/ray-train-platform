package telemetry

import (
 "context"
 "sync"
 "time"

 "github.com/prometheus/client_golang/prometheus"
 sdklog "go.opentelemetry.io/otel/sdk/log"
 sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const exportTimeout = 3*time.Second

// boundedQueue implements the SDK processor contract without blocking OnEnd or
// OnEmit. Owning admission lets us count drops exactly, without parsing SDK logs.
// Only the worker calls the official OTLP exporter; no request does network I/O.
type boundedQueue[T any] struct{
 items chan T
 flushes chan chan struct{}
 stop,done chan struct{}
 ctx context.Context
 cancel context.CancelFunc
 mu sync.Mutex
 closed bool
 export func(context.Context,[]T)error
 shutdownExporter func(context.Context)error
 failures prometheus.Counter
 dropped *prometheus.CounterVec
 signal string
}
func newQueue[T any](size int,signal string,export func(context.Context,[]T)error,shutdown func(context.Context)error,m *metrics)*boundedQueue[T]{
 ctx,cancel:=context.WithCancel(context.Background())
 q:=&boundedQueue[T]{items:make(chan T,size),flushes:make(chan chan struct{}),stop:make(chan struct{}),done:make(chan struct{}),ctx:ctx,cancel:cancel,export:export,shutdownExporter:shutdown,failures:m.failures.WithLabelValues(signal),dropped:m.dropped,signal:signal}
 go q.run();return q
}
func(q *boundedQueue[T])enqueue(item T){
 q.mu.Lock();defer q.mu.Unlock();if q.closed{q.drop("shutdown",1);return}
 select{case q.items<-item:default:q.drop("queue_full",1)}
}
func(q *boundedQueue[T])drop(reason string,count int){q.dropped.WithLabelValues(q.signal,reason).Add(float64(count))}
func(q *boundedQueue[T])exportBatch(batch []T){
 if len(batch)==0{return};ctx,cancel:=context.WithTimeout(q.ctx,exportTimeout);defer cancel()
 if err:=q.export(ctx,batch);err!=nil{q.failures.Inc();q.drop("export_failed",len(batch))}
}
func(q *boundedQueue[T])drain(){
 for len(q.items)>0{if q.ctx.Err()!=nil{q.drop("shutdown",len(q.items));return};batch:=make([]T,0,128);for len(batch)<128{select{case item:=<-q.items:batch=append(batch,item);default:q.exportBatch(batch);goto next}};q.exportBatch(batch);next:}
}
func(q *boundedQueue[T])run(){
 defer close(q.done);ticker:=time.NewTicker(200*time.Millisecond);defer ticker.Stop()
 for{select{case <-ticker.C:q.drain();case ack:=<-q.flushes:q.drain();close(ack);case <-q.stop:q.drain();return;case <-q.ctx.Done():q.drop("shutdown",len(q.items));return}}
}
func(q *boundedQueue[T])ForceFlush(ctx context.Context)error{
 ack:=make(chan struct{});select{case <-q.done:return nil;case q.flushes<-ack:case <-ctx.Done():return ctx.Err()};select{case <-ack:return nil;case <-q.done:return nil;case <-ctx.Done():return ctx.Err()}
}
func(q *boundedQueue[T])Shutdown(ctx context.Context)error{
 q.mu.Lock();if q.closed{q.mu.Unlock();return nil};q.closed=true;close(q.stop);q.mu.Unlock()
 select{case <-q.done:q.cancel();return q.shutdownExporter(ctx);case <-ctx.Done():q.cancel();return ctx.Err()}
}
type spanProcessor struct{*boundedQueue[sdktrace.ReadOnlySpan]}
func(p *spanProcessor)OnStart(context.Context,sdktrace.ReadWriteSpan){}
func(p *spanProcessor)OnEnd(span sdktrace.ReadOnlySpan){p.enqueue(span)}
type logProcessor struct{*boundedQueue[sdklog.Record]}
func(p *logProcessor)Enabled(context.Context,sdklog.EnabledParameters)bool{return true}
func(p *logProcessor)OnEmit(_ context.Context,record *sdklog.Record)error{p.enqueue(record.Clone());return nil}
