package modellifecycle

import (
 "bytes"
 "context"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "errors"
 "io"
 "testing"
 "strings"
 "time"
)

type testSource struct { data []byte; reported int64; closed bool; etag string }
func (s *testSource) Read(context.Context,string,string)(io.ReadCloser,int64,string,error) { return &testReader{Reader: bytes.NewReader(s.data), closed: &s.closed},s.reported,s.etag,nil }
type testReader struct { *bytes.Reader; closed *bool }
func (r *testReader) Close()error { *r.closed=true;return nil }
type testObjects struct { data map[int][]byte }
func (o *testObjects) Put(_ context.Context,_ string,i int,_ string,b []byte)error { o.data[i]=append([]byte(nil),b...);return nil }
func (o *testObjects) Get(_ context.Context,_ string,i int)(io.ReadCloser,int64,error) { b:=o.data[i];return io.NopCloser(bytes.NewReader(b)),int64(len(b)),nil }
func digest(b []byte)string { h:=sha256.Sum256(b);return hex.EncodeToString(h[:]) }
func TestDownloadChecksPartsBeforeWriting(t *testing.T) {
 good:=[]byte("snapshot"); o:=&testObjects{data:map[int][]byte{0:[]byte("tampered")}}
 s:=NewService(nil,nil,o); v:=Version{SourceETag:"source-etag",ID:"version",State:Ready,SizeBytes:int64(len(good)),SHA256:digest(good),Parts:[]Part{{Index:0,SizeBytes:int64(len(good)),SHA256:digest(good)}}}
 var out bytes.Buffer
 if err:=s.Download(context.Background(),v,&out);err==nil || out.Len()!=0 { t.Fatalf("corrupt download exposed bytes: %q %v",out.String(),err) }
 o.data[0]=good
 if err:=s.Download(context.Background(),v,&out);err!=nil || out.String()!=string(good) {t.Fatalf("download: %q %v",out.String(),err)}
}
func TestDownloadRejectsNonReadyAndCancellation(t *testing.T) {
 s:=NewService(nil,nil,&testObjects{data:map[int][]byte{}})
 if !errors.Is(s.Download(context.Background(),Version{SourceETag:"source-etag",},io.Discard),ErrNotReady) {t.Fatal("nonready visible")}
 ctx,cancel:=context.WithCancel(context.Background());cancel()
 if !errors.Is(s.Download(ctx,Version{SourceETag:"source-etag",State:Ready},io.Discard),context.Canceled) {t.Fatal("cancellation ignored")}
}

