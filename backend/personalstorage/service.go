package personalstorage
import(
 "context"
 "crypto/ed25519"
 "crypto/rand"
 "crypto/sha256"
 "encoding/hex"
 "encoding/pem"
 "errors"
 "golang.org/x/crypto/ssh"
)
type Service struct{repo Repository;vault Vault;factory Factory}
func NewService(r Repository,v Vault,f Factory)*Service{return &Service{repo:r,vault:v,factory:f}}
func publicKey(key []byte)(string,string,error){signer,err:=ssh.ParsePrivateKey(key);if err!=nil||signer.PublicKey().Type()!=ssh.KeyAlgoED25519{return "","",ErrConflict};return string(ssh.MarshalAuthorizedKey(signer.PublicKey())),ssh.FingerprintSHA256(signer.PublicKey()),nil}
func(s *Service) binding(ctx context.Context,o Owner)(Binding,error){
 if err:=ValidateOwner(o);err!=nil{return Binding{},err};if s==nil||s.repo==nil||s.vault==nil{return Binding{},ErrUnavailable}
 b,err:=s.repo.Get(ctx,o.ID);if err!=nil{return Binding{},err};if b.OwnerID!=o.ID||b.Username!=o.Username{return Binding{},ErrForbidden};return b,nil
}
func view(b Binding)View{return View{BindingID:b.ID,OwnerID:b.OwnerID,Username:b.Username,PublicKey:b.PublicKey,Fingerprint:b.Fingerprint,State:b.State,Generation:b.Generation,VerifiedAt:b.VerifiedAt,MountPath:MountPath}}
func(s *Service)View(ctx context.Context,o Owner)(View,error){b,err:=s.binding(ctx,o);if errors.Is(err,ErrNotFound){return View{State:StateUnbound,MountPath:MountPath},nil};if err!=nil{return View{},err};return view(b),nil}
func(s *Service)Enroll(ctx context.Context,o Owner)(View,error){
 if b,err:=s.binding(ctx,o);err==nil{if _,err=s.load(ctx,b);err!=nil{return View{},err};return view(b),nil}else if !errors.Is(err,ErrNotFound){return View{},err}
 _,key,err:=ed25519.GenerateKey(rand.Reader);if err!=nil{return View{},ErrUnavailable};block,err:=ssh.MarshalPrivateKey(key,"");if err!=nil{return View{},ErrUnavailable}
 sum:=sha256.Sum256([]byte(o.ID));id:="wp-"+hex.EncodeToString(sum[:16])
 b:=Binding{ID:id,OwnerID:o.ID,Username:o.Username,SecretName:"personal-"+id,Generation:1,State:StatePending}
 c,err:=s.vault.Ensure(ctx,b,pem.EncodeToMemory(block));if err!=nil{return View{},err}
 if c.OwnerID!=b.OwnerID||c.BindingID!=b.ID||c.Username!=b.Username||c.Generation!=b.Generation{return View{},ErrConflict}
 b.PublicKey,b.Fingerprint,err=publicKey(c.PrivateKey);if err!=nil{return View{},err}
 winner,err:=s.repo.Create(ctx,b);if err!=nil{return View{},err};if winner.ID!=b.ID||winner.OwnerID!=b.OwnerID||winner.Username!=b.Username||winner.PublicKey!=b.PublicKey||winner.Generation!=b.Generation||winner.SecretName!=b.SecretName{return View{},ErrConflict};return view(winner),nil
}
func(s *Service)load(ctx context.Context,b Binding)(Credential,error){
 c,err:=s.vault.Load(ctx,b);if err!=nil{return Credential{},err};pub,fp,err:=publicKey(c.PrivateKey)
 if err!=nil||c.OwnerID!=b.OwnerID||c.BindingID!=b.ID||c.Username!=b.Username||c.Generation!=b.Generation||pub!=b.PublicKey||fp!=b.Fingerprint{return Credential{},ErrConflict};return c,nil
}
func(s *Service)Verify(ctx context.Context,o Owner)(View,error){
 b,err:=s.binding(ctx,o);if err!=nil{return View{},err};c,err:=s.load(ctx,b);if err!=nil{return View{},err};if s.factory==nil{return View{},ErrUnavailable}
 store,err:=s.factory.Open(ctx,c);if err!=nil{return View{},ErrUnavailable};defer store.Close()
 entry,err:=store.Stat(ctx,"");if err!=nil||!entry.Mode.IsDir(){return View{},ErrUnavailable}
 b,err=s.repo.MarkVerified(ctx,b.OwnerID,b.ID,b.Generation);if err!=nil{return View{},err};return view(b),nil
}
func(s *Service)Credential(ctx context.Context,o Owner)(Credential,error){b,err:=s.binding(ctx,o);if err!=nil{return Credential{},err};if b.State!=StateReady{return Credential{},ErrNotReady};return s.load(ctx,b)}
func(s *Service)MountReference(ctx context.Context,o Owner)(MountReference,error){b,err:=s.binding(ctx,o);if err!=nil{return MountReference{},err};if b.State!=StateReady{return MountReference{},ErrNotReady};if _,err=s.load(ctx,b);err!=nil{return MountReference{},err};return MountReference{OwnerID:b.OwnerID,BindingID:b.ID,Username:b.Username,Generation:b.Generation,SecretName:b.SecretName},nil}
func(s *Service)OpenStore(ctx context.Context,o Owner)(Store,error){c,err:=s.Credential(ctx,o);if err!=nil{return nil,err};if s.factory==nil{return nil,ErrUnavailable};store,err:=s.factory.Open(ctx,c);if err!=nil{return nil,ErrUnavailable};return store,nil}
