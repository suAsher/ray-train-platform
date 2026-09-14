package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	fw "ray-train-platform-backend/functionwarehouse"
)

type warehouseAPIOIDC struct { calls int }

func (v *warehouseAPIOIDC) VerifyIdentity(_ context.Context, token string) (auth.OIDCIdentity, error) {
	v.calls++
	if token != "verified-proxy" && token != "verified-bearer" {
		return auth.OIDCIdentity{}, errors.New("invalid test identity")
	}
	return auth.OIDCIdentity{Subject: "oidc-alice", Username: "alice"}, nil
}

type warehouseAPIAccounts struct { disabled bool }

func (a warehouseAPIAccounts) ResolveOAuth2ProxyAccount(context.Context, string) (domain.LocalUser, bool, error) {
	return domain.LocalUser{ID: "alice", Username: "alice", TenantID: "local", Roles: []string{"Engineer"}, Disabled: a.disabled}, true, nil
}

type warehouseAPIPAT struct{}

func (warehouseAPIPAT) Authenticate(context.Context, string) (auth.PATIdentity, error) {
	return auth.PATIdentity{Principal: auth.Principal{Subject: "alice", TenantID: "local"}, Scopes: []string{domain.PATScopeJobsRead}}, nil
}

type warehouseAPILocal struct{}

func (warehouseAPILocal) Authenticate(context.Context, string) (auth.Principal, error) {
	return auth.Principal{Subject: "alice", TenantID: "local"}, nil
}

type warehouseAPIClient struct {
	tokens []string
	query fw.PageQuery
	warehouseIDs []string
	listErr, detailErr, typesErr error
	deadlinePresent bool
}

func (f *warehouseAPIClient) ListWarehouses(ctx context.Context, token string, q fw.PageQuery) (fw.WarehousePage, error) {
	f.tokens = append(f.tokens, token)
	f.query = q
	_, f.deadlinePresent = ctx.Deadline()
	return fw.WarehousePage{Records: []fw.Warehouse{{ID: "warehouse-1", Name: "test warehouse"}}, Total: 1, Current: q.PageNum, Size: q.PageSize}, f.listErr
}

func (f *warehouseAPIClient) GetWarehouse(_ context.Context, token, id string) (fw.Warehouse, error) {
	f.tokens = append(f.tokens, token)
	f.warehouseIDs = append(f.warehouseIDs, id)
	return fw.Warehouse{ID: id, Name: "test warehouse", PermissionCodes: []string{"view", "edit"}}, f.detailErr
}

func (f *warehouseAPIClient) ListModelTypes(_ context.Context, token, id string) ([]fw.ModelType, error) {
	f.tokens = append(f.tokens, token)
	f.warehouseIDs = append(f.warehouseIDs, id)
	return []fw.ModelType{{ID: "model-1", Name: "test model", FunctionWarehouseID: id}}, f.typesErr
}

func warehouseAPIRouter(clients map[fw.Environment]FunctionWarehouseClient, accounts warehouseAPIAccounts) (*gin.Engine, *warehouseAPIOIDC) {
	gin.SetMode(gin.TestMode)
	oidc := &warehouseAPIOIDC{}
	r := gin.New()
	r.Use(auth.OAuth2ProxyMiddleware(oidc, accounts, warehouseAPIPAT{}, warehouseAPILocal{}, true, auth.OAuth2ProxyOptions{}))
	h := NewHandler(&fakeJobRepository{}, Options{Models: &modelStoreFake{}, FunctionWarehouses: clients})
	h.RegisterFunctionWarehouseRoutes(r.Group("/api/v1"))
	return r, oidc
}

func warehouseAPIRequest(r http.Handler, path, bearer, proxy string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/function-warehouse"+path, nil)
	if bearer != "" { req.Header.Set("Authorization", "Bearer "+bearer) }
	if proxy != "" { req.Header.Set("X-Auth-Request-Access-Token", proxy) }
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func warehouseAPIErrorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var response struct { Error struct { Code string `json:"code"` } `json:"error"` }
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil { t.Fatal(err) }
	return response.Error.Code
}

