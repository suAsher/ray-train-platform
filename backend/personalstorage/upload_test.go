package personalstorage
import("context";"errors";"os";"strings";"testing";"time")
type uploadInfo struct{size int64}
func(i uploadInfo)Name()string{return "file"};func(i uploadInfo)Size()int64{return i.size};func(i uploadInfo)Mode()os.FileMode{return 0600};func(i uploadInfo)ModTime()time.Time{return time.Time{}};func(i uploadInfo)IsDir()bool{return false};func(i uploadInfo)Sys()any{return nil}
type uploadFile struct{data []byte}
func(f *uploadFile)ReadAt(p []byte,off int64)(int,error){return 0,errors.New("unused")};func(f *uploadFile)WriteAt(p []byte,off int64)(int,error){f.data=append(f.data,p...);return len(p),nil};func(f *uploadFile)Stat()(os.FileInfo,error){return uploadInfo{int64(len(f.data))},nil};func(f *uploadFile)Sync()error{return nil};func(f *uploadFile)Truncate(int64)error{return nil};func(f *uploadFile)Close()error{return nil}
type uploadStore struct{file uploadFile;removed bool;renamed bool}
func(s *uploadStore)List(context.Context,string)([]Entry,error){return nil,nil};func(s *uploadStore)Stat(context.Context,string)(Entry,error){return Entry{},ErrNotFound};func(s *uploadStore)Open(context.Context,string,int)(File,error){return &s.file,nil};func(s *uploadStore)Mkdir(context.Context,string)error{return nil};func(s *uploadStore)Remove(context.Context,string)error{s.removed=true;return nil};func(s *uploadStore)Rename(context.Context,string,string)error{s.renamed=true;return nil};func(s *uploadStore)Close()error{return nil}
func TestPersonalStorageUploadBoundsAndAtomicCompletion(t *testing.T){
 for _,tc:=range []struct{size,max int64;body string;fail bool}{{4,4,"data",false},{5,4,"12345",true},{4,4,"12345",true},{4,4,"123",true}}{s:=&uploadStore{};err:=Upload(context.Background(),s,"file",strings.NewReader(tc.body),tc.size,tc.max);if (err!=nil)!=tc.fail{t.Errorf("bounds %+v: %v",tc,err)};if tc.fail&&s.renamed{t.Fatal("published partial upload")};if !tc.fail&&!s.renamed{t.Fatal("upload not atomically published")}}
}