type fakeRepository struct {
 Repository
 previous Version
 findErr error
 reserved Version
 reserveErr error
 finished Version
 finishErr error
 renewErr error
 claims int
 failClaim bool
 runCancel context.CancelFunc
}
func (r *fakeRepository) FindVersionRequest(context.Context,string,string,string)(Version,error){return r.previous,r.findErr}
func (r *fakeRepository) ReserveVersion(_ context.Context,v Version)(Version,error){r.reserved=v;v.State=Pending;return v,r.reserveErr}
func (r *fakeRepository) FinishVersion(_ context.Context,id,lease,state string,parts []Part,hash string,_ time.Time)error{r.finished=Version{SourceETag:"source-etag",ID:id,LeaseID:lease,State:state,Parts:parts,SHA256:hash};return r.finishErr}
func (r *fakeRepository) RenewVersion(context.Context,string,string,time.Time,time.Time)error{return r.renewErr}
func (r *fakeRepository) ClaimVersion(_ context.Context,lease string,_ time.Time,_ time.Time)(Version,error){r.claims++;if r.claims==1 && !r.failClaim{v:=r.reserved;v.LeaseID=lease;return v,nil};r.runCancel();return Version{SourceETag:"source-etag",},ErrNotFound}
func versionRequest()VersionRequest{return VersionRequest{ModelID:"model",CreatorID:"owner",CreatorName:"Owner",JobID:"job",JobName:"Job",FileName:"weights.bin",SourceRoot:"/private",RelativePath:"weights.bin",IdempotencyKey:"unique-key"}}
func TestRequestVersionPersistsOnlyMetadataAndIdempotency(t *testing.T) {
 src:=&testSource{etag:"source-etag",data:[]byte("weights"),reported:7};repo:=&fakeRepository{findErr:ErrNotFound};objects:=&testObjects{data:map[int][]byte{}};s:=NewService(repo,src,objects)
 request:=versionRequest();v,err:=s.RequestVersion(context.Background(),request)
 if err!=nil || v.State!=Pending || !src.closed || len(objects.data)!=0 || v.SizeBytes!=7 || !ValidID(v.ID) || len(v.RequestSHA256)!=64{t.Fatalf("request did not reserve metadata: %+v %v",v,err)}
 repo.previous=v;repo.findErr=nil;src.closed=false;src.data=nil
 reused,err:=s.RequestVersion(context.Background(),request)
 if err!=nil || reused.ID!=v.ID || src.closed{t.Fatal("idempotent retry reread deleted source")}
 request.Description="changed"
 if _,err:=s.RequestVersion(context.Background(),request);!errors.Is(err,ErrConflict){t.Fatalf("changed retry: %v",err)}
}
type brokenSource struct { err error; nilReader bool }
func (s brokenSource) Read(context.Context,string,string)(io.ReadCloser,int64,string,error){return nil,1,"source-etag",s.err}
func TestRequestVersionValidationAndReadFailures(t *testing.T) {
 request:=versionRequest();ctx:=context.Background();repo:=&fakeRepository{findErr:ErrNotFound};objects:=&testObjects{}
 for _,sourceErr:=range []error{ErrUnavailable,ErrNotFound,context.Canceled}{
  s:=NewService(repo,brokenSource{err:sourceErr},objects)
  if _,err:=s.RequestVersion(ctx,request);!errors.Is(err,sourceErr){t.Fatalf("read error changed: %v",err)}
 }
 if _,err:=NewService(repo,brokenSource{},objects).RequestVersion(ctx,request);!errors.Is(err,ErrUnavailable){t.Fatal("nil stream accepted")}
 for _,size:=range []int64{0,MaxFileSize+1}{src:=&testSource{etag:"source-etag",reported:size};if _,err:=NewService(repo,src,objects).RequestVersion(ctx,request);!errors.Is(err,ErrInvalid) || !src.closed{t.Fatalf("size %d: %v",size,err)}}
 if _,err:=NewService(nil,nil,nil).RequestVersion(ctx,request);!errors.Is(err,ErrNotReady){t.Fatal(err)}
 for _,modify:=range []func(*VersionRequest){
  func(r *VersionRequest){r.RelativePath="../outside"},func(r *VersionRequest){r.RelativePath="/absolute"},func(r *VersionRequest){r.FileName="a/b"},func(r *VersionRequest){r.IdempotencyKey="bad key"},func(r *VersionRequest){r.CodeSHA256="bad"},func(r *VersionRequest){r.CodeCommit="bad"},func(r *VersionRequest){r.DatasetID="without provenance"},func(r *VersionRequest){r.DatasetAssociation="untrusted"},
 }{bad:=request;modify(&bad);if _,err:=NewService(repo,&testSource{etag:"source-etag",reported:1},objects).RequestVersion(ctx,bad);!errors.Is(err,ErrInvalid){t.Fatalf("invalid request accepted: %+v %v",bad,err)}}
 request.DatasetID="dataset";request.DatasetVersionID="version";request.DatasetManifestSHA256=strings.Repeat("a",64);request.DatasetAssociation="training-record"
 if !validRequest(request){t.Fatal("valid frozen provenance rejected")}
 repo.findErr=ErrUnavailable
 if _,err:=NewService(repo,&testSource{etag:"source-etag",reported:1},objects).RequestVersion(ctx,request);!errors.Is(err,ErrUnavailable){t.Fatal(err)}
}
type failingObjects struct { Objects; err error }
func (o failingObjects) Put(context.Context,string,int,string,[]byte)error{return o.err}
func TestCopyStreamsMultiplePartsAndWholeDigest(t *testing.T) {
 data:=bytes.Repeat([]byte("x"),PartSize+13);src:=&testSource{etag:"source-etag",data:data,reported:int64(len(data))};objects:=&testObjects{data:map[int][]byte{}}
 service:=NewService(nil,src,objects);v:=Version{SourceETag:"source-etag",ID:"snapshot",SizeBytes:int64(len(data)),SourceRoot:"/source",RelativePath:"weights"}
 parts,hash,err:=service.copy(context.Background(),v)
 if err!=nil || hash!=digest(data) || !src.closed || len(parts)!=2 || parts[0].SizeBytes!=PartSize || parts[1].SizeBytes!=13 || parts[0].SHA256!=digest(data[:PartSize]) || parts[1].SHA256!=digest(data[PartSize:]) {t.Fatalf("multipart mismatch: %+v %s %v",parts,hash,err)}
 // Snapshot download remains valid after the original source disappears.
 src.data=nil;v.State=Ready;v.Parts=parts;v.SHA256=hash;var out bytes.Buffer
 if err:=service.Download(context.Background(),v,&out);err!=nil || !bytes.Equal(out.Bytes(),data){t.Fatalf("independent snapshot failed: %v",err)}
}
func TestCopyRejectsChangedSourceLengthAndUploadFailure(t *testing.T) {
 for _,tc:=range []struct{name string;data string;reported,size int64}{
  {"short","abc",4,4},{"long","abcde",4,4},{"changed stat","abcd",5,4},{"zero","",0,0},
 }{t.Run(tc.name,func(t *testing.T){src:=&testSource{etag:"source-etag",data:[]byte(tc.data),reported:tc.reported};s:=NewService(nil,src,&testObjects{data:map[int][]byte{}});if _,_,err:=s.copy(context.Background(),Version{SourceETag:"source-etag",SizeBytes:tc.size});err==nil || !src.closed{t.Fatalf("invalid copy succeeded %v",err)}})}
 src:=&testSource{etag:"source-etag",data:[]byte("a"),reported:1};s:=NewService(nil,src,failingObjects{err:ErrUnavailable})
 if _,_,err:=s.copy(context.Background(),Version{SourceETag:"source-etag",SizeBytes:1});!errors.Is(err,ErrUnavailable){t.Fatal(err)}
 ctx,cancel:=context.WithCancel(context.Background());cancel()
 if _,_,err:=s.copy(ctx,Version{SourceETag:"source-etag",SizeBytes:1});!errors.Is(err,context.Canceled){t.Fatal(err)}
 if _,_,err:=NewService(nil,brokenSource{err:ErrNotFound},nil).copy(context.Background(),Version{SourceETag:"source-etag",SizeBytes:1});!errors.Is(err,ErrNotFound){t.Fatal(err)}
}
func TestProcessPublishesOnlyCompleteSnapshot(t *testing.T) {
 for _,failure:=range []bool{false,true}{
  repo:=&fakeRepository{};src:=&testSource{etag:"source-etag",data:[]byte("weights"),reported:7};var objects Objects=&testObjects{data:map[int][]byte{}}
  if failure{objects=failingObjects{err:ErrUnavailable}}
  s:=NewService(repo,src,objects);s.process(context.Background(),Version{SourceETag:"source-etag",ID:"version",LeaseID:"owned-lease",SizeBytes:7})
  want:=Ready;if failure{want=Failed}
  if repo.finished.State!=want || repo.finished.LeaseID!="owned-lease" || (failure && (repo.finished.SHA256!="" || len(repo.finished.Parts)!=0)){t.Fatalf("bad final state: %+v",repo.finished)}
 }
 ctx,cancel:=context.WithCancel(context.Background());cancel();repo:=&fakeRepository{}
 NewService(repo,&testSource{etag:"source-etag",data:[]byte("a"),reported:1},&testObjects{}).process(ctx,Version{SourceETag:"source-etag",SizeBytes:1})
 if repo.finished.State!=Failed{t.Fatal("cancelled snapshot published")}
}
func TestRunDrainsPersistentRequestsAndStops(t *testing.T) {
 for _,empty:=range []bool{false,true}{
  ctx,cancel:=context.WithCancel(context.Background());repo:=&fakeRepository{reserved:Version{SourceETag:"source-etag",ID:"persisted",SizeBytes:1},runCancel:cancel,failClaim:empty}
  s:=NewService(repo,&testSource{etag:"source-etag",data:[]byte("a"),reported:1},&testObjects{data:map[int][]byte{}});s.Run(ctx);cancel()
  if !empty && repo.finished.State!=Ready{t.Fatalf("persisted work not drained: %+v",repo.finished)}
 }
 NewService(nil,nil,nil).Run(context.Background())
}
func TestDownloadFullHashManifestAndLengthChecks(t *testing.T) {
 data:=[]byte("weights");objects:=&testObjects{data:map[int][]byte{0:data}};service:=NewService(nil,nil,objects)
 v:=Version{SourceETag:"source-etag",ID:"v",State:Ready,SizeBytes:int64(len(data)),SHA256:digest(data),Parts:[]Part{{Index:0,SizeBytes:int64(len(data)),SHA256:digest(data)}}}
 bad:=v;bad.SHA256=strings.Repeat("a",64);var out bytes.Buffer
 if err:=service.Download(context.Background(),bad,&out);err==nil || out.Len()!=0{t.Fatal("whole hash mismatch released final part")}
 bad=v;bad.Parts=nil;if err:=service.Download(context.Background(),bad,&out);err==nil{t.Fatal("missing parts accepted")}
 bad=v;bad.Parts=[]Part{{Index:1,SizeBytes:7,SHA256:digest(data)}};if err:=service.Download(context.Background(),bad,&out);err==nil{t.Fatal("wrong order accepted")}
 objects.data[0]=[]byte("longer weights");if err:=service.Download(context.Background(),v,&out);err==nil{t.Fatal("wrong length accepted")}
 if ValidID("../../outside") || ValidID("NOT-A-UUID") {t.Fatal("unsafe object IDs accepted")}
}
func TestVersionJSONDoesNotExposeStorageOrLease(t *testing.T) {
 raw,err:=json.Marshal(Version{SourceETag:"private-etag",SourceRoot:"private-source",RelativePath:"private-file",LeaseID:"private-lease",IdempotencyKey:"private-key",RequestSHA256:"private-hash",Parts:[]Part{{SHA256:"private-part"}}});if err!=nil{t.Fatal(err)}
 if strings.Contains(string(raw),"private-"){t.Fatalf("private storage metadata exposed: %s",raw)}
}