func TestFunctionWarehouseDelegatesOnlyVerifiedAuthenticationToken(t *testing.T) {
	for _, tc := range []struct { name, bearer, proxy, want string }{
		{"proxy", "", "verified-proxy", "verified-proxy"},
		{"bearer takes precedence", "verified-bearer", "unverified-header", "verified-bearer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &warehouseAPIClient{}
			r, oidc := warehouseAPIRouter(map[fw.Environment]FunctionWarehouseClient{fw.Development: client}, warehouseAPIAccounts{})
			w := warehouseAPIRequest(r, "/development/warehouses?pageNum=2&pageSize=7&keywords=weights", tc.bearer, tc.proxy)
			if w.Code != 200 { t.Fatalf("status %d: %s", w.Code, w.Body.String()) }
			if oidc.calls != 1 || len(client.tokens) != 1 || client.tokens[0] != tc.want { t.Fatal("upstream did not receive exactly the token verified by OAuth middleware") }
			if client.query != (fw.PageQuery{PageNum: 2, PageSize: 7, Keywords: "weights"}) || !client.deadlinePresent { t.Fatal("pagination or bounded request context was lost") }
			if strings.Contains(w.Body.String(), tc.want) || w.Header().Get("Cache-Control") != "no-store" { t.Fatal("credential or cache boundary violated") }
		})
	}
}

func TestFunctionWarehouseRejectsNonDelegatableCredentials(t *testing.T) {
	for _, tc := range []struct { name, bearer, proxy string; disabled bool; status int; code string; oidcCalls int }{
		{"PAT cannot delegate proxy header", "rpt_test", "verified-proxy", false, 403, "INTERACTIVE_LOGIN_REQUIRED", 0},
		{"local session cannot delegate proxy header", "rls_test", "verified-proxy", false, 401, "WAREHOUSE_OAUTH_REQUIRED", 0},
		{"unsigned header rejected", "", "attacker-controlled", false, 401, "INVALID_AUTHENTICATION", 1},
		{"invalid bearer does not fall back", "invalid", "verified-proxy", false, 401, "INVALID_AUTHENTICATION", 1},
		{"missing authentication", "", "", false, 401, "AUTH_REQUIRED", 0},
		{"disabled account", "", "verified-proxy", true, 403, "ACCOUNT_NOT_PROVISIONED", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &warehouseAPIClient{}
			r, oidc := warehouseAPIRouter(map[fw.Environment]FunctionWarehouseClient{fw.Development: client}, warehouseAPIAccounts{disabled: tc.disabled})
			w := warehouseAPIRequest(r, "/development/warehouses", tc.bearer, tc.proxy)
			if w.Code != tc.status || warehouseAPIErrorCode(t, w) != tc.code { t.Fatalf("unexpected response %d: %s", w.Code, w.Body.String()) }
			if len(client.tokens) != 0 || oidc.calls != tc.oidcCalls { t.Fatal("authentication failure reached upstream or wrong verifier") }
		})
	}
}

func TestFunctionWarehouseUsesFixedConfiguredEnvironments(t *testing.T) {
	client := &warehouseAPIClient{}
	r, _ := warehouseAPIRouter(map[fw.Environment]FunctionWarehouseClient{fw.Development: client}, warehouseAPIAccounts{})
	w := warehouseAPIRequest(r, "/environments", "", "verified-proxy")
	var response struct { Data struct { Items []fw.Target `json:"items"` } `json:"data"` }
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil { t.Fatal(err) }
	if w.Code != 200 || len(response.Data.Items) != 1 { t.Fatalf("invalid environments: %s", w.Body.String()) }
	target := response.Data.Items[0]
	if target.Environment != fw.Development || target.BaseURL != "https://spiking-dev.wellspiking.ai" || target.GroupID != "d49a984edd3d0648a43ab050d3cc0262" { t.Fatal("environment target changed") }
	for _, tc := range []struct { path string; status int }{
		{"/arbitrary/warehouses", 400}, {"/production/warehouses", 503},
	} {
		w := warehouseAPIRequest(r, tc.path, "", "verified-proxy")
		if w.Code != tc.status { t.Fatalf("%s: status %d", tc.path, w.Code) }
	}
	if len(client.tokens) != 0 { t.Fatal("invalid or unavailable environment reached another target") }
}

