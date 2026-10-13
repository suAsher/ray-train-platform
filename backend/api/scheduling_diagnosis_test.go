package api

import (
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
					StatusReason string `json:"statusReason"`
					SchedulingCapacity *struct {
						Available bool `json:"available"`
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
		var payload struct { Data map[string]json.RawMessage `json:"data"` }
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if _, present := payload.Data["schedulingCapacity"]; present {
			t.Fatalf("state %s received unrelated current scheduling snapshot", state)
		}
	}
}
