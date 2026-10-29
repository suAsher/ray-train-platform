// Package webui serves the console built from the frontend in this repository.
package webui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed dist
var assets embed.FS

// Config contains public browser settings only. Never add credentials here.
type Config struct {
	AuthRequired     bool   `json:"authRequired"`
	KeycloakURL      string `json:"keycloakURL"`
	KeycloakRealm    string `json:"keycloakRealm"`
	KeycloakClientID string `json:"keycloakClientID"`
}

// KeycloakConfig derives public browser settings from the verified API issuer.
// The built-in browser adapter supports Keycloak; other OIDC providers may use
// the API integration independently with local browser login enabled.
func KeycloakConfig(issuerURL, clientID string, required bool) (Config, error) {
	if !required {
		return Config{}, nil
	}
	issuer, err := url.Parse(issuerURL)
	if err != nil || issuer.Host == "" || (issuer.Scheme != "https" && issuer.Scheme != "http") || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return Config{}, fmt.Errorf("browser OIDC requires a valid Keycloak issuer URL")
	}
	prefix, realm, found := strings.Cut(strings.TrimSuffix(issuer.Path, "/"), "/realms/")
	if !found || realm == "" || strings.Contains(realm, "/") || clientID == "" {
		return Config{}, fmt.Errorf("browser OIDC requires a Keycloak /realms/<realm> issuer and client ID")
	}
	issuer.Path = prefix
	issuer.RawPath = ""
	return Config{AuthRequired: true, KeycloakURL: strings.TrimSuffix(issuer.String(), "/"), KeycloakRealm: realm, KeycloakClientID: clientID}, nil
}

// Register installs the SPA fallback after API routes have been registered.
func Register(router *gin.Engine, config Config) {
	root, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err)
	}
	register(router, root, config)
}

func register(router *gin.Engine, root fs.FS, config Config) {
	settings, err := json.Marshal(config)
	if err != nil {
		panic(err)
	}
	router.GET("/runtime-config.js", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Data(http.StatusOK, "application/javascript; charset=utf-8", append([]byte("window.__RAY_PLATFORM_CONFIG__ = "), append(settings, ';')...))
	})
	files := http.FileServer(http.FS(root))
	router.NoRoute(func(c *gin.Context) {
		requestPath := c.Request.URL.Path
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead || reserved(requestPath) || unsafePath(requestPath) {
			c.Status(http.StatusNotFound)
			return
		}
		name := strings.TrimPrefix(requestPath, "/")
		if info, err := fs.Stat(root, name); err == nil && !info.IsDir() {
			c.Header("X-Content-Type-Options", "nosniff")
			c.Header("Cache-Control", "no-cache")
			files.ServeHTTP(c.Writer, c.Request)
			return
		}
		// Missing assets must not return HTML and hide a broken deployment.
		if path.Ext(name) != "" || strings.HasPrefix(name, "assets/") {
			c.Status(http.StatusNotFound)
			return
		}
		index, err := fs.ReadFile(root, "index.html")
		if err != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.Header("Cache-Control", "no-cache")
		c.Header("X-Content-Type-Options", "nosniff")
		if c.Request.Method == http.MethodHead {
			c.Header("Content-Type", "text/html; charset=utf-8")
			c.Status(http.StatusOK)
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	})
}

func reserved(value string) bool {
	for _, prefix := range []string{"/api", "/ray", "/mlflow", "/healthz", "/readyz", "/metrics", "/internal"} {
		if value == prefix || strings.HasPrefix(value, prefix+"/") {
			return true
		}
	}
	return false
}

func unsafePath(value string) bool {
	if strings.ContainsAny(value, "\\\x00") {
		return true
	}
	for _, segment := range strings.Split(value, "/") {
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}
