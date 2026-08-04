package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"golang.org/x/net/http2"
)

// Builds a throwaway pair covering the given names
func testCertPEM(t *testing.T, names ...string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key generation failed %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert generation failed %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("key marshal failed %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return string(certPEM), string(keyPEM)
}

// Writes a pair to disk as config file entries do
func testCertFiles(t *testing.T, names ...string) (config.TLSCertificate, string) {
	t.Helper()
	certPEM, keyPEM := testCertPEM(t, names...)
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certFile, []byte(certPEM), 0644); err != nil {
		t.Fatalf("cert write failed %v", err)
	}
	if err := os.WriteFile(keyFile, []byte(keyPEM), 0600); err != nil {
		t.Fatalf("key write failed %v", err)
	}
	return config.TLSCertificate{CertFile: certFile, KeyFile: keyFile}, certPEM
}

// Index loaded through the config file path
func testIndex(t *testing.T, entries ...config.TLSCertificate) *certIndex {
	t.Helper()
	idx := LoadTLSCertificates(entries, logger.New())
	if idx == nil {
		t.Fatal("index must load")
	}
	return idx
}

// Pool trusting one test pair
func testPool(t *testing.T, certPEM string) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(certPEM)) {
		t.Fatal("pool append failed")
	}
	return pool
}

// Broken entries skip and an empty load returns nil
func TestLoadTLSCertificates(t *testing.T) {
	if idx := LoadTLSCertificates(nil, logger.New()); idx != nil {
		t.Fatal("no entries must return nil")
	}
	missing := config.TLSCertificate{CertFile: "/nope/tls.crt", KeyFile: "/nope/tls.key"}
	if idx := LoadTLSCertificates([]config.TLSCertificate{missing}, logger.New()); idx != nil {
		t.Fatal("unreadable entries must return nil")
	}
	good, _ := testCertFiles(t, "map.example.com")
	idx := LoadTLSCertificates([]config.TLSCertificate{missing, good}, logger.New())
	if idx == nil || len(idx.entries) != 1 {
		t.Fatal("good entry must survive a broken sibling")
	}
}

// Wildcards cover one label, exacts beat them
func TestCertIndexMatch(t *testing.T) {
	wild, _ := testCertFiles(t, "*.sslip.io")
	exact, _ := testCertFiles(t, "map.example.com")
	idx := testIndex(t, wild, exact)

	if _, ok := idx.match("smp-192-168-1-5.sslip.io"); !ok {
		t.Fatal("dash label must match the wildcard")
	}
	if _, ok := idx.match("smp.192-168-1-5.sslip.io"); ok {
		t.Fatal("two labels must not match a single wildcard")
	}
	if _, ok := idx.match("sslip.io"); ok {
		t.Fatal("bare suffix must not match the wildcard")
	}
	if cert, ok := idx.match("MAP.example.com."); !ok || cert == nil {
		t.Fatal("exact match must normalize case and dots")
	}
	if _, ok := idx.match("other.example.com"); ok {
		t.Fatal("uncovered name must not match")
	}
}

// Https terminates and the lane sees the https scheme
func TestTLSTerminationServesHTTPS(t *testing.T) {
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen failed %v", err)
	}
	backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "proto=%s", r.Header.Get("X-Forwarded-Proto"))
	})}
	go func() { _ = backend.Serve(backendLn) }()
	defer backend.Close()

	entry, certPEM := testCertFiles(t, "map.example.com")
	sock := NewListenerSocket(&Config{
		ListenAddr: "127.0.0.1:0",
		Logger:     logger.New(),
		Certs:      testIndex(t, entry),
	})
	if err := sock.Start(); err != nil {
		t.Fatalf("socket start failed %v", err)
	}
	defer sock.Stop()
	sock.SetRoutes([]Route{{
		ServerID:    "svc",
		OwnerKind:   OwnerModule,
		OwnerID:     "svc",
		Hostname:    "map.example.com",
		Protocol:    v1.ModuleProtocol_MODULE_PROTOCOL_HTTP,
		BackendHost: "127.0.0.1",
		BackendPort: backendLn.Addr().(*net.TCPAddr).Port,
	}})

	conn, err := tls.Dial("tcp", sock.listener.Addr().String(), &tls.Config{
		ServerName: "map.example.com",
		RootCAs:    testPool(t, certPEM),
	})
	if err != nil {
		t.Fatalf("tls dial failed %v", err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: map.example.com\r\nConnection: close\r\n\r\n")
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read failed %v", err)
	}
	if !strings.Contains(string(body), "200 OK") || !strings.Contains(string(body), "proto=https") {
		t.Fatalf("unexpected response %q", body)
	}
}

