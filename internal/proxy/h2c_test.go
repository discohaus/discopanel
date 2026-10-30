package proxy

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"testing"

	"golang.org/x/net/http2"

	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Proves agent style cleartext http2 streams survive the lane
func TestPanelLaneCarriesH2C(t *testing.T) {
	// Backend mirrors the panel rpc server's h2c wrapping
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			http.Error(w, "not http2", http.StatusHTTPVersionNotSupported)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		buf := make([]byte, 512)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
			if err != nil {
				return
			}
		}
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

	sock := NewListenerSocket(&Config{ListenAddr: "127.0.0.1:0", Logger: logger.New()})
	if err := sock.Start(); err != nil {
		t.Fatalf("socket start failed %v", err)
	}
	defer sock.Stop()
	sock.SetRoutes([]Route{{
		ServerID:    "panel",
		OwnerKind:   OwnerPanel,
		OwnerID:     OwnerPanel,
		Protocol:    v1.ModuleProtocol_MODULE_PROTOCOL_HTTP,
		BackendHost: "127.0.0.1",
		BackendPort: backendLn.Addr().(*net.TCPAddr).Port,
	}})

	// Client transport matches the runtime agent exactly
	tr := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	bodyR, bodyW := io.Pipe()
	req, err := http.NewRequest(http.MethodPost, "http://"+sock.listener.Addr().String()+"/agent", bodyR)
	if err != nil {
		t.Fatalf("request build failed %v", err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("h2c round trip failed %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d", resp.StatusCode)
	}

	// Two turns prove the stream is full duplex
	for _, msg := range []string{"ping from agent", "telemetry frame"} {
		if _, err := bodyW.Write([]byte(msg)); err != nil {
			t.Fatalf("stream write failed %v", err)
		}
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(resp.Body, got); err != nil {
			t.Fatalf("stream read failed %v", err)
		}
		if string(got) != msg {
			t.Fatalf("echo mismatch %q", got)
		}
	}
	bodyW.Close()
}
