package functionwarehouse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(Development)
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL
	return client
}

func writeEnvelope(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func warehouseFixture() Warehouse {
	return Warehouse{ID: "warehouse-1", GroupID: Environments()[1].GroupID, Name: "Fixture", PermissionCodes: []string{"view", "edit"}}
}

func TestFixedTargets(t *testing.T) {
	targets := Environments()
	if len(targets) != 2 || targets[0].BaseURL != "https://spiking.wellspiking.ai" || targets[0].GroupID != "a989a8e9f2b94758952a60500ba8bb7b" || targets[1].BaseURL != "https://spiking-dev.wellspiking.ai" || targets[1].GroupID != "d49a984edd3d0648a43ab050d3cc0262" {
		t.Fatalf("incorrect fixed targets: %#v", targets)
	}
	targets[0].GroupID = "changed"
	if Environments()[0].GroupID == "changed" {
		t.Fatal("targets shares mutable storage")
	}
	for _, value := range []Environment{"", "http://localhost", "https://example.com", "Production"} {
		if _, err := NewClient(value); !errors.Is(err, ErrInvalid) {
			t.Errorf("environment %q accepted: %v", value, err)
		}
	}
}

func TestListWarehousesContract(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/model/functionWarehouse/page" || r.Header.Get("Authorization") != "Bearer user-token" {
			t.Errorf("unexpected request contract")
		}
		q := r.URL.Query()
		if q.Get("groupId") != warehouseFixture().GroupID || q.Get("pageNum") != "2" || q.Get("pageSize") != "10" || q.Get("keywords") != "name & model" {
			t.Errorf("unexpected query: %v", q)
		}
		writeEnvelope(w, WarehousePage{Records: []Warehouse{warehouseFixture()}, Total: 11, Current: 2, Size: 10})
	})
	page, err := client.ListWarehouses(context.Background(), "user-token", PageQuery{PageNum: 2, PageSize: 10, Keywords: "name & model"})
	if err != nil || page.Total != 11 || len(page.Records) != 1 || page.Records[0].ID != "warehouse-1" {
		t.Fatalf("page = %#v, err = %v", page, err)
	}
}

func TestReadWarehouseChildren(t *testing.T) {
	var detailRequests atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("functionWarehouseId") != "warehouse-1" {
			t.Errorf("missing warehouse scope")
		}
		switch r.URL.Path {
		case "/model/functionWarehouse":
			detailRequests.Add(1)
			writeEnvelope(w, warehouseFixture())
		case "/model/model/type/list":
			if r.URL.Query().Get("groupId") != warehouseFixture().GroupID {
				t.Error("missing fixed group")
			}
			writeEnvelope(w, []ModelType{{ID: "model-1", Name: "Model"}})
		case "/model/model/version/page":
			if r.URL.Query().Get("groupId") != warehouseFixture().GroupID {
				t.Error("missing fixed group")
			}
			writeEnvelope(w, map[string]any{"records": []any{map[string]any{"id": "version-1", "version": "v1", "modelTypeId": "model-1", "paths": []any{map[string]any{"url": "private/source"}}, "unverifiedSource": "private"}}, "total": 1, "current": 1, "size": 20})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	codes, err := client.PermissionCodes(context.Background(), "user-token", "warehouse-1")
	if err != nil || len(codes) != 2 || codes[1] != "edit" {
		t.Fatalf("codes = %v, err = %v", codes, err)
	}
	models, err := client.ListModelTypes(context.Background(), "user-token", "warehouse-1")
	if err != nil || len(models) != 1 || models[0].ID != "model-1" {
		t.Fatalf("models = %v, err = %v", models, err)
	}
	versions, err := client.ListVersions(context.Background(), "user-token", "warehouse-1", PageQuery{})
	if err != nil || len(versions.Records) != 1 || versions.Records[0].Version != "v1" {
		t.Fatalf("versions = %#v, err = %v", versions, err)
	}
	encoded, _ := json.Marshal(versions)
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "unverifiedSource") {
		t.Fatal("unvalidated upstream metadata escaped read model")
	}
	if detailRequests.Load() != 3 {
		t.Fatal("each child operation must verify warehouse scope")
	}
}

func TestRejectCrossGroupAndMismatchedWarehouse(t *testing.T) {
	for _, fixture := range []Warehouse{{ID: "warehouse-1", GroupID: "other"}, {ID: "warehouse-1"}, {ID: "other", GroupID: warehouseFixture().GroupID}} {
		t.Run(fixture.ID+fixture.GroupID, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { writeEnvelope(w, fixture) })
			if _, err := client.GetWarehouse(context.Background(), "token", "warehouse-1"); !errors.Is(err, ErrForbidden) {
				t.Fatalf("foreign warehouse accepted: %v", err)
			}
		})
	}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, WarehousePage{Records: []Warehouse{{ID: "foreign", GroupID: "other"}}})
	})
	if _, err := client.ListWarehouses(context.Background(), "token", PageQuery{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign page accepted: %v", err)
	}
}

func TestErrorClassificationAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{401, `{"msg":"secret-token"}`, ErrUnauthorized},
		{403, `secret-token`, ErrForbidden},
		{429, `secret-token`, ErrRateLimited},
		{502, `secret-token`, ErrUnavailable},
		{200, `{"code":401,"msg":"secret-token"}`, ErrUnauthorized},
		{200, `{"code":403,"msg":"secret-token"}`, ErrForbidden},
		{200, `{"code":429,"msg":"secret-token"}`, ErrRateLimited},
		{200, `{"code":42,"msg":"secret-token"}`, ErrUnavailable},
		{200, `{"data":[]}`, ErrUnavailable},
		{200, `{"code":null,"data":[]}`, ErrUnavailable},
		{200, `{"code":0,"data":null}`, ErrUnavailable},
		{200, `{"code":0,"data":[]} secret-token`, ErrUnavailable},
	} {
		t.Run(fmt.Sprintf("%d/%s", tc.status, tc.body), func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := client.ListWarehouses(context.Background(), "secret-token", PageQuery{})
			if !errors.Is(err, tc.want) || strings.Contains(fmt.Sprintf("%+v", err), "secret-token") {
				t.Fatalf("bad safe error: %v", err)
			}
			if errors.Is(err, ErrRateLimited) {
				var upstream *Error
				if !errors.As(err, &upstream) || upstream.RetryAfter != "30" {
					t.Fatalf("missing retry hint: %v", err)
				}
			}
		})
	}
}

func TestRedirectIsRejectedWithoutForwardingToken(t *testing.T) {
	var requests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer destination.Close()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) })
	if _, err := client.ListWarehouses(context.Background(), "secret-token", PageQuery{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("redirect accepted: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatal("followed redirect")
	}
}

func TestLimitsAndCancellation(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat(" ", 129))) })
	client.maxResponseBytes = 128
	if _, err := client.ListWarehouses(context.Background(), "token", PageQuery{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("oversized response accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.ListWarehouses(ctx, "token", PageQuery{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("canceled context accepted: %v", err)
	}
	blocking := testClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	blocking.requestTimeout = 20 * time.Millisecond
	if _, err := blocking.ListWarehouses(context.Background(), "token", PageQuery{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("request timeout ignored: %v", err)
	}
}

func TestInvalidInputDoesNotSendRequest(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid input reached upstream") })
	for _, token := range []string{"", "Bearer token", "token\r\ninjected", " token"} {
		if _, err := client.ListWarehouses(context.Background(), token, PageQuery{}); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("invalid token accepted: %v", err)
		}
	}
	for _, query := range []PageQuery{{PageNum: -1}, {PageSize: 101}, {Keywords: strings.Repeat("x", 257)}} {
		if _, err := client.ListWarehouses(context.Background(), "token", query); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid query accepted: %v", err)
		}
	}
	for _, id := range []string{"", "../bad", "id\n", strings.Repeat("x", 129)} {
		if _, err := client.GetWarehouse(context.Background(), "token", id); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid id accepted: %v", err)
		}
	}
}

func TestChildScopeConflictAndRevocation(t *testing.T) {
	for _, path := range []string{"models", "versions"} {
		for _, conflict := range []string{"group", "warehouse", "revoked"} {
			t.Run(path+"/"+conflict, func(t *testing.T) {
				client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/model/functionWarehouse" {
						if conflict == "revoked" {
							w.WriteHeader(http.StatusForbidden)
							return
						}
						writeEnvelope(w, warehouseFixture())
						return
					}
					if conflict == "revoked" {
						t.Error("read child despite failed warehouse permission check")
					}
					row := map[string]any{"id": "child-1"}
					if conflict == "group" {
						row["groupId"] = "another-group"
					} else {
						row["functionWarehouseId"] = "another-warehouse"
					}
					if path == "models" {
						writeEnvelope(w, []any{row})
					} else {
						writeEnvelope(w, map[string]any{"records": []any{row}, "total": 1})
					}
				})
				var err error
				if path == "models" {
					_, err = client.ListModelTypes(context.Background(), "token", "warehouse-1")
				} else {
					_, err = client.ListVersions(context.Background(), "token", "warehouse-1", PageQuery{})
				}
				if !errors.Is(err, ErrForbidden) {
					t.Fatalf("scope conflict accepted: %v", err)
				}
			})
		}
	}
}

func TestEachCallUsesCurrentToken(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		expected := fmt.Sprintf("Bearer token-%d", calls.Add(1))
		if r.Header.Get("Authorization") != expected {
			t.Error("reused authorization from another request")
		}
		writeEnvelope(w, WarehousePage{Records: []Warehouse{}, Current: 1, Size: 20})
	})
	for _, token := range []string{"token-1", "token-2"} {
		page, err := client.ListWarehouses(context.Background(), token, PageQuery{})
		if err != nil || page.Records == nil {
			t.Fatalf("empty page = %#v, err = %v", page, err)
		}
	}
}

func TestMalformedPageAndUnsafeRetryHint(t *testing.T) {
	for _, payload := range []string{`{}`, `{"records":null}`, `{"records":[{}]}`, `{"records":[],"total":-1}`} {
		client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, `{"code":0,"data":%s}`, payload)
		})
		if _, err := client.ListWarehouses(context.Background(), "token", PageQuery{}); !errors.Is(err, ErrUnavailable) {
			t.Errorf("malformed page accepted: %v", err)
		}
	}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "sensitive-upstream-message")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := client.ListWarehouses(context.Background(), "token", PageQuery{})
	var upstream *Error
	if !errors.As(err, &upstream) || upstream.RetryAfter != "" {
		t.Fatal("unsafe retry header escaped")
	}
}
