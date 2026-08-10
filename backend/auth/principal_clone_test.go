package auth

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGinPrincipalBoundaryClonesRolesAndScopes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/", nil)
	original := Principal{Subject: "user", Roles: []string{"Engineer"}, Scopes: []string{"jobs:read"}}
	setPrincipal(c, original)
	original.Roles[0] = "SuperAdmin"
	original.Scopes[0] = "jobs:write"

	first, ok := PrincipalFromGin(c)
	if !ok || first.Roles[0] != "Engineer" || first.Scopes[0] != "jobs:read" {
		t.Fatalf("stored principal was mutated through input slices: roles=%v scopes=%v", first.Roles, first.Scopes)
	}
	first.Roles[0] = "SuperAdmin"
	first.Scopes[0] = "jobs:write"
	second, _ := PrincipalFromGin(c)
	if second.Roles[0] != "Engineer" || second.Scopes[0] != "jobs:read" {
		t.Fatalf("stored principal was mutated through output slices: roles=%v scopes=%v", second.Roles, second.Scopes)
	}
}

func TestContextPrincipalBoundaryClonesRolesAndScopes(t *testing.T) {
	original := Principal{Subject: "user", Roles: []string{"Engineer"}, Scopes: []string{"jobs:read"}}
	ctx := SetPrincipalContext(context.Background(), original)
	original.Roles[0] = "SuperAdmin"
	original.Scopes[0] = "jobs:write"

	first, ok := PrincipalFromContext(ctx)
	if !ok || first.Roles[0] != "Engineer" || first.Scopes[0] != "jobs:read" {
		t.Fatalf("context principal was mutated through input slices: roles=%v scopes=%v", first.Roles, first.Scopes)
	}
	first.Roles[0] = "SuperAdmin"
	first.Scopes[0] = "jobs:write"
	second, _ := PrincipalFromContext(ctx)
	if second.Roles[0] != "Engineer" || second.Scopes[0] != "jobs:read" {
		t.Fatalf("context principal was mutated through output slices: roles=%v scopes=%v", second.Roles, second.Scopes)
	}
}