type waitingSource struct{}
func (waitingSource) Read(ctx context.Context,_ string,_ string)(io.ReadCloser,int64,string,error){return &waitingReader{ctx:ctx},1,"source-etag",nil}
type waitingReader struct{ctx context.Context}
func (r *waitingReader) Read([]byte)(int,error){<-r.ctx.Done();return 0,r.ctx.Err()}
func (r *waitingReader) Close()error{return nil}
func TestLeaseLossCancelsCopyAndNeverPublishesReady(t *testing.T) {
 ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel()
 repo:=&fakeRepository{renewErr:ErrConflict,finishErr:ErrConflict}
 s:=NewService(repo,waitingSource{},&testObjects{data:map[int][]byte{}});s.heartbeatInterval=time.Millisecond
 s.process(ctx,Version{SourceETag:"source-etag",ID:"leased",LeaseID:"stale",SizeBytes:1})
 if repo.finished.State!=Failed || ctx.Err()!=nil{t.Fatalf("lost lease failed to stop copy: %+v %v",repo.finished,ctx.Err())}
}

func TestVersionDescriptionUsesCharacterLimit(t *testing.T) {
 r:=versionRequest();r.Description=strings.Repeat("模",4000)
 if !validRequest(r){t.Fatal("4000 Chinese characters rejected")}
 r.Description+="型";if validRequest(r){t.Fatal("4001 characters accepted")}
}

