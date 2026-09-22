package assistantidle

import (
 "encoding/json"
 "net/http"
 "sync"
 "time"
)

type GateStatus struct {
 Allow bool `json:"allow"`
 Epoch string `json:"epoch"`
 ValidUntil time.Time `json:"validUntil"`
}

type Gate struct {
 mu sync.RWMutex
 epoch string
 until time.Time
 now func()time.Time
}
func NewGate(epoch string,now func()time.Time)*Gate{return &Gate{epoch:epoch,now:now}}
func (g *Gate) Open(until time.Time){
 g.mu.Lock();defer g.mu.Unlock()
 // A caller cannot accidentally turn a short observation lease into a long one.
 max:=g.now().Add(3*time.Second);if until.After(max){until=max}
 g.until=until
}
func (g *Gate) Close(){g.mu.Lock();defer g.mu.Unlock();g.until=time.Time{}}
func (g *Gate) Snapshot()GateStatus{
 g.mu.RLock();defer g.mu.RUnlock()
 return GateStatus{Allow:g.now().Before(g.until),Epoch:g.epoch,ValidUntil:g.until}
}
func (g *Gate) ServeHTTP(w http.ResponseWriter,r *http.Request){
 w.Header().Set("Cache-Control","no-store")
 if r.Method!=http.MethodGet{w.WriteHeader(http.StatusMethodNotAllowed);return}
 w.Header().Set("Content-Type","application/json")
 _=json.NewEncoder(w).Encode(g.Snapshot())
}

type LeaseStatus struct {
 Holder string
 RenewedAt time.Time
 Duration time.Duration
}
// Missing, expired, invalid or implausibly future leases close admission.
func LeaseExpired(lease LeaseStatus,now time.Time)bool{
 return lease.Holder=="" || lease.RenewedAt.IsZero() || lease.Duration<=0 || lease.Duration>30*time.Second || lease.RenewedAt.After(now.Add(5*time.Second)) || !now.Before(lease.RenewedAt.Add(lease.Duration))
}
