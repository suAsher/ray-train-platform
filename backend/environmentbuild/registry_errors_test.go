package environmentbuild

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ray-train-platform-backend/registryauth"
)

type authenticationFailureRegistry struct {
	Registry
	failure error
}

func (r authenticationFailureRegistry) Authenticate(context.Context, Credentials) error {
	return r.failure
}
func (r authenticationFailureRegistry) CheckPush(context.Context, Credentials, string) error {
	return r.failure
}

func TestRegistryAvailabilityIsNotReportedAsIncorrectCredentials(t *testing.T) {
	for _, tc := range []struct{ failure, want error }{
		{registryauth.ErrCredentials, ErrAuthorization}, {registryauth.ErrForbidden, ErrAuthorization}, {ErrAuthorization, ErrAuthorization},
		{registryauth.ErrUnavailable, ErrUnavailable}, {context.DeadlineExceeded, ErrUnavailable}, {errors.New("private-upstream?token=fixture-secret"), ErrUnavailable},
	} {
		s, _, _, _, _ := lifecycleFixture(t)
		s.registry = authenticationFailureRegistry{failure: fmt.Errorf("wrapped: %w", tc.failure)}
		_, err := s.CreateAuthorization(context.Background(), Owner{UserID: "owner", TenantID: "team"}, Credentials{Username: "harbor-user", Secret: "fixture"})
		if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "fixture-secret") {
			t.Fatalf("wrong authentication classification: %v", err)
		}
		err = s.CheckTarget(context.Background(), Owner{UserID: "owner", TenantID: "team"}, "auth", "project", "image")
		if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "fixture-secret") {
			t.Fatalf("wrong target classification: %v", err)
		}
		_, err = s.bindAuthorization(context.Background(), Owner{UserID: "owner", TenantID: "team"}, "auth", storeBuildForRegistryFailure())
		if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "fixture-secret") {
			t.Fatalf("wrong bind classification: %v", err)
		}
	}
}

func storeBuildForRegistryFailure() Build {
	return Build{ID: "build", Project: "project", Repository: "image"}
}

func TestBackgroundPushCheckDistinguishesRegistryOutageFromDeniedCredentials(t *testing.T) {
	for _, tc := range []struct {
		failure error
		status  string
	}{
		{registryauth.ErrCredentials, AwaitingAuth}, {registryauth.ErrForbidden, AwaitingAuth}, {ErrAuthorization, AwaitingAuth},
		{registryauth.ErrUnavailable, Failed}, {context.DeadlineExceeded, Failed}, {errors.New("private?token=fixture-secret"), Failed},
	} {
		s, store, runner, _, _ := lifecycleFixture(t)
		store.build.ArtifactDigest = "sha256:" + strings.Repeat("1", 64)
		s.registry = authenticationFailureRegistry{failure: tc.failure}
		if err := s.reconcileBuild(context.Background(), store.build); err != nil {
			t.Fatal(err)
		}
		if store.build.Status != tc.status || store.build.ResumeStatus != Pushing || runner.steps != 0 || store.build.ArtifactDigest == "" {
			t.Fatalf("wrong background failure state: %+v", store.build)
		}
		if strings.Contains(store.build.Message, "fixture-secret") {
			t.Fatal("private registry diagnostic leaked")
		}
		if tc.status == Failed && (!strings.Contains(store.build.Message, "按结束策略清理") || strings.Contains(store.build.Message, "授权已失效") || strings.Contains(store.build.Message, "没有目标仓库")) {
			t.Fatal("network failure misreported as missing permission")
		}
		if err := s.reconcileBuild(context.Background(), store.build); err != nil {
			t.Fatal(err)
		}
		if runner.steps != 0 || runner.cleanups != 1 || !runner.retained || store.build.CleanedAt == nil || !store.revoked {
			t.Fatal("failed push check did not clean credentials and reach bounded artifact retry state")
		}
	}
}
