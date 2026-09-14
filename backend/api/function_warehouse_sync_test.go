package api

import (
 "context"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"

 "github.com/gin-gonic/gin"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/domain"
 fw "ray-train-platform-backend/functionwarehouse"
 ws "ray-train-platform-backend/warehousesync"
)

type warehouseSyncAPIStore struct {
 ws.Store
 operation ws.Operation
 creates,resumes,cancels int
}
func(s *warehouseSyncAPIStore) Create(_ context.Context,op ws.Operation)(ws.Operation,error){s.creates++;s.operation=op;return op,nil}
func(s *warehouseSyncAPIStore) Get(context.Context,string)(ws.Operation,error){return s.operation,nil}
func(s *warehouseSyncAPIStore) Resume(_ context.Context,_ string,_ ws.Actor,credential []byte)(ws.Operation,error){s.resumes++;s.operation.State=ws.Queued;s.operation.Credential=credential;return s.operation,nil}
func(s *warehouseSyncAPIStore) Cancel(context.Context,string,ws.Actor)(ws.Operation,error){s.cancels++;s.operation.State=ws.Canceled;s.operation.Credential=nil;return s.operation,nil}

type warehouseSyncAPIUpstream struct{ws.Upstream;checks int}
func(u *warehouseSyncAPIUpstream) GetWarehouse(context.Context,string,string)(fw.Warehouse,error){u.checks++;return fw.Warehouse{ID:"warehouse-1",GroupID:"d49a984edd3d0648a43ab050d3cc0262",PermissionCodes:[]string{"edit"}},nil}
func(u *warehouseSyncAPIUpstream) ListModelTypes(context.Context,string,string)([]fw.ModelType,error){return []fw.ModelType{{ID:"model-type-1"}},nil}

