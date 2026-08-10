package auth

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestExtractBearerToken(t *testing.T) {
	token, err := ExtractBearer("Bearer abc.def.ghi")
	if err != nil || token != "abc.def.ghi" {
		t.Fatalf("unexpected token extraction: %q %v", token, err)
	}
	if _, err := ExtractBearer("Basic abc"); err == nil {
		t.Fatal("expected invalid authorization scheme error")
	}
}

func TestPrincipalFromGinReturnsAuthenticatedPrincipal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	want := Principal{Subject: "user-1", TenantID: "tenant-a", Roles: []string{"Engineer"}}
	ctx.Set(principalContextKey, want)

	got, ok := PrincipalFromGin(ctx)
	if !ok || got.Subject != want.Subject || got.TenantID != want.TenantID {
		t.Fatalf("unexpected principal: %+v, ok=%v", got, ok)
	}
}
