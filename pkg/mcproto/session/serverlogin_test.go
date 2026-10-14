package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/mojang"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// Fake session server always trusting Steve
func fakeSessionServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("username") != "Steve" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		json.NewEncoder(w).Encode(mojang.Profile{
			ID:   "069a79f444e94726a5befca90e38aaf5",
			Name: "Steve",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Login start body for one name
func testLoginStart(t *testing.T, name string) *mcproto.LoginStart {
	t.Helper()
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, 0x00)
	mcproto.WriteVarInt(&body, mcproto.VarInt(len(name)))
	body.WriteString(name)
	var framed bytes.Buffer
	if err := mcproto.WriteFramed(&framed, body.Bytes()); err != nil {
		t.Fatalf("frame failed %v", err)
	}
	ls, err := mcproto.ReadLoginStart(bytes.NewReader(framed.Bytes()))
	if err != nil {
		t.Fatalf("login start build failed %v", err)
	}
	return ls
}

// Plays the client half of the encryption dance
func clientRespond(t *testing.T, conn net.Conn, protocol int32, name string) (io.Reader, io.Writer) {
	t.Helper()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	frame, err := packet.ReadFrame(conn)
	if err != nil {
		t.Fatalf("request read failed %v", err)
	}
	buf := bytes.NewReader(frame)
	id, err := mcproto.ReadVarInt(buf)
	if err != nil || id != 0x01 {
		t.Fatalf("request id = %d, err %v", id, err)
	}
	if _, err := packet.ReadString(buf); err != nil {
		t.Fatalf("server id read failed %v", err)
	}

	readBytes := func() []byte {
		var b []byte
		var err error
		if protocol <= 5 {
			b, err = packet.ReadShortBytes(buf, 4096)
		} else {
			b, err = packet.ReadVarBytes(buf, 4096)
		}
		if err != nil {
			t.Fatalf("array read failed %v", err)
		}
		return b
	}
	pubDER := readBytes()
	token := readBytes()
	if protocol >= 766 {
		if _, err := packet.ReadBool(buf); err != nil {
			t.Fatalf("auth flag read failed %v", err)
		}
	}

	pubAny, err := x509.ParsePKIXPublicKey(pubDER)
	if err != nil {
		t.Fatalf("pubkey parse failed %v", err)
	}
	pub := pubAny.(*rsa.PublicKey)

	secret := make([]byte, 16)
	rand.Read(secret)
	encSecret, err := rsa.EncryptPKCS1v15(rand.Reader, pub, secret)
	if err != nil {
		t.Fatalf("secret encrypt failed %v", err)
	}
	encToken, err := rsa.EncryptPKCS1v15(rand.Reader, pub, token)
	if err != nil {
		t.Fatalf("token encrypt failed %v", err)
	}

	var body bytes.Buffer
	mcproto.WriteVarInt(&body, 0x01)
	writeBytes := packet.WriteVarBytes
	if protocol <= 5 {
		writeBytes = packet.WriteShortBytes
	}
	if protocol >= 759 && protocol <= 760 {
		writeBytes(&body, encSecret)
		packet.WriteBool(&body, true)
		writeBytes(&body, encToken)
	} else {
		writeBytes(&body, encSecret)
		writeBytes(&body, encToken)
	}
	if err := packet.WriteFrame(conn, body.Bytes()); err != nil {
		t.Fatalf("response write failed %v", err)
	}

	cr, err := packet.NewCipherReader(conn, secret)
	if err != nil {
		t.Fatalf("client cipher reader failed %v", err)
	}
	cw, err := packet.NewCipherWriter(conn, secret)
	if err != nil {
		t.Fatalf("client cipher writer failed %v", err)
	}
	return cr, cw
}

// Full dance settles ciphered streams both ways
func TestAuthenticateOnline(t *testing.T) {
	for _, protocol := range []int32{5, 47, 340, 760, 763, 772} {
		auth, err := NewServerAuth(true)
		if err != nil {
			t.Fatalf("auth build failed %v", err)
		}
		auth.SessionBase = fakeSessionServer(t).URL

		server, client := net.Pipe()
		defer server.Close()
		defer client.Close()

		type outcome struct {
			result *AuthResult
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			res, err := auth.Authenticate(context.Background(), server, server, protocol, testLoginStart(t, "Steve"))
			done <- outcome{res, err}
		}()

		cr, cw := clientRespond(t, client, protocol, "Steve")

		out := <-done
		if out.err != nil {
			t.Fatalf("protocol %d auth failed %v", protocol, out.err)
		}
		if out.result.Profile == nil || out.result.Profile.Name != "Steve" {
			t.Fatalf("protocol %d profile wrong: %+v", protocol, out.result.Profile)
		}

		// Ciphered echo proves both stream directions
		go func() {
			buf := make([]byte, 4)
			io.ReadFull(out.result.R, buf)
			out.result.W.Write(buf)
		}()
		if _, err := cw.Write([]byte("ping")); err != nil {
			t.Fatalf("client write failed %v", err)
		}
		echo := make([]byte, 4)
		if _, err := io.ReadFull(cr, echo); err != nil {
			t.Fatalf("client read failed %v", err)
		}
		if string(echo) != "ping" {
			t.Fatalf("echo = %q", echo)
		}
	}
}

// Unknown players get refused after the dance
func TestAuthenticateRejectsUnknown(t *testing.T) {
	auth, err := NewServerAuth(true)
	if err != nil {
		t.Fatalf("auth build failed %v", err)
	}
	auth.SessionBase = fakeSessionServer(t).URL

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		_, err := auth.Authenticate(context.Background(), server, server, 772, testLoginStart(t, "Ghost"))
		done <- err
	}()
	clientRespond(t, client, 772, "Ghost")
	if err := <-done; err == nil {
		t.Fatal("unknown player must fail")
	}
}

// Offline mode skips encryption entirely
func TestAuthenticateOffline(t *testing.T) {
	auth, err := NewServerAuth(false)
	if err != nil {
		t.Fatalf("auth build failed %v", err)
	}
	var r bytes.Buffer
	var w bytes.Buffer
	result, err := auth.Authenticate(context.Background(), &r, &w, 772, testLoginStart(t, "Steve"))
	if err != nil {
		t.Fatalf("offline auth failed %v", err)
	}
	if result.Name != "Steve" || result.Profile != nil {
		t.Fatalf("offline result wrong: %+v", result)
	}
	if w.Len() != 0 {
		t.Fatal("offline must write nothing")
	}
}
