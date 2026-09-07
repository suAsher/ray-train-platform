package main

import (
	"context"
	"log"
	"time"

	"ray-train-platform-backend/config"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
)

type trainingCapacityReader interface {
	TrainingPoolCapacity(context.Context, map[string]string) (k8s.TrainingPoolCapacity, error)
}

type trainingCapacityObserver struct {
	reader       trainingCapacityReader
	selector     map[string]string
	timeout      time.Duration
	logf         func(string, ...any)
	lastError    string
	lastCapacity k8s.TrainingPoolCapacity
}

// Every API replica needs current process-local limits, including standbys.
// This observer only reads nodes; ClusterQueue writes remain leader-owned.
func startTrainingCapacityObserver(ctx context.Context, reader trainingCapacityReader, cfg config.Config) {
	if !cfg.KueueAutoQuota || reader == nil {
		return
	}
	observer := &trainingCapacityObserver{reader: reader, selector: cfg.TrainingNodeSelector, timeout: 5 * time.Second, logf: log.Printf}
	// Bound startup's first read so requests do not initially see stale config
	// on a healthy cluster, without making API startup depend on Kubernetes.
	observer.observe(ctx)
	go observer.run(ctx, 5*time.Second)
}

func (o *trainingCapacityObserver) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.observe(ctx)
		}
	}
}

func (o *trainingCapacityObserver) observe(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	capacity, err := o.reader.TrainingPoolCapacity(readCtx, o.selector)
	if ctx.Err() != nil {
		return
	}
	if err == nil {
		err = readCtx.Err()
	}
	if err == nil {
		err = domain.UpdateResourceLimitsFromCapacity(capacity.Nodes, capacity.GuaranteedGPUsPerWorker, capacity.GPUs)
	}
	if err != nil {
		if message := err.Error(); message != o.lastError {
			o.logf("training capacity observation deferred; retaining last valid limits: %v", err)
			o.lastError = message
		}
		return
	}
	o.lastError = ""
	if capacity != o.lastCapacity {
		o.logf("training capacity observed: nodes=%d GPUs=%d GPUsPerWorker=%d", capacity.Nodes, capacity.GPUs, capacity.GuaranteedGPUsPerWorker)
		o.lastCapacity = capacity
	}
}
