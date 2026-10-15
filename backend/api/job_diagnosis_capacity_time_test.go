package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
)

type diagnosisClockTestProvider struct {
	diagnosisTestProvider
	finished time.Time
}

func (p *diagnosisClockTestProvider) QueryJobDiagnosisCandidates(ctx context.Context, id string, limit int, start, end time.Time) ([]observability.LogLine, error) {
	lines, err := p.diagnosisTestProvider.QueryJobDiagnosisCandidates(ctx, id, limit, start, end)
	p.finished = time.Now().UTC()
	return lines, err
}

func TestJobDiagnosisCapacityClockIsStampedAfterLogQuery(t *testing.T) {
	for _, mode := range []string{"unsupported-provider", "provider-error", "success"} {
		t.Run(mode, func(t *testing.T) {
			job := diagnosisTestJob()
			job.ObservedState = domain.StateQueued
			clockProvider := &diagnosisClockTestProvider{}
			var provider LogProvider = clockProvider
			if mode == "unsupported-provider" {
				provider = nil
			} else if mode == "provider-error" {
				clockProvider.err = errors.New("unavailable")
			}
			started := time.Now().UTC()
			_, response := readDiagnosisTestResponse(t, &fakeJobRepository{jobs: []domain.TrainingJob{job}}, provider)
			finished := time.Now().UTC()
			var body struct {
				Data struct {
					Capacity *struct {
						ObservedAt time.Time  `json:"observedAt"`
						ServerTime *time.Time `json:"serverTime"`
					} `json:"schedulingCapacity"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			capacity := body.Data.Capacity
			if capacity == nil || capacity.ServerTime == nil || capacity.ServerTime.IsZero() {
				t.Fatalf("diagnosis capacity must include a response clock: %s", response.Body)
			}
			if capacity.ServerTime.Before(started) || capacity.ServerTime.After(finished) || capacity.ServerTime.Before(capacity.ObservedAt) || capacity.ServerTime.Before(clockProvider.finished) {
				t.Fatalf("response clock predates final diagnosis processing: %+v, provider finished %s", capacity, clockProvider.finished)
			}
		})
	}
}
