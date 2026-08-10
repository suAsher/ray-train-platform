package httpapi

import "testing"

func TestSuccessEnvelopeContainsRequestIDAndData(t *testing.T) {
	response := Success("req-123", map[string]string{"id": "job-1"})
	if !response.Success || response.RequestID != "req-123" {
		t.Fatalf("unexpected success envelope: %+v", response)
	}
	if response.Data["id"] != "job-1" || response.Error != nil {
		t.Fatalf("unexpected payload: %+v", response)
	}
}

func TestFailureEnvelopeContainsSafePublicError(t *testing.T) {
	response := Failure[struct{}]("req-456", "JOB_NOT_FOUND", "任务不存在")
	if response.Success || response.Error == nil {
		t.Fatalf("unexpected failure envelope: %+v", response)
	}
	if response.Error.Code != "JOB_NOT_FOUND" || response.Error.Message != "任务不存在" {
		t.Fatalf("unexpected public error: %+v", response.Error)
	}
}
