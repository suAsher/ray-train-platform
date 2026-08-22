package domain

import (
	"strings"
	"testing"
	"time"
)

func TestJobDashboardAccessTokenIsBoundToJobTenantAndUser(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	token, err := IssueJobDashboardAccessToken("tenant-a", "job-1", "user-1", testPepper(), now, time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if err := VerifyJobDashboardAccessToken(token, "tenant-a", "job-1", "user-1", testPepper(), now.Add(30*time.Second)); err != nil {
		t.Fatalf("verify token: %v", err)
	}
	for _, changed := range []struct{ tenant, job, user string }{
		{"tenant-b", "job-1", "user-1"},
		{"tenant-a", "job-2", "user-1"},
		{"tenant-a", "job-1", "user-2"},
	} {
		if err := VerifyJobDashboardAccessToken(token, changed.tenant, changed.job, changed.user, testPepper(), now); err == nil {
			t.Fatalf("token must not verify for %+v", changed)
		}
	}
}

func TestJobDashboardAccessTokenExpiresAndRejectsWeakSecret(t *testing.T) {
	now := time.Now().UTC()
	if _, err := IssueJobDashboardAccessToken("tenant-a", "job-1", "user-1", []byte("short"), now, time.Minute); err == nil {
		t.Fatal("expected weak signing secret to be rejected")
	}
	token, err := IssueJobDashboardAccessToken("tenant-a", "job-1", "user-1", testPepper(), now, time.Second)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if err := VerifyJobDashboardAccessToken(token, "tenant-a", "job-1", "user-1", testPepper(), now.Add(2*time.Second)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expiry error, got %v", err)
	}
}
