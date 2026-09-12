package modellifecycle

import (
 "bytes"
 "context"
 "crypto/sha256"
 "encoding/hex"
 "errors"
 "io"
 "testing"
)

type testSource struct { data []byte; reported int64; closed bool }
func (s *testSource) Read(context.Context,string,string)(io.ReadCloser,int64,error) { return &testReader{Reader: bytes.NewReader(s.data), closed: &s.closed},s.reported,nil }
type testReader struct { *bytes.Reader; closed *bool }
func (r *testReader) Close()error { *r.closed=true;return nil }
type testObjects struct { data map[int][]byte }
func (o *testObjects) Put(_ context.Context,_ string,i int,_ string,b []byte)error { o.data[i]=append([]byte(nil),b...);return nil }
func (o *testObjects) Get(_ context.Context,_ string,i int)(io.ReadCloser,int64,error) { b:=o.data[i];return io.NopCloser(bytes.NewReader(b)),int64(len(b)),nil }
func digest(b []byte)string { h:=sha256.Sum256(b);return hex.EncodeToString(h[:]) }
func TestDownloadChecksPartsBeforeWriting(t *testing.T) {
 good:=[]byte("snapshot"); o:=&testObjects{data:map[int][]byte{0:[]byte("tampered")}}
 s:=NewService(nil,nil,o); v:=Version{ID:"version",State:Ready,SizeBytes:int64(len(good)),SHA256:digest(good),Parts:[]Part{{Index:0,SizeBytes:int64(len(good)),SHA256:digest(good)}}}
 var out bytes.Buffer
 if err:=s.Download(context.Background(),v,&out);err==nil || out.Len()!=0 { t.Fatalf("corrupt download exposed bytes: %q %v",out.String(),err) }
 o.data[0]=good
 if err:=s.Download(context.Background(),v,&out);err!=nil || out.String()!=string(good) {t.Fatalf("download: %q %v",out.String(),err)}
}
func TestDownloadRejectsNonReadyAndCancellation(t *testing.T) {
 s:=NewService(nil,nil,&testObjects{data:map[int][]byte{}})
 if !errors.Is(s.Download(context.Background(),Version{},io.Discard),ErrNotReady) {t.Fatal("nonready visible")}
 ctx,cancel:=context.WithCancel(context.Background());cancel()
 if !errors.Is(s.Download(ctx,Version{State:Ready},io.Discard),context.Canceled) {t.Fatal("cancellation ignored")}
}
