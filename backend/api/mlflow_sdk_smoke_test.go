package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"ray-train-platform-backend/mlflowtracking"
	"ray-train-platform-backend/observability"
	"ray-train-platform-backend/repositories"
)

// This opt-in test runs the real pinned SDK against the platform service and an
// isolated MLflow server. Never point its explicit URL at production MLflow.
func TestMLflowSDK314Integration(t *testing.T) {
	upstream:=os.Getenv("MLFLOW_TRACKING_SMOKE_URL")
	python:=os.Getenv("MLFLOW_SDK_SMOKE_PYTHON")
	if upstream==""||python=="" {t.Skip("isolated MLflow and pinned Python SDK required")}
	parsed,parseErr:=url.Parse(upstream)
	if parseErr!=nil || parsed.Scheme!="http" || parsed.User!=nil || parsed.RawQuery!="" || parsed.Fragment!="" || (parsed.Hostname()!="tracking-mlflow" && parsed.Hostname()!="127.0.0.1" && parsed.Hostname()!="localhost") {t.Fatal("SDK smoke requires an explicitly isolated HTTP MLflow URL")}
	database,err:=gorm.Open(sqlite.Open(filepath.Join(t.TempDir(),"tracking.db")),&gorm.Config{})
	if err!=nil {t.Fatal(err)}
	if err=database.AutoMigrate(&repositories.MLflowTrackingExperimentRecord{},&repositories.MLflowTrackingRunRecord{});err!=nil {t.Fatal(err)}
	key:=make([]byte,32);if _,err=rand.Read(key);err!=nil {t.Fatal(err)}
	service:=mlflowtracking.New(repositories.NewMLflowTrackingStore(database),&observability.MLflowClient{BaseURL:upstream,ProvenanceKey:key},mlflowtracking.Options{CursorKey:key})
	actor:=mlflowtracking.Actor{TenantID:"sdk-smoke",UserID:"sdk-owner"}
	idempotency:=hex.EncodeToString(key[:16])
	experiment,err:=service.CreateExperiment(context.Background(),actor,idempotency,"sdk-smoke")
	if err!=nil {t.Fatal(err)}
	run,err:=service.CreateRun(context.Background(),actor,experiment.ID,idempotency,"sdk-roundtrip")
	if err!=nil {t.Fatal(err)}
	p:=trackingPrincipal("experiments:read","experiments:write");p.TenantID=actor.TenantID;p.Subject=actor.UserID
	gin.SetMode(gin.TestMode);router:=gin.New()
	router.Use(func(c *gin.Context){
		if c.GetHeader("Authorization")!="Bearer "+hex.EncodeToString(key) {c.AbortWithStatus(401);return}
		c.Set("ray-platform-principal",p);c.Next()
	})
	h:=NewHandler(&fakeJobRepository{},Options{MLflowTracking:service,MLflowDashboardStore:newFakeMLflowDashboardStore()})
	h.RegisterMLflowSDKRoutes(router.Group("/api/v1"))
	server:=httptest.NewServer(router);defer server.Close()
	cmd:=exec.Command(python,"-c",mlflowSDKSmokePython,server.URL+"/api/v1/mlflow-tracking",run.ID)
	cmd.Env=append(os.Environ(),"MLFLOW_TRACKING_TOKEN="+hex.EncodeToString(key),"MLFLOW_ENABLE_ASYNC_LOGGING=false","MLFLOW_HTTP_REQUEST_MAX_RETRIES=0","MLFLOW_ENABLE_TELEMETRY=false")
	output,err:=cmd.CombinedOutput()
	if err!=nil {t.Fatalf("SDK failed: %v %s",err,strings.ReplaceAll(string(output),hex.EncodeToString(key),"[redacted]"))}
	t.Log(strings.TrimSpace(string(output)))
	detail,err:=service.GetRun(context.Background(),actor,run.ID)
	if err!=nil||detail.Run.State!="FINISHED"||detail.Params["epochs"]!="3"||detail.Latest["loss"]!=0.25 {t.Fatalf("roundtrip mismatch: %+v %v",detail,err)}
}

const mlflowSDKSmokePython = `
import sys
import mlflow
from mlflow import MlflowClient
from mlflow.entities import Metric, Param, RunTag
assert mlflow.__version__ == "3.14.0", mlflow.__version__
client = MlflowClient(tracking_uri=sys.argv[1])
run_id = sys.argv[2]
assert client.get_run(run_id).info.run_id == run_id
client.log_param(run_id, "epochs", "3")
client.log_metric(run_id, "loss", 0.5, timestamp=1000, step=1)
client.set_tag(run_id, "review", "candidate")
client.log_batch(run_id, metrics=[Metric("loss", 0.25, 2000, 2)], params=[Param("batch", "8")], tags=[RunTag("purpose", "sdk-smoke")])
result = client.get_run(run_id)
assert result.data.metrics["loss"] == 0.25
assert result.data.params["epochs"] == "3"
client.set_terminated(run_id, status="FINISHED")
assert client.get_run(run_id).info.status == "FINISHED"
print("MLflow 3.14.0 SDK get/log_param/log_metric/set_tag/log_batch/set_terminated: passed")
`
