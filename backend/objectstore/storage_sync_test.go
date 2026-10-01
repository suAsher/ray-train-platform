package objectstore

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type syncMetadataTransport func(*http.Request)(*http.Response,error)
func(f syncMetadataTransport)RoundTrip(r *http.Request)(*http.Response,error){return f(r)}
func TestStorageSyncMetadataUsesVersionIdentityAndGuardedRead(t *testing.T){
	store,err:=NewTOSStore(TOSConfig{Endpoint:"https://tos-cn-shanghai.volces.com",Region:"cn-shanghai",Bucket:"test-bucket",AccessKey:"test-ak",SecretKey:"test-sk",Transport:syncMetadataTransport(func(r *http.Request)(*http.Response,error){
		if r.Method!="HEAD"{t.Fatalf("unexpected network method %s",r.Method)}
		headers:=http.Header{"Content-Length":[]string{"12"},"Etag":[]string{"opaque-multipart-3"},"Last-Modified":[]string{"Thu, 01 Oct 2026 00:00:00 GMT"},"X-Tos-Version-Id":[]string{"version-1"},"X-Tos-Hash-Crc64ecma":[]string{"123"},"X-Tos-Meta-Sha256":[]string{"untrusted-user-metadata"}}
		return &http.Response{StatusCode:200,Header:headers,Body:io.NopCloser(strings.NewReader("")),Request:r},nil
	})})
	if err!=nil{t.Fatal(err)}
	object,err:=store.StorageSyncHead(context.Background(),"test-bucket","allowed/a")
	if err!=nil||object==nil||object.Size!=12||object.ETag!="opaque-multipart-3"||object.VersionID!="version-1"||object.SHA256!=""{t.Fatalf("identity incomplete or trusts caller checksum: %#v %v",object,err)}
	read,err:=store.StorageSyncReadURL(context.Background(),"test-bucket","allowed/a",object.ETag,object.VersionID)
	if err!=nil||read.Headers["If-Match"]!=object.ETag||!strings.Contains(read.URL,"versionId=version-1"){t.Fatalf("read not bound to snapshot: %#v %v",read,err)}
	if _,err=store.StorageSyncHead(context.Background(),"other-bucket","allowed/a");err==nil{t.Fatal("foreign bucket accepted")}
	if _,err=store.StorageSyncReadURL(context.Background(),"test-bucket","allowed/a","","");err==nil{t.Fatal("unconditional content read signed")}
}

func TestStorageSyncMetadataMissingCRCIsUnknownAndHeadersMatchWorker(t *testing.T){
	store,err:=NewTOSStore(TOSConfig{Endpoint:"https://tos-cn-shanghai.volces.com",Region:"cn-shanghai",Bucket:"test-bucket",AccessKey:"test-ak",SecretKey:"test-sk",Transport:syncMetadataTransport(func(r *http.Request)(*http.Response,error){
		return &http.Response{StatusCode:200,Header:http.Header{"Content-Length":[]string{"12"},"Etag":[]string{"\"opaque\""},"Last-Modified":[]string{"Thu, 01 Oct 2026 00:00:00 GMT"},"Content-Type":[]string{"application/json"},"Content-Encoding":[]string{"gzip"},"Content-Disposition":[]string{"attachment; filename=a+b%20c.json"},"Content-Language":[]string{"zh-CN"},"Cache-Control":[]string{"private"}},Body:io.NopCloser(strings.NewReader("")),Request:r},nil
	})});if err!=nil{t.Fatal(err)}
	object,err:=store.StorageSyncHead(context.Background(),"test-bucket","allowed/a");if err!=nil{t.Fatal(err)}
	if object.CRC64!=""{t.Fatalf("missing CRC became content proof: %q",object.CRC64)}
	encoded,_:=json.Marshal(object);var fields map[string]any;if err=json.Unmarshal(encoded,&fields);err!=nil{t.Fatal(err)}
	if fields["contentType"]!="application/json"||fields["contentEncoding"]!="gzip"||fields["contentDisposition"]!="attachment; filename=a+b c.json"||fields["contentLanguage"]!="zh-CN"||fields["cacheControl"]!="private"||fields["lastModified"]!="2026-10-01T00:00:00.000Z"||fields["etag"]!="opaque"{t.Fatalf("SDK and gateway fingerprints have different metadata: %s",encoded)}
}
