package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"ray-train-platform-backend/personalcsi"
	"ray-train-platform-backend/personalstorage"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	socket := flag.String("socket", "/csi/csi.sock", "trusted kubelet CSI Unix socket")
	kubeletRoot := flag.String("kubelet-root", "/var/lib/kubelet", "host kubelet root")
	nodeID := flag.String("node-id", os.Getenv("NODE_ID"), "Kubernetes node identity")
	knownHosts := flag.String("known-hosts", "/etc/personal-storage/known_hosts", "pinned Wellspiking host keys")
	probe := flag.Bool("probe", false, "probe local CSI socket and exit")
	flag.Parse()
	if !filepath.IsAbs(*socket) || filepath.Clean(*socket) != *socket || strings.HasPrefix(*socket, "//") {
		return errors.New("CSI requires an absolute Unix socket")
	}
	if *probe {
		return checkReady(*socket)
	}
	if os.Geteuid() != 0 {
		return errors.New("CSI node driver must run as host-trusted root")
	}
	if *nodeID == "" {
		return errors.New("CSI node identity is required")
	}
	keys, err := os.ReadFile(*knownHosts)
	if err != nil {
		return errors.New("pinned Wellspiking host key file is required")
	}
	factory, err := personalstorage.NewSFTPFactory(personalstorage.Endpoint{Host: "mount.wellspiking.ai", Port: 22, KnownHosts: keys})
	if err != nil {
		return errors.New("invalid pinned Wellspiking host key configuration")
	}
	if err := prepareSocket(*socket); err != nil {
		return err
	}
	listener, err := net.Listen("unix", *socket)
	if err != nil {
		return errors.New("cannot listen on CSI Unix socket")
	}
	defer listener.Close()
	if err := os.Chmod(*socket, 0600); err != nil {
		return errors.New("cannot restrict CSI socket")
	}
	driver := personalcsi.NewDriver(*nodeID, *kubeletRoot, factory, personalcsi.NewFuseMounter())
	server := grpc.NewServer(grpc.MaxRecvMsgSize(128 * 1024))
	csi.RegisterIdentityServer(server, driver)
	csi.RegisterNodeServer(server, driver)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	failure := make(chan error, 1)
	go func() { failure <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		server.Stop()
		return driver.Close()
	case <-failure:
		server.Stop()
		_ = driver.Close()
		return errors.New("CSI Unix server stopped unexpectedly")
	}
}
func prepareSocket(socket string) error {
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		return errors.New("cannot create CSI socket directory")
	}
	info, err := os.Lstat(socket)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return errors.New("CSI socket path is occupied by a non-socket")
	}
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err == nil {
		conn.Close()
		return errors.New("CSI socket is already active")
	}
	if err := os.Remove(socket); err != nil {
		return errors.New("cannot remove stale CSI socket")
	}
	return nil
}
func checkReady(socket string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return errors.New("CSI probe connection failed")
	}
	defer client.Close()
	response, err := csi.NewIdentityClient(client).Probe(ctx, &csi.ProbeRequest{})
	if err != nil || !response.GetReady().GetValue() {
		return errors.New("CSI node driver is not ready")
	}
	return nil
}
