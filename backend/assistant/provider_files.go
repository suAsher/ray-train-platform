package assistant

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func validProviderFilePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !containsControl(path) && len(path) <= 4096
}

// Kubernetes Secret volumes use symlinks. Follow them, then require a bounded
// regular file; never return file-system errors containing deployment paths.
func readProviderFile(path string, limit int64) ([]byte, error) {
	if !validProviderFilePath(path) {
		return nil, errors.New("assistant provider file must use an absolute canonical path")
	}
	// Reject directories/devices/FIFOs before opening them, which could otherwise
	// block startup. Recheck the opened descriptor below for projection changes.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("assistant provider file must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("assistant provider file is unavailable")
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("assistant provider file must be a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("assistant provider file could not be read")
	}
	return data, nil
}

// ReadProviderKeyFile resolves only the deployment-owned configuration path.
// It is not exposed to HTTP callers and never includes the key in errors.
func ReadProviderKeyFile(path string) (string, error) {
	data, err := readProviderFile(path, 8192)
	if err != nil {
		return "", err
	}
	key := strings.TrimRight(string(data), "\r\n")
	if key == "" || strings.TrimSpace(key) != key || containsControl(key) {
		return "", errors.New("assistant provider credential file is invalid")
	}
	return key, nil
}

func providerTLSConfig(caFile string) (*tls.Config, error) {
	data, err := readProviderFile(caFile, 1024*1024)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return nil, errors.New("assistant provider CA file does not contain a certificate")
	}
	// This trust pool belongs only to the one configured local provider. Keep
	// certificate and hostname verification enabled and global trust untouched.
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, nil
}
