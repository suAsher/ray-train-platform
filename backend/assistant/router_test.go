package assistant

import (
	"context"
	"errors"
	"testing"
)

type fakeProvider struct { calls int; answer string; err error }
func (p *fakeProvider) complete(context.Context, Input) (string, error) { p.calls++; return p.answer, p.err }

func TestRoutingModesAndFallback(t *testing.T) {
	for _, tc := range []struct { mode string; apiError error; want string; apiCalls, localCalls int }{
		{"docs", nil, "docs", 0, 0}, {"api", nil, "api", 1, 0},
		{"local", nil, "local", 0, 1}, {"auto", nil, "api", 1, 0},
		{"auto", errBudget, "local", 1, 1}, {"api", errBudget, "docs", 1, 0},
	} {
		t.Run(tc.mode+tc.want, func(t *testing.T) {
			a := &fakeProvider{answer:"company",err:tc.apiError}; l := &fakeProvider{answer:"local"}
			r := &Router{api:a,local:l}
			got, err := r.Answer(context.Background(),tc.mode,Input{Question:"question"})
			if err != nil || got.Mode != tc.want || a.calls != tc.apiCalls || l.calls != tc.localCalls { t.Fatalf("result=%+v error=%v calls=%d/%d",got,err,a.calls,l.calls) }
		})
	}
}
func TestNoProviderAndLocalFirst(t *testing.T) {
	r,err:=NewRouter(Config{}); if err!=nil { t.Fatal(err) }
	got,err:=r.Answer(context.Background(),"auto",Input{}); if err!=nil || got.Mode!="docs" {t.Fatal(got,err)}
	a:=&fakeProvider{answer:"api"};l:=&fakeProvider{answer:"local"}
	r=&Router{api:a,local:l,localFirst:true};got,err=r.Answer(context.Background(),"auto",Input{})
	if err!=nil||got.Mode!="local"||a.calls!=0 {t.Fatal(got,err,a.calls)}
}
func TestCanceledRequestDoesNotCallFallback(t *testing.T) {
	a:=&fakeProvider{err:context.Canceled};l:=&fakeProvider{answer:"local"}
	r:=&Router{api:a,local:l};_,err:=r.Answer(context.Background(),"auto",Input{})
	if !errors.Is(err,context.Canceled)||l.calls!=0 {t.Fatal(err,l.calls)}
}
func TestBudgetCircuitAndAuthenticationReason(t *testing.T) {
	a:=&fakeProvider{err:errBudget};r:=&Router{api:a}
	for i:=0;i<2;i++ {got,err:=r.Answer(context.Background(),"auto",Input{});if err!=nil||got.Mode!="docs"||got.Reason!="budget_exhausted" {t.Fatal(got,err)}}
	if a.calls!=1 {t.Fatal("quota retry storm",a.calls)}
	r=&Router{api:&fakeProvider{err:errAuthentication}};got,_:=r.Answer(context.Background(),"api",Input{})
	if got.Reason!="authentication_error" {t.Fatal(got)}
}
func TestProviderConfigRejectsCredentialURLsAndPlaintextRemote(t *testing.T) {
	for _,url:=range []string{"http://example.com/v1","https://user:pass@example.com/v1","https://example.com/v1?key=secret","https://example.com/v1#fragment","https://example.com/v1/../secret"} {
		if _,err:=NewRouter(Config{API:ProviderConfig{BaseURL:url,Model:"test",APIKey:"test"}});err==nil {t.Fatal("accepted unsafe URL",url)}
	}
	if _,err:=NewRouter(Config{API:ProviderConfig{BaseURL:"https://example.com/v1",Model:"test"}});err==nil {t.Fatal("API without key")}
}