func warehouseSyncAPIRouter(t *testing.T,job domain.TrainingJob,enabled bool)(*gin.Engine,*warehouseSyncAPIStore,*warehouseSyncAPIUpstream){
 t.Helper();gin.SetMode(gin.TestMode)
 store:=&warehouseSyncAPIStore{};upstream:=&warehouseSyncAPIUpstream{}
 h:=NewHandler(&fakeJobRepository{jobs:[]domain.TrainingJob{job}},Options{Models:&modelStoreFake{}})
 if enabled {
  service,err:=ws.NewService(store,warehouseSyncSource{h:h},map[fw.Environment]ws.Upstream{fw.Development:upstream},[]byte(strings.Repeat("p",32)))
  if err!=nil{t.Fatal(err)};h.warehouseSync=service
 }
 r:=gin.New();r.Use(auth.OAuth2ProxyMiddleware(&warehouseAPIOIDC{},warehouseAPIAccounts{},warehouseAPIPAT{},warehouseAPILocal{},true,auth.OAuth2ProxyOptions{}))
 h.RegisterFunctionWarehouseRoutes(r.Group("/api/v1"))
 return r,store,upstream
}
func warehouseSyncAPIPost(r http.Handler,path,body,bearer string)*httptest.ResponseRecorder{
 req:=httptest.NewRequest(http.MethodPost,"/api/v1/function-warehouse/syncs"+path,strings.NewReader(body))
 req.Header.Set("Content-Type","application/json");req.Header.Set("Idempotency-Key","sync-request-1")
 req.Header.Set("X-Auth-Request-Access-Token","verified-proxy")
 if bearer!=""{req.Header.Set("Authorization","Bearer "+bearer)}
 w:=httptest.NewRecorder();r.ServeHTTP(w,req);return w
}
const warehouseSyncAPIInput=`{"jobId":"job-a","environment":"development","warehouseId":"warehouse-1","modelTypeId":"model-type-1","version":"v1","paths":["checkpoints/best.pth"],"automatic":false}`
func TestWarehouseSyncCreateChecksJobOwnerTenantAndTerminalState(t *testing.T){
 for _,tc:=range []struct{name,owner,tenant string;state domain.State;automatic bool;want int}{
  {"foreign owner","bob","local",domain.StateSucceeded,false,403},
  {"foreign team","alice","other",domain.StateSucceeded,false,403},
  {"running manual","alice","local",domain.StateRunning,false,409},
  {"running automatic","alice","local",domain.StateRunning,true,202},
  {"success manual","alice","local",domain.StateSucceeded,false,202},
  {"failed manual","alice","local",domain.StateFailed,false,202},
  {"canceled manual","alice","local",domain.StateCanceled,false,202},
  {"timedout manual","alice","local",domain.StateTimedOut,false,202},
 }{
  t.Run(tc.name,func(t *testing.T){
   job:=warehouseSourceJob();job.UserID=tc.owner;job.TenantID=tc.tenant;job.ObservedState=tc.state
   r,store,upstream:=warehouseSyncAPIRouter(t,job,true)
   body:=warehouseSyncAPIInput;if tc.automatic{body=strings.Replace(body,`"automatic":false`,`"automatic":true`,1)}
   w:=warehouseSyncAPIPost(r,"",body,"")
   if w.Code!=tc.want{t.Fatalf("status %d: %s",w.Code,w.Body.String())}
   if tc.want!=202&&(store.creates!=0||upstream.checks!=0){t.Fatal("forbidden source reached target or persisted")}
   if tc.want==202&&(store.creates!=1||store.operation.OwnerID!="alice"||store.operation.TenantID!="local"||len(store.operation.Credential)==0){t.Fatal("accepted sync lost authenticated owner or durable encrypted authorization")}
   if strings.Contains(w.Body.String(),"verified-proxy")||strings.Contains(w.Body.String(),"credential"){t.Fatal("sync response leaked credential")}
  })
 }
}
func TestWarehouseSyncDisabledAndPATCannotCreate(t *testing.T){
 for _,tc:=range []struct{name,bearer string;enabled bool;want int}{
  {"disabled","",false,503},{"PAT","rpt_test",true,403},{"local","rls_test",true,401},
 }{
  t.Run(tc.name,func(t *testing.T){r,store,upstream:=warehouseSyncAPIRouter(t,warehouseSourceJob(),tc.enabled);w:=warehouseSyncAPIPost(r,"",warehouseSyncAPIInput,tc.bearer)
   if w.Code!=tc.want||store.creates!=0||upstream.checks!=0{t.Fatalf("invalid delegation status %d: %s",w.Code,w.Body.String())}
  })
 }
}
func TestWarehouseSyncRetryAndCancelRequireOwnerAndCurrentTeam(t *testing.T){
 for _,action:=range []string{"retry","cancel"}{
  for _,scope:=range []string{"owner","foreign owner","foreign tenant"}{
   t.Run(action+" "+scope,func(t *testing.T){
    r,store,upstream:=warehouseSyncAPIRouter(t,warehouseSourceJob(),true)
    state:=ws.WaitingSource;if action=="retry"{state=ws.WaitingReauth}
    store.operation=ws.Operation{ID:"sync-1",OwnerID:"alice",OwnerName:"alice",TenantID:"local",JobID:"job-a",State:state,Environment:fw.Development,WarehouseID:"warehouse-1",ModelTypeID:"model-type-1"}
    if scope=="foreign owner"{store.operation.OwnerID="bob"};if scope=="foreign tenant"{store.operation.TenantID="other"}
    w:=warehouseSyncAPIPost(r,"/sync-1/"+action,"","")
    want:=200;if action=="retry"{want=202};if scope!="owner"{want=403}
    if w.Code!=want{t.Fatalf("status %d: %s",w.Code,w.Body.String())}
    if scope!="owner"&&(store.resumes!=0||store.cancels!=0||upstream.checks!=0){t.Fatal("foreign sync mutation reached upstream/store")}
    if scope=="owner"&&store.resumes+store.cancels!=1{t.Fatal("authorized action was not performed")}
   })
  }
 }
}
