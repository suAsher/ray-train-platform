package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func TestJobDashboardPortalRoundTripPreservesPrefixAndStripsCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, key := range []string{"Authorization", "Cookie", "X-Auth-Request-Access-Token", "X-Auth-Request-User", "X-Forwarded-Access-Token", "X-Api-Key"} {
			if r.Header.Get(key) != "" {
				t.Errorf("upstream received credential header %s", key)
			}
		}
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Set-Cookie", "untrusted=1; Path=/")
		_, _ = io.WriteString(w, `fetch("/api/v0/nodes")`)
	}))
	defer upstream.Close()
	h, _ := dashboardTestHandler(t, upstream.URL)
	inner := gin.New()
	h.RegisterJobDashboardProxyRoute(inner.Group("/api/v1"))
	routes := inner.Group("/api/v1", func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "user-1", TenantID: "tenant-a", AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	h.RegisterTrainingRoutes(routes)
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/raytrain/") {
			t.Errorf("request escaped Portal prefix: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/raytrain")
		inner.ServeHTTP(w, r)
	}))
	defer outer.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	response, err := client.Post(outer.URL+"/raytrain/api/v1/jobs/job-1/dashboard-access?portal=1", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Data.URL, "/raytrain/api/v1/jobs/job-1/dashboard/") {
		t.Fatalf("bad access URL: %s", result.Data.URL)
	}
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, outer.URL+result.Data.URL, nil)
	request.Header.Set("Authorization", "Bearer test-only")
	request.Header.Set("X-Auth-Request-Access-Token", "test-only")
	request.Header.Set("X-Auth-Request-User", "test-only")
	request.Header.Set("X-Forwarded-Access-Token", "test-only")
	request.Header.Set("X-Api-Key", "test-only")
	page, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer page.Body.Close()
	body, _ := io.ReadAll(page.Body)
	if page.StatusCode != 200 || string(body) != `fetch("/raytrain/api/v1/jobs/job-1/dashboard/api/v0/nodes")` {
		t.Fatalf("round trip: %d %s", page.StatusCode, body)
	}
	if page.Header.Get("Set-Cookie") != "" {
		t.Fatal("upstream cookie escaped proxy")
	}
	if strings.Contains(page.Request.URL.RawQuery, "access_token") || strings.Contains(page.Request.URL.RawQuery, "portal") {
		t.Fatal("credential/routing query remained after exchange")
	}
}

func TestJobDashboardRejectsInvalidPortalHint(t *testing.T) {
	h, _ := dashboardTestHandler(t, "http://dashboard.invalid")
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "user-1", TenantID: "tenant-a", AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	h.RegisterTrainingRoutes(router.Group("/api/v1"))
	for _, q := range []string{"portal=https://evil.invalid", "portal=1&portal=1", "portal=0"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/job-1/dashboard-access?"+q, nil))
		if w.Code != 400 {
			t.Fatalf("invalid portal hint %q accepted: %d", q, w.Code)
		}
	}
}

func TestJobDashboardPortalTokenCannotAccessAnotherJob(t *testing.T) {
	h, _ := dashboardTestHandler(t, "http://dashboard.invalid")
	router := gin.New()
	h.RegisterJobDashboardProxyRoute(router.Group("/api/v1"))
	token, err := domain.IssueJobDashboardAccessToken("tenant-a", "job-1", "user-1", h.workspacePepper, time.Now(), domain.JobDashboardAccessTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-2/dashboard/?portal=1&tenant=tenant-a&subject=user-1&access_token="+token, nil))
	if w.Code != 401 {
		t.Fatalf("cross-job token accepted: %d", w.Code)
	}
}

func TestJobDashboardRewritesHTMLAndRedirectsWithoutChangingRelativeAssets(t *testing.T) {
	for _, prefix := range []string{"", "/raytrain"} {
		base := prefix + jobDashboardBasePath("job-1")
		target, _ := url.Parse("http://head.invalid")
		request := httptest.NewRequest(http.MethodGet, "http://head.invalid/", nil)
		response := &http.Response{Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader(`<script src="/static/main.js"></script><link href="./favicon.ico"><link href="//cdn.invalid/x"><a href="/">home</a><a href="/api/jobs">jobs</a>`)), Request: request}
		if err := rewriteRayDashboardResponse(response, base); err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		if !strings.Contains(string(body), `src="`+base+`static/main.js"`) || !strings.Contains(string(body), `href="./favicon.ico"`) || !strings.Contains(string(body), `href="//cdn.invalid/x"`) || !strings.Contains(string(body), `href="`+base+`"`) || !strings.Contains(string(body), `href="`+base+`api/jobs"`) {
			t.Fatalf("HTML rewrite: %s", body)
		}
		response.Header.Set("Location", "/static/main.js?x=1")
		if err := rewriteRayDashboardLocation(response, target, base); err != nil {
			t.Fatal(err)
		}
		if response.Header.Get("Location") != base+"static/main.js?x=1" {
			t.Fatal("redirect lost proxy prefix")
		}
		response.Header.Set("Location", "https://evil.invalid/")
		if err := rewriteRayDashboardLocation(response, target, base); err == nil {
			t.Fatal("accepted external redirect")
		}
	}
}

func TestJobDashboardRejectsCookieSignedWithUnconfiguredKey(t *testing.T) {
	for _, key := range [][]byte{nil, []byte("short")} {
		h, _ := dashboardTestHandler(t, "http://dashboard.invalid")
		h.workspacePepper = key
		router := gin.New()
		h.RegisterJobDashboardProxyRoute(router.Group("/api/v1"))
		expiry := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write([]byte("job-dashboard-access\x00tenant-a\x00job-1\x00user-1\x00" + expiry))
		request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-1/dashboard/", nil)
		for name, value := range map[string]string{jobDashboardSessionCookie: expiry + "." + hex.EncodeToString(mac.Sum(nil)), jobDashboardTenantCookie: "tenant-a", jobDashboardSubjectCookie: "user-1", jobDashboardPortalCookie: "1"} {
			request.AddCookie(&http.Cookie{Name: name, Value: value})
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("weak-key proxy accepted: %d", response.Code)
		}
	}
}
