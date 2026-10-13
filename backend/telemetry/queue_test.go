package telemetry

import (
 "context"
 "net/http/httptest"
 "strings"
 "testing"
 "time"
)

func TestCanceledExportCountsEachPendingRecordOnce(t *testing.T) {
 m:=newMetrics();started:=make(chan struct{})
 queue:=newQueue(3,"traces",func(ctx context.Context,_ []int)error{close(started);<-ctx.Done();return ctx.Err()},func(context.Context)error{return nil},m)
 queue.enqueue(1);go queue.ForceFlush(context.Background());<-started
 queue.enqueue(2);queue.enqueue(3)
 ctx,cancel:=context.WithTimeout(context.Background(),time.Millisecond);defer cancel();_ = queue.Shutdown(ctx)
 select{case <-queue.done:case <-time.After(time.Second):t.Fatal("worker retained queued records")}
 s:=&Service{metrics:m};w:=httptest.NewRecorder();s.metricsHandler().ServeHTTP(w,httptest.NewRequest("GET","/metrics",nil))
 if !strings.Contains(w.Body.String(),`raytrain_telemetry_dropped_total{reason="shutdown",signal="traces"} 2`){t.Fatal("pending records must be discarded exactly once")}
 if !strings.Contains(w.Body.String(),`raytrain_telemetry_dropped_total{reason="export_failed",signal="traces"} 1`){t.Fatal("failed in-flight record must be counted once")}
}
