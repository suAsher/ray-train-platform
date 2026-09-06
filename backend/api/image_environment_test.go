package api

import (
	"net/http"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"strings"
	"testing"
)

func TestCreateImageEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, role, environment string
		status                  int
	}{
		{"admin declaration", domain.RoleTenantAdmin, `{"python":"3.11","dependencies":"numpy==1.26.4","validationNotes":"GPU 未验证"}`, http.StatusCreated},
		{"invalid declaration", domain.RoleTenantAdmin, `{"python":"` + strings.Repeat("x", 129) + `"}`, http.StatusBadRequest},
		{"engineer forbidden", domain.RoleEngineer, `{"python":"3.11"}`, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &compatibilityImageStore{}
			response := postImage(t, store, auth.Principal{Subject: "user", TenantID: "team-a", Roles: []string{tc.role}, AuthType: auth.AuthTypeLocal}, `{"name":"custom","reference":"harbor.other.example/team/train:v1","kind":"training","environment":`+tc.environment+`}`)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.status == http.StatusCreated {
				if store.created.Environment.Python != "3.11" || !strings.Contains(response.Body.String(), `"python":"3.11"`) {
					t.Fatal("environment not persisted/returned")
				}
			} else if store.created.ID != "" {
				t.Fatal("invalid request persisted")
			}
		})
	}
}
