package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

func TestConsoleRoutesKeepAPIBoundaries(t *testing.T) {
	router := gin.New()
	router.GET("/api/v1/example", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	register(router, fstest.MapFS{"index.html": {Data: []byte("<html>console</html>")}, "assets/app.js": {Data: []byte("console.log('app')")}}, Config{})
	for _, tc := range []struct {
		method string
		path   string
		status int
	}{
		{"GET", "/", 200}, {"GET", "/job/detail/one", 200}, {"HEAD", "/job", 200},
		{"GET", "/api/v1/example", 200}, {"GET", "/api/missing", 404}, {"GET", "/ray/missing", 404},
		{"GET", "/mlflow/missing", 404}, {"GET", "/metrics", 404}, {"GET", "/internal/missing", 404},
		{"POST", "/job", 404}, {"GET", "/assets/missing.js", 404}, {"GET", "/.env", 404},
		{"GET", "/foo/../index.html", 404}, {"GET", "/assets/app.js", 200},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status {
				t.Fatalf("got %d, want %d: %s", response.Code, tc.status, response.Body.String())
			}
			if tc.method == http.MethodHead && response.Body.Len() != 0 {
				t.Fatal("HEAD response contains a body")
			}
		})
	}
}

func TestPublicRuntimeConfigEscapesScriptContent(t *testing.T) {
	router := gin.New()
	register(router, fstest.MapFS{}, Config{KeycloakURL: "</script><script>bad</script>"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/runtime-config.js", nil))
	if response.Code != 200 || strings.Contains(response.Body.String(), "</script>") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unsafe runtime configuration: %s", response.Body.String())
	}
}

func TestKeycloakConfig(t *testing.T) {
	cfg, err := KeycloakConfig("https://identity.example.com/auth/realms/training", "raytrain", true)
	if err != nil || cfg.KeycloakURL != "https://identity.example.com/auth" || cfg.KeycloakRealm != "training" || !cfg.AuthRequired {
		t.Fatalf("unexpected config: %+v %v", cfg, err)
	}
	if _, err := KeycloakConfig("https://identity.example.com/oidc", "raytrain", true); err == nil {
		t.Fatal("non-Keycloak browser issuer accepted")
	}
	if cfg, err := KeycloakConfig("", "", false); err != nil || cfg.AuthRequired {
		t.Fatal("local browser login must not require OIDC")
	}
}
