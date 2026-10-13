package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestSchedulingDiagnosisUnavailableDoesNotFailLogDiagnosis(t *testing.T) {
	for _, state := range []domain.State{domain.StateSubmitted, domain.StateValidating, domain.StateQueued, domain.StateAdmitted, domain.StateProvisioning} {
		t.Run(string(state), func(t *testing.T) {
			job := diagnosisTestJob()
			job.ObservedState = state
			job.StatusReason = "ImagePullBackOff"
			job.StatusMessage = "image unavailable"
			_, response := readDiagnosisTestResponse(t, &fakeJobRepository{jobs: []domain.TrainingJob{job}}, &diagnosisTestProvider{err: errors.New("private log backend")})
			var payload struct {
				Data struct {
					StatusReason       string `json:"statusReason"`
					SchedulingCapacity *struct {
						Available         bool   `json:"available"`
						UnavailableReason string `json:"unavailableReason"`
					} `json:"schedulingCapacity"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			capacity := payload.Data.SchedulingCapacity
			if capacity == nil || capacity.Available || capacity.UnavailableReason == "" {
				t.Fatalf("early job must disclose unavailable capacity: %s", response.Body.String())
			}
			if payload.Data.StatusReason != "ImagePullBackOff" || strings.Contains(response.Body.String(), "private log backend") {
				t.Fatalf("capacity replaced startup evidence or leaked provider details: %s", response.Body.String())
			}
		})
	}
}

func TestSchedulingDiagnosisDoesNotAttachCapacityToRunningOrFinishedJobs(t *testing.T) {
	for _, state := range []domain.State{domain.StateRunning, domain.StateSucceeded, domain.StateFailed, domain.StateCanceled, domain.StateTimedOut} {
		job := diagnosisTestJob()
		job.ObservedState = state
		_, response := readDiagnosisTestResponse(t, &fakeJobRepository{jobs: []domain.TrainingJob{job}}, nil)
		var payload struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if _, present := payload.Data["schedulingCapacity"]; present {
			t.Fatalf("state %s received unrelated current scheduling snapshot", state)
		}
	}
}

func TestSchedulingDiagnosisQuotaExcludesOnlyOwnActiveReservation(t *testing.T) {
	for _, test := range []struct {
		name                                     string
		desired                                  domain.DesiredState
		used, available, wantUsed, wantAvailable int
		wantReason                               string
	}{
		{"active", domain.DesiredActive, 64, 0, 56, 8, "fits"},
		{"over-limit", domain.DesiredActive, 70, 0, 62, 2, "team_quota_exceeded"},
		{"cancel-requested", domain.DesiredCanceled, 60, 4, 60, 4, "team_quota_exceeded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			quota := domain.TenantQuota{TenantID: "team-a", GPULimit: 64, GPUUsed: test.used, GPUAvailable: test.available}
			capacity := domain.SchedulingCapacity{Available: true, Quota: &quota, Fit: domain.SchedulingCapacityFit{CanFit: true, Reason: "fits"}}
			job := diagnosisTestJob()
			job.DesiredState = test.desired
			job.Spec.Resources = domain.Resources{WorkerReplicas: 2, GPUsPerWorker: 4}
			got := jobCapacityQuota(capacity, job)
			if got.Quota.GPUUsed != test.wantUsed || got.Quota.GPUAvailable != test.wantAvailable || got.Fit.Reason != test.wantReason {
				t.Fatalf("quota: %+v fit: %+v", got.Quota, got.Fit)
			}
			if quota.GPUUsed != test.used || quota.GPUAvailable != test.available {
				t.Fatal("diagnosis mutated shared quota")
			}
		})
	}
}

func TestSchedulingDiagnosisProvisioningCapacityIsReference(t *testing.T) {
	job := diagnosisTestJob()
	job.ObservedState = domain.StateProvisioning
	job.StatusReason = ""
	h := NewHandler(&fakeJobRepository{}, Options{})
	got := h.jobSchedulingCapacity(context.Background(), job)
	if got == nil || got.Fit.Reason != "already_admitted_capacity_reference" {
		t.Fatalf("startup must not be diagnosed as capacity wait: %+v", got)
	}
}

func TestSchedulingDiagnosisConcreteStartupErrorRemainsPrimary(t *testing.T) {
	job := diagnosisTestJob()
	job.ObservedState = domain.StateQueued
	job.StatusReason = "ImagePullBackOff"
	h := NewHandler(&fakeJobRepository{}, Options{})
	got := h.jobSchedulingCapacity(context.Background(), job)
	if got == nil || got.Fit.Reason != "startup_status_capacity_reference" || job.StatusReason != "ImagePullBackOff" {
		t.Fatalf("startup reason lost: %+v", got)
	}
}