// Plain http on the same socket keeps working untouched
func TestTLSSocketStillServesPlainHTTP(t *testing.T) {
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen failed %v", err)
	}
	backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "proto=%s", r.Header.Get("X-Forwarded-Proto"))
	})}
	go func() { _ = backend.Serve(backendLn) }()
	defer backend.Close()

	entry, _ := testCertFiles(t, "map.example.com")
	sock := NewListenerSocket(&Config{
		ListenAddr: "127.0.0.1:0",
		Logger:     logger.New(),
		Certs:      testIndex(t, entry),
	})
	if err := sock.Start(); err != nil {
		t.Fatalf("socket start failed %v", err)
	}
	defer sock.Stop()
	sock.SetRoutes([]Route{{
		ServerID:    "svc",
		OwnerKind:   OwnerModule,
		OwnerID:     "svc",
		Hostname:    "map.example.com",
		Protocol:    v1.ModuleProtocol_MODULE_PROTOCOL_HTTP,
		BackendHost: "127.0.0.1",
		BackendPort: backendLn.Addr().(*net.TCPAddr).Port,
	}})

	req, err := http.NewRequest(http.MethodGet, "http://"+sock.listener.Addr().String()+"/", nil)
	if err != nil {
		t.Fatalf("request build failed %v", err)
	}
	req.Host = "map.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("plain get failed %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "proto=http") || strings.Contains(string(body), "https") {
		t.Fatalf("plain request must stay http, got %q", body)
	}
}

// Edge scheme claims pass through, bare conns say http
func TestForwardedProtoPassthrough(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"claim passes untouched", "https", "proto=https"},
		{"chains pass untouched", "https, http", "proto=https, http"},
		{"no claim stays http", "", "proto=http"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backendLn, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("backend listen failed %v", err)
			}
			backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, "proto=%s", r.Header.Get("X-Forwarded-Proto"))
			})}
			go func() { _ = backend.Serve(backendLn) }()
			defer backend.Close()

			sock := NewListenerSocket(&Config{
				ListenAddr: "127.0.0.1:0",
				Logger:     logger.New(),
			})
			if err := sock.Start(); err != nil {
				t.Fatalf("socket start failed %v", err)
			}
			defer sock.Stop()
			sock.SetRoutes([]Route{{
				ServerID:    "svc",
				OwnerKind:   OwnerModule,
				OwnerID:     "svc",
				Hostname:    "map.example.com",
				Protocol:    v1.ModuleProtocol_MODULE_PROTOCOL_HTTP,
				BackendHost: "127.0.0.1",
				BackendPort: backendLn.Addr().(*net.TCPAddr).Port,
			}})

			req, err := http.NewRequest(http.MethodGet, "http://"+sock.listener.Addr().String()+"/", nil)
			if err != nil {
				t.Fatalf("request build failed %v", err)
			}
			req.Host = "map.example.com"
			if tc.value != "" {
				req.Header.Set("X-Forwarded-Proto", tc.value)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("get failed %v", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if string(body) != tc.want {
				t.Fatalf("scheme wrong, want %q got %q", tc.want, body)
			}
		})
	}
}

