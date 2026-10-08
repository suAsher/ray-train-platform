package redis

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// These tests are copied into the pinned go-redis v9.0.2 module. In-package
// construction keeps Sentinel order deterministic; NewFailoverClient shuffles it.
func TestN9ESentinelTLSHandshakeTimeoutContinues(t *testing.T) {
	for _, cached := range []bool{false, true} {
		name := "uncached"
		if cached {
			name = "cached"
		}
		t.Run(name, func(t *testing.T) {
			clientTLS, serverTLS := n9eTLSConfigs(t)
			stalled := n9eStartSentinel(t, nil)
			healthy := n9eStartSentinel(t, serverTLS)
			failover := n9eFailover(t, clientTLS, cached, stalled.addr(), healthy.addr())

			// A live caller must survive the per-connection TLS handshake deadline.
			addr, err := failover.MasterAddr(context.Background())
			if err != nil || addr != n9eTestMasterAddr {
				t.Fatalf("live caller did not reach healthy Sentinel after TLS handshake timeout: addr=%q err=%v", addr, err)
			}
			minimum := int32(1)
			if cached {
				// RLock cache lookup, Lock cache recheck, then the candidate list.
				minimum = 3
			}
			if got := atomic.LoadInt32(&stalled.clientHellos); got < minimum {
				t.Fatalf("observed %d stalled TLS ClientHellos, want at least %d", got, minimum)
			}
			if atomic.LoadInt32(&healthy.verifiedClients) == 0 || atomic.LoadInt32(&healthy.masterQueries) == 0 {
				t.Fatal("healthy Sentinel was not queried over verified mutual TLS")
			}
		})
	}
}

func TestN9ESentinelTLSCallerAlreadyDone(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancelled"
		if deadline {
			name = "expired"
		}
		t.Run(name, func(t *testing.T) {
			clientTLS, serverTLS := n9eTLSConfigs(t)
			stalled := n9eStartSentinel(t, nil)
			healthy := n9eStartSentinel(t, serverTLS)
			failover := n9eFailover(t, clientTLS, false, stalled.addr(), healthy.addr())
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if deadline {
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
			}
			start := time.Now()
			addr, err := failover.MasterAddr(ctx)
			if addr != "" || !errors.Is(err, ctx.Err()) {
				t.Fatalf("completed caller: addr=%q err=%v, want %v", addr, err, ctx.Err())
			}
			if time.Since(start) > time.Second {
				t.Fatal("already completed caller did not return promptly")
			}
			if atomic.LoadInt32(&stalled.clientHellos) != 0 || atomic.LoadInt32(&healthy.masterQueries) != 0 {
				t.Fatal("completed caller attempted Sentinel discovery")
			}
		})
	}
}

func TestN9ESentinelTLSCallerCancelledDuringHandshake(t *testing.T) {
	clientTLS, serverTLS := n9eTLSConfigs(t)
	stalled := n9eStartSentinel(t, nil)
	healthy := n9eStartSentinel(t, serverTLS)
	failover := n9eFailover(t, clientTLS, false, stalled.addr(), healthy.addr())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		addr string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		addr, err := failover.MasterAddr(ctx)
		done <- result{addr, err}
	}()
	select {
	case <-stalled.sawHello:
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("client never began the stalled TLS handshake")
	}
	// v9.0.2 DialWithDialer has its own context: cancellation is observed when
	// that handshake returns. This patch does not change its transport API.
	select {
	case got := <-done:
		if got.addr != "" || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("caller cancellation lost: addr=%q err=%v", got.addr, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("caller cancellation was not observed after the dial timeout")
	}
	if atomic.LoadInt32(&healthy.masterQueries) != 0 {
		t.Fatal("cancelled caller continued to another Sentinel")
	}
}

func TestN9ESentinelTLSRejectsInvalidPeers(t *testing.T) {
	for _, mode := range []string{"missing-client-certificate", "untrusted-server", "wrong-server-name"} {
		t.Run(mode, func(t *testing.T) {
			clientTLS, serverTLS := n9eTLSConfigs(t)
			switch mode {
			case "missing-client-certificate":
				clientTLS.Certificates = nil
			case "untrusted-server":
				clientTLS.RootCAs = x509.NewCertPool()
			case "wrong-server-name":
				clientTLS.ServerName = "untrusted.invalid"
			}
			healthy := n9eStartSentinel(t, serverTLS)
			failover := n9eFailover(t, clientTLS, false, healthy.addr())
			addr, err := failover.MasterAddr(context.Background())
			if addr != "" || err == nil {
				t.Fatalf("invalid TLS peer accepted: addr=%q err=%v", addr, err)
			}
			if atomic.LoadInt32(&healthy.masterQueries) != 0 {
				t.Fatal("Sentinel command reached an unauthenticated TLS session")
			}
		})
	}
}

func n9eFailover(t *testing.T, clientTLS *tls.Config, cached bool, addresses ...string) *sentinelFailover {
	t.Helper()
	opt := &FailoverOptions{
		MasterName:    "n9e-regression",
		SentinelAddrs: append([]string(nil), addresses...),
		TLSConfig:    clientTLS,
		DialTimeout:  200 * time.Millisecond,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
		MaxRetries:   -1,
		PoolSize:     4,
	}
	failover := &sentinelFailover{opt: opt, sentinelAddrs: append([]string(nil), addresses...)}
	if cached {
		failover.sentinel = NewSentinelClient(opt.sentinelOptions(addresses[0]))
		// A supported empty subscription provides the normal Close lifecycle
		// without opening an unrelated connection during test setup.
		failover.pubsub = failover.sentinel.Subscribe(context.Background())
	}
	t.Cleanup(func() { _ = failover.Close() })
	return failover
}
