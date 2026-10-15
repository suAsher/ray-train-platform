// Package testfixture provides isolated in-memory roots over real SSH/SFTP.
// It is only used by candidate integration tests and is not an upstream attestation.
package testfixture

import (
	"bytes"
	"crypto/ed25519"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"net"
	ps "ray-train-platform-backend/personalstorage"
	"strconv"
	"sync"
)

type Fixture struct {
	Endpoint    ps.Endpoint
	Credentials []ps.Credential
	listener    net.Listener
	mu          sync.Mutex
	connections map[net.Conn]bool
	handlers    map[string]sftp.Handlers
	closed      bool
}

func Start() (*Fixture, error) {
	return start(nil)
}

// StartWithECDSAHostKey offers ECDSA and Ed25519, while the trusted endpoint pin
// contains only Ed25519. This mirrors servers supporting multiple host keys.
func StartWithECDSAHostKey() (*Fixture, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return nil, err
	}
	return start([]ssh.Signer{signer})
}

func start(additionalHostKeys []ssh.Signer) (*Fixture, error) {
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	host, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	hostName, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	f := &Fixture{Endpoint: ps.Endpoint{Host: hostName, Port: port, KnownHosts: []byte(knownhosts.Line([]string{listener.Addr().String()}, host.PublicKey()) + "\n")}, listener: listener, connections: map[net.Conn]bool{}, handlers: map[string]sftp.Handlers{}}
	keys := map[string][]byte{}
	for _, name := range []string{"user-a", "user-b"} {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			f.Close()
			return nil, err
		}
		block, err := ssh.MarshalPrivateKey(private, "")
		if err != nil {
			f.Close()
			return nil, err
		}
		signer, _ := ssh.NewSignerFromKey(private)
		keys[name] = signer.PublicKey().Marshal()
		f.handlers[name] = sftp.InMemHandler()
		f.Credentials = append(f.Credentials, ps.Credential{OwnerID: name, BindingID: name, Username: name, Generation: 1, PrivateKey: pem.EncodeToMemory(block)})
	}
	config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if expected, ok := keys[meta.User()]; ok && bytes.Equal(expected, key.Marshal()) {
			return &ssh.Permissions{}, nil
		}
		return nil, errors.New("fixture authentication denied")
	}}
	for _, additional := range additionalHostKeys {
		config.AddHostKey(additional)
	}
	config.AddHostKey(host)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			if f.closed {
				f.mu.Unlock()
				_ = conn.Close()
				return
			}
			f.connections[conn] = true
			f.mu.Unlock()
			go f.serve(conn, config)
		}
	}()
	return f, nil
}
func (f *Fixture) serve(conn net.Conn, config *ssh.ServerConfig) {
	defer func() { _ = conn.Close(); f.mu.Lock(); delete(f.connections, conn); f.mu.Unlock() }()
	sshConn, chans, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(requests)
	for incoming := range chans {
		if incoming.ChannelType() != "session" {
			_ = incoming.Reject(ssh.UnknownChannelType, "fixture sessions only")
			continue
		}
		channel, requests, err := incoming.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for request := range requests {
				var payload struct{ Name string }
				if request.Type != "subsystem" || ssh.Unmarshal(request.Payload, &payload) != nil || payload.Name != "sftp" {
					_ = request.Reply(false, nil)
					continue
				}
				_ = request.Reply(true, nil)
				server := sftp.NewRequestServer(channel, f.handlers[sshConn.User()])
				_ = server.Serve()
				_ = server.Close()
				return
			}
		}()
	}
}
func (f *Fixture) Close() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	_ = f.listener.Close()
	for conn := range f.connections {
		_ = conn.Close()
	}
}
func (f *Fixture) RawClient(index int) (*sftp.Client, error) {
	c := f.Credentials[index]
	signer, err := ssh.ParsePrivateKey(c.PrivateKey)
	if err != nil {
		return nil, err
	}
	// Host key is pinned through the same trusted factory in normal tests. RawClient
	// is fixture-only seeding; parse the known_hosts key directly for strict pinning.
	_, _, key, _, _, err := ssh.ParseKnownHosts(f.Endpoint.KnownHosts)
	if err != nil {
		return nil, err
	}
	conn, err := ssh.Dial("tcp", net.JoinHostPort(f.Endpoint.Host, strconv.Itoa(f.Endpoint.Port)), &ssh.ClientConfig{User: c.Username, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(key)})
	if err != nil {
		return nil, err
	}
	client, err := sftp.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}
