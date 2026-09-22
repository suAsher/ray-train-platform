package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/config"
)

func TestAssistantRoutesAreOptionalAndRemainAuthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _,enabled:=range []bool{false,true}{
		r:=gin.New();h:=api.NewHandler(&mainJobRepository{},api.Options{})
		registerAPIRoutesWithLocalAuth(r,h,nil,nil,nil,nil,nil,nil,nil,nil,nil,config.Config{OIDCRequired:true,Assistant:config.AssistantConfig{Enabled:enabled}})
		foundQuery,foundCaps:=false,false
		for _,route:=range r.Routes(){if route.Path=="/api/v1/assistant/query"{foundQuery=true};if route.Path=="/api/v1/assistant/capabilities"{foundCaps=true}}
		if foundQuery!=enabled||!foundCaps{t.Fatalf("incorrect optional registration enabled=%v",enabled)}
		w:=httptest.NewRecorder();r.ServeHTTP(w,httptest.NewRequest(http.MethodGet,"/api/v1/assistant/capabilities",nil));if w.Code!=401{t.Fatalf("anonymous caps status=%d",w.Code)}
		if enabled{w=httptest.NewRecorder();request:=httptest.NewRequest(http.MethodPost,"/api/v1/assistant/query",strings.NewReader(`{"question":"hi"}`));request.Header.Set("Content-Type","application/json");r.ServeHTTP(w,request);if w.Code!=401{t.Fatalf("anonymous query status=%d",w.Code)}}
	}
}
