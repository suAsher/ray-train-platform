package storagesync

import (
	"context"
	"errors"
	"regexp"
)

var workerIdentity=regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

// Claim pins one Pod UID before that process opens writer credentials. A Job
// may start the program twice; the second Pod never inherits the first claim.
func(m *Manager)Claim(ctx context.Context,id string,attempt int,generation int64,workerID string)error{
	if !workerIdentity.MatchString(workerID){return ErrInvalid}
	return m.repo.Transact(ctx,func(tx Tx)error{
		r,err:=tx.GetRun(id);if err==nil{
			if r.Attempt!=attempt||r.Generation!=generation||r.State!="RUNNING"||r.ReceiptState!=""{return ErrStaleAttempt}
			if r.WorkerID!=""&&r.WorkerID!=workerID{return ErrStaleAttempt};r.WorkerID=workerID;return tx.PutRun(r)
		};if !errors.Is(err,ErrNotFound){return err}
		p,err:=tx.GetPreview(id);if err!=nil{return err};if p.Attempt!=attempt||p.Generation!=generation||p.State!="RUNNING"||p.ReceiptState!=""||!p.ExpiresAt.After(m.now()){return ErrStaleAttempt}
		if p.WorkerID!=""&&p.WorkerID!=workerID{return ErrStaleAttempt};p.WorkerID=workerID;return tx.PutPreview(p)
	})
}
func runActor(r Run)string{if r.AuthorizedBy!=""{return r.AuthorizedBy};return r.RequestedBy}