// Matched hello unwraps and relays plaintext to the backend
func TestTLSTerminatedRelay(t *testing.T) {
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen failed %v", err)
	}
	defer backendLn.Close()
	got := make(chan []byte, 1)
	go func() {
		conn, err := backendLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		got <- buf[:n]
	}()

	entry, certPEM := testCertFiles(t, "relay.example.com")
	sock := NewListenerSocket(&Config{
		ListenAddr: "127.0.0.1:0",
		Logger:     logger.New(),
		Certs:      testIndex(t, entry),
	})
	if err := sock.Start(); err != nil {
		t.Fatalf("socket start failed %v", err)
	}
	defer sock.Stop()
	sock.SetRoutes([]Route{{
		ServerID:    "svc",
		OwnerKind:   OwnerModule,
		OwnerID:     "svc",
		Protocol:    v1.ModuleProtocol_MODULE_PROTOCOL_TCP,
		BackendHost: "127.0.0.1",
		BackendPort: backendLn.Addr().(*net.TCPAddr).Port,
	}})

	conn, err := tls.Dial("tcp", sock.listener.Addr().String(), &tls.Config{
		ServerName: "relay.example.com",
		RootCAs:    testPool(t, certPEM),
	})
	if err != nil {
		t.Fatalf("tls dial failed %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("secret payload")); err != nil {
		t.Fatalf("write failed %v", err)
	}
	select {
	case payload := <-got:
		if string(payload) != "secret payload" {
			t.Fatalf("backend saw %q", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("backend never received the plaintext")
	}
}

// Unknown names pass the encrypted bytes through untouched
func TestTLSUnknownNamePassthroughToRelay(t *testing.T) {
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen failed %v", err)
	}
	defer backendLn.Close()
	got := make(chan byte, 1)
	go func() {
		conn, err := backendLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1)
		if _, err := io.ReadFull(conn, buf); err == nil {
			got <- buf[0]
		}
	}()

	entry, _ := testCertFiles(t, "other.example.com")
	sock := NewListenerSocket(&Config{
		ListenAddr: "127.0.0.1:0",
		Logger:     logger.New(),
		Certs:      testIndex(t, entry),
	})
	if err := sock.Start(); err != nil {
		t.Fatalf("socket start failed %v", err)
	}
	defer sock.Stop()
	sock.SetRoutes([]Route{{
		ServerID:    "svc",
		OwnerKind:   OwnerModule,
		OwnerID:     "svc",
		Protocol:    v1.ModuleProtocol_MODULE_PROTOCOL_TCP,
		BackendHost: "127.0.0.1",
		BackendPort: backendLn.Addr().(*net.TCPAddr).Port,
	}})

	// Backend speaks no tls so the client handshake dies
	conn, err := tls.Dial("tcp", sock.listener.Addr().String(), &tls.Config{
		ServerName:         "unknown.example.com",
		InsecureSkipVerify: true,
	})
	if err == nil {
		conn.Close()
	}
	select {
	case first := <-got:
		if first != tlsRecordHandshake {
			t.Fatalf("backend saw %#x, want the raw hello", first)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("backend never received the hello bytes")
	}
}

// No cert and no relay closes the handshake
func TestTLSUnknownNameWithoutRelayCloses(t *testing.T) {
	entry, _ := testCertFiles(t, "other.example.com")
	sock := NewListenerSocket(&Config{
		ListenAddr: "127.0.0.1:0",
		Logger:     logger.New(),
		Certs:      testIndex(t, entry),
	})
	if err := sock.Start(); err != nil {
		t.Fatalf("socket start failed %v", err)
	}
	defer sock.Stop()
	sock.SetRoutes([]Route{{
		ServerID:    "svc",
		OwnerKind:   OwnerServer,
		OwnerID:     "svc",
		Hostname:    "play.example.com",
		Protocol:    v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT,
		BackendHost: "127.0.0.1",
		BackendPort: 1,
	}})

	_, err := tls.Dial("tcp", sock.listener.Addr().String(), &tls.Config{
		ServerName:         "unknown.example.com",
		InsecureSkipVerify: true,
	})
	if err == nil {
		t.Fatal("handshake must fail without a certificate")
	}
}

// Agent style http2 rides alpn through termination
func TestTLSCarriesH2ToPanelBackend(t *testing.T) {
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			http.Error(w, "not http2", http.StatusHTTPVersionNotSupported)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen failed %v", err)
	}
	backend := &http.Server{Handler: echo}
	backendProtocols := new(http.Protocols)
	backendProtocols.SetHTTP1(true)
	backendProtocols.SetUnencryptedHTTP2(true)
	backend.Protocols = backendProtocols
	go func() { _ = backend.Serve(backendLn) }()
	defer backend.Close()

	entry, certPEM := testCertFiles(t, "panel.example.com")
	sock := NewListenerSocket(&Config{
		ListenAddr: "127.0.0.1:0",
		Logger:     logger.New(),
		Certs:      testIndex(t, entry),
	})
	if err := sock.Start(); err != nil {
		t.Fatalf("socket start failed %v", err)
	}
	defer sock.Stop()
	sock.SetRoutes([]Route{{
		ServerID:    "panel",
		OwnerKind:   OwnerPanel,
		OwnerID:     OwnerPanel,
		Hostname:    "panel.example.com",
		Protocol:    v1.ModuleProtocol_MODULE_PROTOCOL_HTTP,
		BackendHost: "127.0.0.1",
		BackendPort: backendLn.Addr().(*net.TCPAddr).Port,
	}})

	addr := sock.listener.Addr().String()
	tr := &http2.Transport{
		TLSClientConfig: &tls.Config{
			ServerName: "panel.example.com",
			RootCAs:    testPool(t, certPEM),
		},
		DialTLSContext: func(ctx context.Context, network, _ string, cfg *tls.Config) (net.Conn, error) {
			return tls.Dial(network, addr, cfg)
		},
	}
	req, err := http.NewRequest(http.MethodGet, "https://panel.example.com/agent", nil)
	if err != nil {
		t.Fatalf("request build failed %v", err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("h2 round trip failed %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d", resp.StatusCode)
	}
}
