package objectstore

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos/enum"
)

// These methods provide read capabilities only. The caller must additionally
// confine every key to its approved attempt snapshot before invoking them.
type StorageSyncObject struct {
	Key string `json:"key"`
	Size int64 `json:"size"`
	ETag string `json:"etag"`
	VersionID string `json:"versionId,omitempty"`
	CRC64 string `json:"crc64,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	LastModified string `json:"lastModified"`
	ContentType string `json:"contentType,omitempty"`
	ContentEncoding string `json:"contentEncoding,omitempty"`
	ContentDisposition string `json:"contentDisposition,omitempty"`
	ContentLanguage string `json:"contentLanguage,omitempty"`
	CacheControl string `json:"cacheControl,omitempty"`
}
type StorageSyncObjectPage struct {
	Entries []StorageSyncObject `json:"entries"`
	Directories []string `json:"directories,omitempty"`
	NextToken string `json:"nextToken"`
}
type StorageSyncRead struct { URL string `json:"url"`; Headers map[string]string `json:"headers"` }
func(s *TOSStore) syncClient(bucket string)(*sdkTOSClient,error){
	if s==nil||bucket!=s.bucket{return nil,ErrUnavailable};client,ok:=s.client.(*sdkTOSClient);if !ok||client.client==nil{return nil,ErrUnavailable};return client,nil
}
func(s *TOSStore) StorageSyncHead(ctx context.Context,bucket,key string)(*StorageSyncObject,error){
	client,err:=s.syncClient(bucket);if err!=nil{return nil,err};if _,err=cleanPublicationObjectKey(key);err!=nil{return nil,err}
	result,err:=client.client.HeadObjectV2(ctx,&tos.HeadObjectV2Input{Bucket:bucket,Key:key});if tos.StatusCode(err)==http.StatusNotFound{return nil,nil};if err!=nil||result==nil{return nil,ErrUnavailable}
	if result.DeleteMarker{return nil,nil};if result.ContentLength<0||result.ETag==""||result.ObjectType=="Symlink"{return nil,ErrUnavailable}
	// Missing checksums are unknown, not the SDK's uint64 zero default. Custom
	// user metadata is never accepted as verified content integrity.
	crc:="";if raw:=result.Header.Get("X-Tos-Hash-Crc64ecma");raw!=""{value,parseErr:=strconv.ParseUint(raw,10,64);if parseErr!=nil{return nil,ErrUnavailable};crc=strconv.FormatUint(value,10)}
	// Match Python SDK decoding without treating a literal '+' as a space.
	disposition:=result.Header.Get("Content-Disposition");if decoded,decodeErr:=url.PathUnescape(disposition);decodeErr==nil{disposition=decoded}
	return &StorageSyncObject{Key:key,Size:result.ContentLength,ETag:strings.Trim(result.ETag,"\""),VersionID:result.VersionID,CRC64:crc,LastModified:syncTimestamp(result.LastModified),ContentType:result.ContentType,ContentEncoding:result.ContentEncoding,ContentDisposition:disposition,ContentLanguage:result.ContentLanguage,CacheControl:result.CacheControl},nil
}
func syncTimestamp(value time.Time)string{if value.IsZero(){return ""};return value.UTC().Format("2006-01-02T15:04:05.000Z")}
func(s *TOSStore) StorageSyncList(ctx context.Context,bucket,prefix,cursor string,limit int,delimiter string)(StorageSyncObjectPage,error){
	client,err:=s.syncClient(bucket);if err!=nil{return StorageSyncObjectPage{},err}
	if limit<1||limit>1000||len(cursor)>4096||strings.ContainsRune(cursor,'\x00')||(delimiter!=""&&delimiter!="/"){return StorageSyncObjectPage{},fmt.Errorf("invalid metadata page")}
	if _,err=cleanPublicationObjectPrefix(prefix);err!=nil{return StorageSyncObjectPage{},err}
	result,err:=client.client.ListObjectsType2(ctx,&tos.ListObjectsType2Input{Bucket:bucket,Prefix:prefix,ContinuationToken:cursor,MaxKeys:limit,Delimiter:delimiter})
	if err!=nil||result==nil||(result.NextContinuationToken!=""&&result.NextContinuationToken==cursor){return StorageSyncObjectPage{},ErrUnavailable}
	page:=StorageSyncObjectPage{Entries:[]StorageSyncObject{},Directories:[]string{},NextToken:result.NextContinuationToken}
	for _,item:=range result.Contents {
		if _,err=cleanPublicationObjectKey(strings.TrimSuffix(item.Key,"/"));err!=nil||!strings.HasPrefix(item.Key,prefix)||item.Size<0||item.ObjectType=="Symlink"{return StorageSyncObjectPage{},ErrUnavailable}
		if strings.HasSuffix(item.Key,"/"){continue}
		// Listing SDKs disagree on missing CRC versus zero. Listing supplies
		// identity only; the worker always HEADs selected files for integrity.
		page.Entries=append(page.Entries,StorageSyncObject{Key:item.Key,Size:item.Size,ETag:strings.Trim(item.ETag,"\""),LastModified:syncTimestamp(item.LastModified)})
	}
	for _,entry:=range result.CommonPrefixes{if !strings.HasPrefix(entry.Prefix,prefix){return StorageSyncObjectPage{},ErrUnavailable};page.Directories=append(page.Directories,entry.Prefix)}
	return page,nil
}
func(s *TOSStore) StorageSyncReadURL(ctx context.Context,bucket,key,etag,versionID string)(StorageSyncRead,error){
	if err:=ctx.Err();err!=nil{return StorageSyncRead{},err};client,err:=s.syncClient(bucket);if err!=nil{return StorageSyncRead{},err}
	if _,err=cleanPublicationObjectKey(key);err!=nil{return StorageSyncRead{},err}
	if etag==""||len(etag)>512||len(versionID)>1024||strings.ContainsAny(etag+versionID,"\r\n\x00"){return StorageSyncRead{},fmt.Errorf("conditional snapshot identity required")}
	headers:=map[string]string{"If-Match":etag};query:=map[string]string{};if versionID!=""{query["versionId"]=versionID}
	result,err:=client.client.PreSignedURL(&tos.PreSignedURLInput{HTTPMethod:enum.HttpMethodGet,Bucket:bucket,Key:key,Expires:120,Header:headers,Query:query,IsSignedAllHeaders:true})
	if err!=nil||result==nil{return StorageSyncRead{},ErrUnavailable};return StorageSyncRead{URL:result.SignedUrl,Headers:headers},nil
}
