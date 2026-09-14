package warehousesync

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
)

var errCredential = errors.New("warehouse sync credential unavailable")

type credentialBox struct { aead cipher.AEAD }

// Derive a purpose-specific key so the platform's pepper is never used directly
// as an encryption key. AAD prevents moving a credential between owners/tasks.
func newCredentialBox(pepper []byte) (credentialBox,error) {
	if len(pepper)<32 { return credentialBox{},errCredential }
	hash := sha256.New()
	hash.Write([]byte("raytrain/function-warehouse/delegation/v1\x00"))
	hash.Write(pepper)
	block,err:=aes.NewCipher(hash.Sum(nil))
	if err!=nil{return credentialBox{},errCredential}
	aead,err:=cipher.NewGCM(block)
	if err!=nil{return credentialBox{},errCredential}
	return credentialBox{aead:aead},nil
}

func credentialAAD(op Operation) []byte {
	encoded,_:=json.Marshal([4]string{op.ID,op.OwnerID,op.TenantID,string(op.Environment)})
	return encoded
}

func (b credentialBox) seal(op Operation, token string)([]byte,error) {
	if token==""||len(token)>32768||strings.ContainsAny(token,"\x00\r\n") {return nil,errCredential}
	nonce:=make([]byte,b.aead.NonceSize())
	if _,err:=rand.Read(nonce);err!=nil{return nil,errCredential}
	return b.aead.Seal(nonce,nonce,[]byte(token),credentialAAD(op)),nil
}

func (b credentialBox) open(op Operation)(string,error) {
	n:=b.aead.NonceSize()
	if len(op.Credential)<n+b.aead.Overhead(){return "",errCredential}
	plain,err:=b.aead.Open(nil,op.Credential[:n],op.Credential[n:],credentialAAD(op))
	if err!=nil{return "",errCredential}
	return string(plain),nil
}