func TestQueuedSnapshotRejectsSameSizeSourceReplacement(t *testing.T) {
 src:=&testSource{data:[]byte("first"),reported:5,etag:"original-etag"}
 repo:=&fakeRepository{findErr:ErrNotFound};objects:=&testObjects{data:map[int][]byte{}};s:=NewService(repo,src,objects)
 v,err:=s.RequestVersion(context.Background(),versionRequest());if err!=nil{t.Fatal(err)}
 if v.SourceETag!="original-etag"{t.Fatal("request did not freeze source identity")}
 src.data=[]byte("other");src.etag="replacement-etag";src.closed=false
 s.process(context.Background(),v)
 if repo.finished.State!=Failed || len(objects.data)!=0 || !src.closed{t.Fatalf("replaced source was copied: %+v objects=%d",repo.finished,len(objects.data))}
 repo.previous=v;repo.findErr=nil;src.closed=false
 retry,err:=s.RequestVersion(context.Background(),versionRequest())
 if err!=nil || retry.ID!=v.ID || retry.SourceETag!="original-etag" || src.closed{t.Fatalf("retry changed frozen source: %+v %v",retry,err)}
}
func TestSnapshotRequiresNonemptySourceETag(t *testing.T) {
 for _,etag:=range []string{"","  "}{
  src:=&testSource{data:[]byte("a"),reported:1,etag:etag};repo:=&fakeRepository{findErr:ErrNotFound};objects:=&testObjects{data:map[int][]byte{}};s:=NewService(repo,src,objects)
  if _,err:=s.RequestVersion(context.Background(),versionRequest());!errors.Is(err,ErrUnavailable) || !src.closed{t.Fatalf("empty ETag accepted: %v",err)}
  src.closed=false
  if _,_,err:=s.copy(context.Background(),Version{SizeBytes:1,SourceETag:"original"});!errors.Is(err,ErrUnavailable) || !src.closed || len(objects.data)!=0{t.Fatalf("worker accepted unidentified source: %v",err)}
 }
}
