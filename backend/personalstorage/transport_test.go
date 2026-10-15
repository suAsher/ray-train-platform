package personalstorage
import("errors";"strings";"testing")
func TestPersonalStorageDirectoryTransportBudget(t *testing.T){
 closed:=false;b:=&responseBudget{reader:strings.NewReader("0123456789"),close:func(){closed=true}};b.set(4);buf:=make([]byte,10)
 if _,err:=b.Read(buf);!errors.Is(err,ErrTooLarge)||!closed{t.Fatal("directory transport budget did not terminate oversized session")}
}
