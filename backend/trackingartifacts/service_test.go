package trackingartifacts

import (
 "bytes"
 "context"
 "crypto/sha256"
 "encoding/hex"
 "errors"
 "io"
 "sort"
 "sync"
 "testing"
 "time"
)

func digest(b []byte) string { sum:=sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
var testScope = Scope{TenantID:"local",OwnerID:"owner",RunID:"0123456789abcdef0123456789abcdef"}

type memoryRepo struct { mu sync.Mutex; records map[string]Record }
func newMemoryRepo() *memoryRepo { return &memoryRepo{records:map[string]Record{}} }
func (r *memoryRepo) Reserve(_ context.Context, v Record) (Record,error) {
 r.mu.Lock(); defer r.mu.Unlock()
 var size int64; pending:=0
 for _, a:=range r.records { if a.Scope.TenantID!=v.Scope.TenantID || a.Scope.OwnerID!=v.Scope.OwnerID { continue }; if a.IdempotencyHash==v.IdempotencyHash { if a.Scope.RunID!=v.Scope.RunID || a.Name!=v.Name || a.SizeBytes!=v.SizeBytes || a.SHA256!=v.SHA256 { return Record{},ErrConflict }; return a,nil }; if a.State!="CANCELLED" {size+=a.SizeBytes}; if a.State=="PENDING" {pending++} }
 if size+v.SizeBytes>OwnerBudgetBytes || pending>=MaxPending {return Record{},ErrQuota}; r.records[v.ID]=v; return v,nil
}
func (r *memoryRepo) Get(_ context.Context, scope Scope, id string) (Record,error) {r.mu.Lock();defer r.mu.Unlock(); v,ok:=r.records[id];if !ok||v.Scope!=scope{return Record{},ErrNotFound};return v,nil}
func (r *memoryRepo) List(_ context.Context, scope Scope, cursor string, limit int) ([]Record,error) {r.mu.Lock();defer r.mu.Unlock();result:=[]Record{};for id,v:=range r.records {if v.Scope==scope&&id>cursor {result=append(result,v)}};sort.Slice(result,func(i,j int)bool{return result[i].ID<result[j].ID});if len(result)>limit{result=result[:limit]};return result,nil}
func (r *memoryRepo) Mutate(_ context.Context, scope Scope,id string, fn func(Record)(Record,error)) (Record,error) {r.mu.Lock();defer r.mu.Unlock();v,ok:=r.records[id];if !ok||v.Scope!=scope{return Record{},ErrNotFound}; next,err:=fn(v);if err!=nil{return Record{},err};r.records[id]=next;return next,nil}

type memoryObjects struct {values map[string][]byte; deleteErr error; reads int; corrupt bool}
func newMemoryObjects() *memoryObjects{return &memoryObjects{values:map[string][]byte{}}}
func (o *memoryObjects) Put(_ context.Context,id string,index int,sha string,b []byte)error{key:=partKey(id,index);if old,ok:=o.values[key];ok {if digest(old)!=sha{return ErrConflict};return nil};o.values[key]=append([]byte(nil),b...);return nil}
func(o *memoryObjects)Get(_ context.Context,id string,index int)(io.ReadCloser,int64,error){o.reads++;v,ok:=o.values[partKey(id,index)];if !ok{return nil,0,ErrUnavailable};if o.corrupt{v=bytes.Repeat([]byte("x"),len(v))};return io.NopCloser(bytes.NewReader(v)),int64(len(v)),nil}
func(o *memoryObjects)Delete(_ context.Context,id string,index int)error{if o.deleteErr!=nil{return o.deleteErr};delete(o.values,partKey(id,index));return nil}

func initArtifact(t *testing.T,s *Service,key string,b []byte)Artifact{t.Helper();a,err:=s.Init(context.Background(),testScope,key,InitInput{Name:"checkpoint.bin",SizeBytes:int64(len(b)),SHA256:digest(b)});if err!=nil{t.Fatal(err)};return a}
func TestArtifactRoundTripAndImmutableBoundaries(t *testing.T){
 ctx:=context.Background();r:=newMemoryRepo();o:=newMemoryObjects();s:=New(r,o);data:=[]byte("checkpoint-v1");a:=initArtifact(t,s,"stable-key-123456",data)
 again:=initArtifact(t,s,"stable-key-123456",data);if again.ID!=a.ID{t.Fatal("idempotency changed artifact")}
 if _,err:=s.Complete(ctx,testScope,a.ID);!errors.Is(err,ErrConflict){t.Fatalf("incomplete: %v",err)}
 for i:=0;i<2;i++{if _,err:=s.PutPart(ctx,testScope,a.ID,1,digest(data),bytes.NewReader(data));err!=nil{t.Fatal(err)}}
 changed:=bytes.Repeat([]byte("z"),len(data));if _,err:=s.PutPart(ctx,testScope,a.ID,1,digest(changed),bytes.NewReader(changed));!errors.Is(err,ErrConflict){t.Fatalf("overwrite: %v",err)}
 ready,err:=s.Complete(ctx,testScope,a.ID);if err!=nil||ready.State!="READY"{t.Fatalf("complete: %v %+v",err,ready)}
 _,body,err:=s.Download(ctx,testScope,a.ID);if err!=nil{t.Fatal(err)};got,err:=io.ReadAll(body);body.Close();if err!=nil||!bytes.Equal(got,data){t.Fatalf("download: %q %v",got,err)}
 if _,err:=s.Cancel(ctx,testScope,a.ID);!errors.Is(err,ErrConflict){t.Fatalf("cancel ready: %v",err)}
 other:=testScope;other.OwnerID="other";if _,err:=s.Get(ctx,other,a.ID);!errors.Is(err,ErrNotFound){t.Fatalf("other owner: %v",err)}
 other=testScope;other.RunID="ffffffffffffffffffffffffffffffff";if _,err:=s.Get(ctx,other,a.ID);!errors.Is(err,ErrNotFound){t.Fatalf("other run: %v",err)}
}
func TestArtifactValidationAndQuota(t *testing.T){
 ctx:=context.Background();s:=New(newMemoryRepo(),newMemoryObjects());good:=InitInput{Name:"model.bin",SizeBytes:1,SHA256:digest([]byte("a"))}
 for _,name:=range []string{"../secret","dir/file","a\\b",".","x\nheader", ""}{v:=good;v.Name=name;if _,err:=s.Init(ctx,testScope,"stable-key-123456",v);!errors.Is(err,ErrInvalid){t.Errorf("name %q: %v",name,err)}}
 for _,size:=range []int64{0,-1,MaxFileBytes+1}{v:=good;v.SizeBytes=size;if _,err:=s.Init(ctx,testScope,"stable-key-123456",v);!errors.Is(err,ErrInvalid){t.Errorf("size %d: %v",size,err)}}
 for i:=0;i<MaxPending;i++{if _,err:=s.Init(ctx,testScope,"key-"+string(rune('a'+i))+"-1234567890",good);err!=nil{t.Fatal(err)}}
 if _,err:=s.Init(ctx,testScope,"one-too-many-1234",good);!errors.Is(err,ErrQuota){t.Fatalf("pending quota: %v",err)}
}
func TestArtifactPartAndCompletionHashValidation(t *testing.T){
 ctx:=context.Background();o:=newMemoryObjects();s:=New(newMemoryRepo(),o);data:=[]byte("abc");a:=initArtifact(t,s,"stable-key-123456",data)
 for _,index:=range []int{0,2}{if _,err:=s.PutPart(ctx,testScope,a.ID,index,digest(data),bytes.NewReader(data));!errors.Is(err,ErrInvalid){t.Errorf("index %d: %v",index,err)}}
 for _,body:=range [][]byte{[]byte("ab"),[]byte("abcd"),[]byte("xyz")}{if _,err:=s.PutPart(ctx,testScope,a.ID,1,digest(data),bytes.NewReader(body));!errors.Is(err,ErrInvalid){t.Errorf("invalid body %q: %v",body,err)}}
 if _,err:=s.PutPart(ctx,testScope,a.ID,1,digest(data),bytes.NewReader(data));err!=nil{t.Fatal(err)}
 o.corrupt=true;if _,err:=s.Complete(ctx,testScope,a.ID);!errors.Is(err,ErrConflict){t.Fatalf("corrupt total hash: %v",err)}
 o.corrupt=false;if _,err:=s.Complete(ctx,testScope,a.ID);err!=nil{t.Fatal(err)}
}
func TestArtifactCancelRetainsReservationUntilCleanupSucceeds(t *testing.T){
 ctx:=context.Background();r:=newMemoryRepo();o:=newMemoryObjects();s:=New(r,o);data:=[]byte("abc");a:=initArtifact(t,s,"stable-key-123456",data)
 if _,err:=s.PutPart(ctx,testScope,a.ID,1,digest(data),bytes.NewReader(data));err!=nil{t.Fatal(err)}
 o.deleteErr=ErrUnavailable;if _,err:=s.Cancel(ctx,testScope,a.ID);!errors.Is(err,ErrUnavailable){t.Fatal(err)};v,_:=s.Get(ctx,testScope,a.ID);if v.State=="CANCELLED"{t.Fatal("released budget before cleanup")}
 o.deleteErr=nil;v,err:=s.Cancel(ctx,testScope,a.ID);if err!=nil||v.State!="CANCELLED"||len(o.values)!=0{t.Fatalf("cancel: %+v %v",v,err)}
 if _,err:=s.PutPart(ctx,testScope,a.ID,1,digest(data),bytes.NewReader(data));!errors.Is(err,ErrConflict){t.Fatal(err)}
}
func TestArtifactExpiryAndStreamingMultipart(t *testing.T){
 ctx:=context.Background();r:=newMemoryRepo();o:=newMemoryObjects();s:=New(r,o);data:=append(bytes.Repeat([]byte("a"),int(PartSizeBytes)),[]byte("tail")...);a:=initArtifact(t,s,"stable-key-123456",data)
 for i,p:=range [][]byte{data[:PartSizeBytes],data[PartSizeBytes:]}{if _,err:=s.PutPart(ctx,testScope,a.ID,i+1,digest(p),bytes.NewReader(p));err!=nil{t.Fatal(err)}}
 if _,err:=s.Complete(ctx,testScope,a.ID);err!=nil{t.Fatal(err)};o.reads=0;_,body,err:=s.Download(ctx,testScope,a.ID);if err!=nil{t.Fatal(err)};if o.reads>1{t.Fatal("download eagerly opened all parts")};got,err:=io.ReadAll(body);body.Close();if err!=nil||!bytes.Equal(got,data){t.Fatalf("multi download %v",err)}
 b:=initArtifact(t,s,"expiry-key-123456",[]byte("a"));v:=r.records[b.ID];v.ExpiresAt=time.Now().Add(-time.Hour);r.records[b.ID]=v;if _,err:=s.PutPart(ctx,testScope,b.ID,1,digest([]byte("a")),bytes.NewReader([]byte("a")));!errors.Is(err,ErrConflict){t.Fatalf("expired write %v",err)};if _,err:=s.Cancel(ctx,testScope,b.ID);err!=nil{t.Fatal(err)}
}

func TestArtifactDeclaredBudgetAndIdempotencyConflict(t *testing.T){
 ctx:=context.Background();r:=newMemoryRepo();s:=New(r,newMemoryObjects());input:=InitInput{Name:"large.bin",SizeBytes:MaxFileBytes,SHA256:digest([]byte("declaration"))}
 for i:=0;i<5;i++{if _,err:=s.Init(ctx,testScope,"large-"+string(rune('a'+i))+"-12345678",input);err!=nil{t.Fatal(err)}}
 if _,err:=s.Init(ctx,testScope,"over-budget-123456",input);!errors.Is(err,ErrQuota){t.Fatalf("size quota %v",err)}
 changed:=input;changed.Name="different.bin";if _,err:=s.Init(ctx,testScope,"large-a-12345678",changed);!errors.Is(err,ErrConflict){t.Fatalf("changed declaration %v",err)}
 other:=testScope;other.RunID="ffffffffffffffffffffffffffffffff";if _,err:=s.Init(ctx,other,"large-a-12345678",input);!errors.Is(err,ErrConflict){t.Fatalf("key different run %v",err)}
}
func TestArtifactGetListAndUnavailableBoundaries(t *testing.T){
 ctx:=context.Background();s:=New(newMemoryRepo(),newMemoryObjects());a:=initArtifact(t,s,"stable-key-123456",[]byte("a"))
 page,err:=s.List(ctx,testScope,"",100);if err!=nil||len(page.Items)!=1||page.Items[0].ID!=a.ID{t.Fatalf("list %+v %v",page,err)}
 for _,limit:=range []int{0,101}{if _,err:=s.List(ctx,testScope,"",limit);!errors.Is(err,ErrInvalid){t.Fatal(err)}}
 if _,err:=s.List(ctx,testScope,"../../secret",1);!errors.Is(err,ErrInvalid){t.Fatal(err)}
 if _,err:=s.Get(ctx,testScope,"bad-id");!errors.Is(err,ErrInvalid){t.Fatal(err)}
 if _,err:=s.Get(ctx,testScope,"ffffffffffffffffffffffffffffffff");!errors.Is(err,ErrNotFound){t.Fatal(err)}
 if _,_,err:=s.Download(ctx,testScope,a.ID);!errors.Is(err,ErrConflict){t.Fatal(err)}
 if _,err:=New(nil,nil).Get(ctx,testScope,a.ID);!errors.Is(err,ErrUnavailable){t.Fatal(err)}
 if _,err:=s.PutPart(ctx,testScope,a.ID,1,digest([]byte("a")),nil);!errors.Is(err,ErrInvalid){t.Fatal(err)}
 scope:=testScope;scope.OwnerID="bad\nowner";if _,err:=s.Get(ctx,scope,a.ID);!errors.Is(err,ErrInvalid){t.Fatal(err)}
}
func TestArtifactDownloadDetectsCorruptionAndClose(t *testing.T){
 ctx:=context.Background();o:=newMemoryObjects();s:=New(newMemoryRepo(),o);data:=[]byte("abc");a:=initArtifact(t,s,"stable-key-123456",data)
 if _,err:=s.PutPart(ctx,testScope,a.ID,1,digest(data),bytes.NewReader(data));err!=nil{t.Fatal(err)};if _,err:=s.Complete(ctx,testScope,a.ID);err!=nil{t.Fatal(err)}
 o.corrupt=true;_,body,err:=s.Download(ctx,testScope,a.ID);if err!=nil{t.Fatal(err)};if _,err:=io.ReadAll(body);!errors.Is(err,ErrUnavailable){t.Fatalf("corrupt read: %v",err)};if err:=body.Close();err!=nil{t.Fatal(err)}
 o.corrupt=false;_,body,err=s.Download(ctx,testScope,a.ID);if err!=nil{t.Fatal(err)};body.Close();if _,err:=body.Read(make([]byte,1));err!=io.EOF{t.Fatalf("closed read: %v",err)}
}

func TestArtifactWholeFileDigestAndCursor(t *testing.T){
 ctx:=context.Background();s:=New(newMemoryRepo(),newMemoryObjects());a:=initArtifact(t,s,"stable-key-123456",[]byte("abc"))
 if _,err:=s.PutPart(ctx,testScope,a.ID,1,digest([]byte("xyz")),bytes.NewReader([]byte("xyz")));err!=nil{t.Fatal(err)}
 if _,err:=s.Complete(ctx,testScope,a.ID);!errors.Is(err,ErrConflict){t.Fatalf("whole digest mismatch: %v",err)}
 initArtifact(t,s,"stable-key-223456",[]byte("abc"));initArtifact(t,s,"stable-key-323456",[]byte("abc"))
 first,err:=s.List(ctx,testScope,"",2);if err!=nil||len(first.Items)!=2||first.NextCursor==""{t.Fatalf("first page %+v %v",first,err)}
 second,err:=s.List(ctx,testScope,first.NextCursor,2);if err!=nil||len(second.Items)!=1||second.NextCursor!=""{t.Fatalf("second page %+v %v",second,err)}
 for _,v:=range first.Items{if v.ID==second.Items[0].ID{t.Fatal("cursor returned duplicate")}}
}
