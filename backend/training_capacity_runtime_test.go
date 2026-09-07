package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"ray-train-platform-backend/config"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
)

type capacityReaderFunc func(context.Context, map[string]string) (k8s.TrainingPoolCapacity, error)

func (f capacityReaderFunc) TrainingPoolCapacity(ctx context.Context, selector map[string]string) (k8s.TrainingPoolCapacity, error) {
	return f(ctx, selector)
}

func TestTrainingCapacityObserverStartupAndDisabled(t *testing.T) {
	original := domain.CurrentResourceLimits()
	t.Cleanup(func() { domain.SetResourceLimits(original) })
	domain.SetResourceLimits(domain.ResourceLimits{MaxWorkerReplicas: 2, MaxGPUsPerWorker: 8, MaxTotalGPUs: 16})
	calls := 0
	reader := capacityReaderFunc(func(ctx context.Context, selector map[string]string) (k8s.TrainingPoolCapacity, error) {
		calls++
		if selector["pool"] != "training" {
			t.Fatal("wrong selector")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing read deadline")
		}
		return k8s.TrainingPoolCapacity{Nodes: 3, GPUs: 24, GuaranteedGPUsPerWorker: 8, CPUMillis: 24_000, MemoryBytes: 96 << 30}, nil
	})
	startTrainingCapacityObserver(context.Background(), reader, config.Config{})
	startTrainingCapacityObserver(context.Background(), nil, config.Config{KueueAutoQuota: true})
	if calls != 0 {
		t.Fatal("disabled observer read capacity")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startTrainingCapacityObserver(ctx, reader, config.Config{KueueAutoQuota: true, TrainingNodeSelector: map[string]string{"pool": "training"}})
	if calls != 1 || domain.CurrentResourceLimits().MaxTotalGPUs != 24 {
		t.Fatal("startup must synchronously replace stale 16 GPU limit")
	}
}

func TestTrainingCapacityObserverPreservesLastGoodAndDeduplicatesErrors(t *testing.T) {
	original := domain.CurrentResourceLimits()
	t.Cleanup(func() { domain.SetResourceLimits(original) })
	domain.SetResourceLimits(domain.ResourceLimits{MaxWorkerReplicas: 3, MaxGPUsPerWorker: 8, MaxTotalGPUs: 24})
	logs := 0
	o := trainingCapacityObserver{reader: capacityReaderFunc(func(context.Context, map[string]string) (k8s.TrainingPoolCapacity, error) {
		return k8s.TrainingPoolCapacity{}, errors.New("offline")
	}), timeout: time.Second, logf: func(string, ...any) { logs++ }}
	o.observe(context.Background())
	o.observe(context.Background())
	if logs != 1 || domain.CurrentResourceLimits().MaxTotalGPUs != 24 {
		t.Fatal("failed reads must retain last good capacity and avoid repeated logs")
	}
	o.reader = capacityReaderFunc(func(context.Context, map[string]string) (k8s.TrainingPoolCapacity, error) {
		return k8s.TrainingPoolCapacity{CPUMillis: 1000}, nil
	})
	o.observe(context.Background())
	if domain.CurrentResourceLimits().MaxTotalGPUs != 24 {
		t.Fatal("invalid observation replaced capacity")
	}
}

func TestTrainingCapacityObserverEmptyObservationClearsStaleLimit(t *testing.T) {
	original := domain.CurrentResourceLimits()
	t.Cleanup(func() { domain.SetResourceLimits(original) })
	domain.SetResourceLimits(domain.ResourceLimits{MaxWorkerReplicas: 2, MaxGPUsPerWorker: 8, MaxTotalGPUs: 16})
	o := trainingCapacityObserver{timeout: time.Second, logf: func(string, ...any) {}, reader: capacityReaderFunc(func(context.Context, map[string]string) (k8s.TrainingPoolCapacity, error) {
		return k8s.TrainingPoolCapacity{}, nil
	})}
	o.observe(context.Background())
	if got := domain.CurrentResourceLimits().MaxTotalGPUs; got != 0 {
		t.Fatalf("successful empty observation retained stale GPU limit: %d", got)
	}
	o.reader = capacityReaderFunc(func(context.Context, map[string]string) (k8s.TrainingPoolCapacity, error) {
		return k8s.TrainingPoolCapacity{}, errors.New("offline")
	})
	o.observe(context.Background())
	if got := domain.CurrentResourceLimits().MaxTotalGPUs; got != 0 {
		t.Fatalf("failed read must retain last valid zero: %d", got)
	}
}

func TestTrainingCapacityObserverPeriodicReadAndCancellation(t *testing.T) {
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	o := trainingCapacityObserver{timeout: time.Second, logf: func(string, ...any) {}, reader: capacityReaderFunc(func(ctx context.Context, _ map[string]string) (k8s.TrainingPoolCapacity, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		return k8s.TrainingPoolCapacity{}, ctx.Err()
	})}
	done := make(chan struct{})
	go func() { defer close(done); o.run(ctx, time.Millisecond) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("periodic read never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop observer")
	}
	if calls.Load() != 1 {
		t.Fatal("observer read after cancellation")
	}
}

func TestTrainingCapacityObserverReadTimeoutKeepsLastGood(t *testing.T) {
	original := domain.CurrentResourceLimits()
	t.Cleanup(func() { domain.SetResourceLimits(original) })
	domain.SetResourceLimits(domain.ResourceLimits{MaxWorkerReplicas: 2, MaxGPUsPerWorker: 8, MaxTotalGPUs: 16})
	o := trainingCapacityObserver{timeout: time.Millisecond, logf: func(string, ...any) {}, reader: capacityReaderFunc(func(ctx context.Context, _ map[string]string) (k8s.TrainingPoolCapacity, error) {
		<-ctx.Done()
		return k8s.TrainingPoolCapacity{Nodes: 3, GPUs: 24, GuaranteedGPUsPerWorker: 8, CPUMillis: 24_000, MemoryBytes: 96 << 30}, nil
	})}
	o.observe(context.Background())
	if domain.CurrentResourceLimits().MaxTotalGPUs != 16 {
		t.Fatal("expired read changed limits")
	}
}

func TestTrainingCapacityObserverLogsOnlyChangedCapacity(t *testing.T) {
	original := domain.CurrentResourceLimits()
	t.Cleanup(func() { domain.SetResourceLimits(original) })
	logs := 0
	capacity := k8s.TrainingPoolCapacity{Nodes: 2, GPUs: 16, GuaranteedGPUsPerWorker: 8, CPUMillis: 16_000, MemoryBytes: 64 << 30}
	o := trainingCapacityObserver{timeout: time.Second, logf: func(string, ...any) { logs++ }, reader: capacityReaderFunc(func(context.Context, map[string]string) (k8s.TrainingPoolCapacity, error) { return capacity, nil })}
	o.observe(context.Background())
	o.observe(context.Background())
	if logs != 1 || domain.CurrentResourceLimits().MaxTotalGPUs != 16 {
		t.Fatal("unchanged capacity should not repeat log")
	}
	capacity = k8s.TrainingPoolCapacity{Nodes: 3, GPUs: 24, GuaranteedGPUsPerWorker: 8, CPUMillis: 24_000, MemoryBytes: 96 << 30}
	o.observe(context.Background())
	if logs != 2 || domain.CurrentResourceLimits().MaxTotalGPUs != 24 {
		t.Fatal("new capacity was not updated and logged")
	}
}