func TestFunctionWarehouseRejectsInvalidQueryBeforeUpstream(t *testing.T) {
	for _, query := range []string{
		"pageNum=0", "pageNum=10001", "pageNum=x", "pageSize=0", "pageSize=101", "pageSize=x",
		"keywords="+strings.Repeat("x", 201), "groupId=another-team", "baseUrl=https://attacker.invalid",
	} {
		t.Run(query, func(t *testing.T) {
			client := &warehouseAPIClient{}
			r, _ := warehouseAPIRouter(map[fw.Environment]FunctionWarehouseClient{fw.Development: client}, warehouseAPIAccounts{})
			w := warehouseAPIRequest(r, "/development/warehouses?"+query, "", "verified-proxy")
			if w.Code != 400 || warehouseAPIErrorCode(t, w) != "WAREHOUSE_QUERY_INVALID" || len(client.tokens) != 0 { t.Fatalf("invalid query reached upstream: %d", w.Code) }
		})
	}
}

func TestFunctionWarehouseModelTypesRequireWarehouseAccess(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprint(denied), func(t *testing.T) {
			client := &warehouseAPIClient{}
			if denied { client.detailErr = fw.ErrForbidden }
			r, _ := warehouseAPIRouter(map[fw.Environment]FunctionWarehouseClient{fw.Development: client}, warehouseAPIAccounts{})
			w := warehouseAPIRequest(r, "/development/warehouses/warehouse-1/model-types", "", "verified-proxy")
			wantStatus, wantCalls := 200, 2
			if denied { wantStatus, wantCalls = 403, 1 }
			if w.Code != wantStatus || len(client.tokens) != wantCalls { t.Fatalf("access check behavior: status %d calls %d", w.Code, len(client.tokens)) }
			for i, token := range client.tokens {
				if token != "verified-proxy" || client.warehouseIDs[i] != "warehouse-1" { t.Fatal("model request delegation lost source identity or target") }
			}
		})
	}
}

func TestFunctionWarehouseUpstreamErrorsRemainSafe(t *testing.T) {
	for _, tc := range []struct { err error; status int; code string }{
		{fw.ErrUnauthorized, 401, "WAREHOUSE_REAUTH_REQUIRED"},
		{fw.ErrForbidden, 403, "WAREHOUSE_FORBIDDEN"},
		{fw.ErrInvalid, 400, "WAREHOUSE_REQUEST_INVALID"},
		{fw.ErrRateLimited, 429, "WAREHOUSE_RATE_LIMITED"},
		{errors.New("private upstream details verified-proxy"), 502, "WAREHOUSE_UPSTREAM_FAILED"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			client := &warehouseAPIClient{listErr: fmt.Errorf("private upstream details: %w", tc.err)}
			r, _ := warehouseAPIRouter(map[fw.Environment]FunctionWarehouseClient{fw.Development: client}, warehouseAPIAccounts{})
			w := warehouseAPIRequest(r, "/development/warehouses", "", "verified-proxy")
			if w.Code != tc.status || warehouseAPIErrorCode(t, w) != tc.code { t.Fatalf("unexpected mapping: %d %s", w.Code, w.Body.String()) }
			if strings.Contains(w.Body.String(), "private upstream") || strings.Contains(w.Body.String(), "verified-proxy") { t.Fatal("upstream error leaked details") }
			if tc.status == 429 && w.Header().Get("Retry-After") == "" { t.Fatal("missing retry hint") }
		})
	}
}

func TestFunctionWarehouseRouteRateLimitPreventsUpstreamCalls(t *testing.T) {
	client := &warehouseAPIClient{}
	r, _ := warehouseAPIRouter(map[fw.Environment]FunctionWarehouseClient{fw.Development: client}, warehouseAPIAccounts{})
	for i := 0; i < 120; i++ {
		w := warehouseAPIRequest(r, "/development/warehouses", "", "verified-proxy")
		if w.Code != 200 { t.Fatalf("unexpected status before limit at request %d: %d", i+1, w.Code) }
	}
	w := warehouseAPIRequest(r, "/development/warehouses", "", "verified-proxy")
	if w.Code != 429 || len(client.tokens) != 120 || w.Header().Get("Retry-After") == "" { t.Fatalf("limit did not stop upstream: status %d calls %d", w.Code, len(client.tokens)) }
}
