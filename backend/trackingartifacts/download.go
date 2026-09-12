package trackingartifacts

import (
 "context"
 "crypto/sha256"
 "encoding/hex"
 "hash"
 "io"
)

func (s *Service) Download(ctx context.Context,scope Scope,id string)(Artifact,io.ReadCloser,error){
 a,err:=s.Get(ctx,scope,id);if err!=nil{return Artifact{},nil,err};if a.State!="READY"||len(a.UploadedParts)!=a.TotalParts{return Artifact{},nil,ErrConflict}
 ctx,cancel:=context.WithTimeout(ctx,OperationTimeout);stream:=&partReader{ctx:ctx,cancel:cancel,objects:s.objects,id:id,parts:a.UploadedParts}
 if err:=stream.open();err!=nil{stream.Close();return Artifact{},nil,err};return a,stream,nil
}
// partReader keeps just one object stream open and checks each immutable
// part's length and checksum again during download. Close always releases both
// the HTTP stream and deadline, including when a client disconnects early.
type partReader struct{ctx context.Context;cancel context.CancelFunc;objects Objects;id string;parts []Part;index int;current io.ReadCloser;sum hash.Hash;count int64;closed bool}
func(r *partReader)open()error{
 if r.ctx.Err()!=nil{return ErrUnavailable};if r.index>=len(r.parts){return io.EOF};part:=r.parts[r.index];body,size,err:=r.objects.Get(r.ctx,r.id,part.Index);if err!=nil{return failure(err)};if body==nil||size!=part.SizeBytes{if body!=nil{body.Close()};return ErrUnavailable};r.current=body;r.sum=sha256.New();r.count=0;return nil
}
func(r *partReader)Read(p []byte)(int,error){
 if len(p)==0{return 0,nil};if r.closed{return 0,io.EOF};if r.ctx.Err()!=nil{r.Close();return 0,ErrUnavailable}
 n,err:=r.current.Read(p);r.count+=int64(n);if n>0{_,_=r.sum.Write(p[:n])};part:=r.parts[r.index]
 if r.count>part.SizeBytes{r.Close();return 0,ErrUnavailable}
 if err==io.EOF{
  closeErr:=r.current.Close();r.current=nil;if r.count!=part.SizeBytes||hex.EncodeToString(r.sum.Sum(nil))!=part.SHA256||closeErr!=nil{r.Close();return n,ErrUnavailable}
  r.index++;if r.index==len(r.parts){r.Close();return n,io.EOF};if nextErr:=r.open();nextErr!=nil{r.Close();return n,nextErr};return n,nil
 }
 if err!=nil{r.Close();return n,ErrUnavailable};return n,nil
}
func(r *partReader)Close()error{if r.closed{return nil};r.closed=true;r.cancel();if r.current!=nil{return r.current.Close()};return nil}
