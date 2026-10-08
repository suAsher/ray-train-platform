package redis

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const n9eTestMasterAddr = "127.0.0.1:16379"

type n9eSentinelFixture struct {
	listener        net.Listener
	serverTLS       *tls.Config
	done            chan struct{}
	sawHello        chan struct{}
	helloOnce       sync.Once
	clientHellos    int32
	verifiedClients int32
	masterQueries   int32
	mu              sync.Mutex
	closed          bool
	connections     map[net.Conn]struct{}
	wg              sync.WaitGroup
	protocolErrors  chan error
}

// A nil TLS configuration accepts TCP and reads a real ClientHello, but never
// answers it. Its address and listener remain available, like a paused Sentinel.
func n9eStartSentinel(t *testing.T, serverTLS *tls.Config) *n9eSentinelFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &n9eSentinelFixture{
		listener: listener, serverTLS: serverTLS,
		done: make(chan struct{}), sawHello: make(chan struct{}),
		connections: make(map[net.Conn]struct{}), protocolErrors: make(chan error, 1),
	}
	s.wg.Add(1)
	go s.accept()
	t.Cleanup(func() {
		s.mu.Lock()
		s.closed = true
		close(s.done)
		_ = s.listener.Close()
		for conn := range s.connections {
			_ = conn.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
		select {
		case err := <-s.protocolErrors:
			t.Errorf("Sentinel protocol fixture: %v", err)
		default:
		}
	})
	return s
}

func (s *n9eSentinelFixture) addr() string { return s.listener.Addr().String() }

func (s *n9eSentinelFixture) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.connections[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(conn)
	}
}

func (s *n9eSentinelFixture) serve(raw net.Conn) {
	defer s.wg.Done()
	defer func() {
		_ = raw.Close()
		s.mu.Lock()
		delete(s.connections, raw)
		s.mu.Unlock()
	}()
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	if s.serverTLS == nil {
		var header [5]byte
		if _, err := io.ReadFull(raw, header[:]); err != nil {
			return
		}
		if header[0] != 22 || header[1] != 3 {
			s.report(fmt.Errorf("expected TLS handshake record, got %x", header))
			return
		}
		atomic.AddInt32(&s.clientHellos, 1)
		s.helloOnce.Do(func() { close(s.sawHello) })
		<-s.done
		return
	}
	conn := tls.Server(raw, s.serverTLS)
	if err := conn.Handshake(); err != nil {
		return // Negative TLS cases deliberately fail authentication.
	}
	if len(conn.ConnectionState().VerifiedChains) == 0 {
		s.report(fmt.Errorf("TLS handshake had no verified client chain"))
		return
	}
	atomic.AddInt32(&s.verifiedClients, 1)
	reader := bufio.NewReader(conn)
	for {
		args, err := n9eReadCommand(reader)
		if err != nil {
			return
		}
		if err := s.respond(conn, args); err != nil {
			return
		}
	}
}

func (s *n9eSentinelFixture) respond(conn net.Conn, args []string) error {
	switch strings.ToLower(args[0]) {
	case "hello":
		_, err := io.WriteString(conn, "%0\r\n")
		return err
	case "sentinel":
		if len(args) == 3 && args[2] == "n9e-regression" {
			switch strings.ToLower(args[1]) {
			case "get-master-addr-by-name":
				atomic.AddInt32(&s.masterQueries, 1)
				_, err := io.WriteString(conn, "*2\r\n$9\r\n127.0.0.1\r\n$5\r\n16379\r\n")
				return err
			case "sentinels":
				_, err := io.WriteString(conn, "*0\r\n")
				return err
			}
		}
	case "subscribe":
		for i, channel := range args[1:] {
			if _, err := fmt.Fprintf(conn, "*3\r\n$9\r\nsubscribe\r\n$%d\r\n%s\r\n:%d\r\n", len(channel), channel, i+1); err != nil {
				return err
			}
		}
		return nil
	case "ping":
		_, err := io.WriteString(conn, "+PONG\r\n")
		return err
	}
	s.report(fmt.Errorf("unsupported command %q", args))
	_, err := io.WriteString(conn, "-ERR unsupported fixture command\r\n")
	return err
}

func (s *n9eSentinelFixture) report(err error) {
	select {
	case s.protocolErrors <- err:
	default:
	}
}

func n9eReadCommand(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 4 || line[0] != '*' || !strings.HasSuffix(line, "\r\n") {
		return nil, fmt.Errorf("expected RESP array")
	}
	count, err := strconv.Atoi(line[1 : len(line)-2])
	if err != nil || count < 1 || count > 16 {
		return nil, fmt.Errorf("invalid command length")
	}
	args := make([]string, count)
	for i := range args {
		line, err = reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if len(line) < 4 || line[0] != '$' || !strings.HasSuffix(line, "\r\n") {
			return nil, fmt.Errorf("expected RESP bulk string")
		}
		size, err := strconv.Atoi(line[1 : len(line)-2])
		if err != nil || size < 0 || size > 4096 {
			return nil, fmt.Errorf("invalid argument size")
		}
		value := make([]byte, size+2)
		if _, err := io.ReadFull(reader, value); err != nil {
			return nil, err
		}
		if string(value[size:]) != "\r\n" {
			return nil, fmt.Errorf("invalid argument terminator")
		}
		args[i] = string(value[:size])
	}
	return args, nil
}

func n9eTLSConfigs(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "n9e-regression-ca"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	issue := func(serial int64, usage x509.ExtKeyUsage) tls.Certificate {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{
			SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "n9e-regression"},
			NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
			IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der, ca.Raw}, PrivateKey: key}
	}
	client := &tls.Config{
		MinVersion: tls.VersionTLS12, RootCAs: roots,
		Certificates: []tls.Certificate{issue(2, x509.ExtKeyUsageClientAuth)},
	}
	server := &tls.Config{
		MinVersion: tls.VersionTLS12, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert,
		Certificates: []tls.Certificate{issue(3, x509.ExtKeyUsageServerAuth)},
	}
	return client, server
}
