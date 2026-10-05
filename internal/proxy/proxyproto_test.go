package proxy

import (
	"net"
	"testing"
)

func TestWrapIngressListener_V1(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	wrapped, err := WrapIngressListener(ln, nil)
	if err != nil {
		t.Fatalf("WrapIngressListener failed: %v", err)
	}
	defer wrapped.Close()

	done := make(chan struct{})
	var clientIP string

	go func() {
		defer close(done)
		conn, err := wrapped.Accept()
		if err != nil {
			t.Errorf("accept error: %v", err)
			return
		}
		defer conn.Close()
		clientIP, _, _ = net.SplitHostPort(conn.RemoteAddr().String())
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	// Write PROXY v1 text header
	header := "PROXY TCP4 203.0.113.195 70.41.3.18 56324 25565\r\n"
	if _, err := conn.Write([]byte(header)); err != nil {
		t.Fatalf("failed to write proxy header: %v", err)
	}

	<-done
	if clientIP != "203.0.113.195" {
		t.Errorf("expected client IP 203.0.113.195, got %s", clientIP)
	}
}

func TestWrapIngressListener_V2(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	wrapped, err := WrapIngressListener(ln, nil)
	if err != nil {
		t.Fatalf("WrapIngressListener failed: %v", err)
	}
	defer wrapped.Close()

	done := make(chan struct{})
	var clientIP string

	go func() {
		defer close(done)
		conn, err := wrapped.Accept()
		if err != nil {
			t.Errorf("accept error: %v", err)
			return
		}
		defer conn.Close()
		clientIP, _, _ = net.SplitHostPort(conn.RemoteAddr().String())
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	clientAddr, _ := net.ResolveTCPAddr("tcp", "198.51.100.42:12345")
	localAddr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:25565")
	if err := WriteProxyV2Header(conn, clientAddr, localAddr); err != nil {
		t.Fatalf("WriteProxyV2Header failed: %v", err)
	}

	<-done
	if clientIP != "198.51.100.42" {
		t.Errorf("expected client IP 198.51.100.42, got %s", clientIP)
	}
}
