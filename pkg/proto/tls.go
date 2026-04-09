package proto

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// LoadTLSClientConfig builds a tls.Config for dialing wss:// with a client
// certificate (mTLS). If serverCAFile is empty, the system root CAs are used.
func LoadTLSClientConfig(certFile, keyFile, serverCAFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load client certificate: %w", err)
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	if serverCAFile != "" {
		pem, err := os.ReadFile(serverCAFile)
		if err != nil {
			return nil, fmt.Errorf("read server CA bundle: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates parsed from server CA file %q", serverCAFile)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}
