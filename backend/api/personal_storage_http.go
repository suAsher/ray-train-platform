package api

import (
 "context"
 "net/http"
 "time"

 "github.com/gin-gonic/gin"
)

type personalStorageHTTPControllerKey struct{}

// PersonalStorageHTTPHandler preserves access to the net/http writer beneath
// Gin 1.9. Only a personal SFTP upload sets a socket read deadline; existing
// object-store transfers retain their independently defined streaming policy.
func PersonalStorageHTTPHandler(next http.Handler) http.Handler {
 return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  ctx:=context.WithValue(r.Context(),personalStorageHTTPControllerKey{},http.NewResponseController(w))
  next.ServeHTTP(w,r.WithContext(ctx))
 })
}

func (h *Handler) boundPersonalStorageUpload(c *gin.Context)(func(),bool) {
 controller,ok:=c.Request.Context().Value(personalStorageHTTPControllerKey{}).(*http.ResponseController)
 if !ok { h.writeError(c,http.StatusServiceUnavailable,"PERSONAL_STORAGE_UPLOAD_UNAVAILABLE","personal storage upload transport is unavailable");return nil,false }
 timeout:=h.personalStorageUploadTimeout
 if timeout<=0 { timeout=2*time.Minute }
 if err:=controller.SetReadDeadline(time.Now().Add(timeout));err!=nil { h.writeError(c,http.StatusServiceUnavailable,"PERSONAL_STORAGE_UPLOAD_UNAVAILABLE","personal storage upload transport is unavailable");return nil,false }
 ctx,cancel:=context.WithTimeout(c.Request.Context(),timeout)
 c.Request=c.Request.WithContext(ctx)
 return func(){cancel();_ = controller.SetReadDeadline(time.Time{})},true
}
