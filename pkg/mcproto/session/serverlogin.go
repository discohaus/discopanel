// Package session runs minecraft login state machines
package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"fmt"
	"io"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/mojang"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// Login state packet ids, stable across versions
const (
	loginDisconnectID      = 0x00
	encryptionRequestID    = 0x01
	encryptionResponseID   = 0x01
	loginStartID           = 0x00
	loginSuccessID         = 0x02
	setCompressionID       = 0x03
	loginAcknowledgedID    = 0x03
	maxEncryptionFrameSize = 4096
)

// Last protocol using short prefixed crypto arrays
const shortArrayProtocolMax = 5

// Protocols accepting salt signatures over verify tokens
const (
	saltSignatureProtocolMin = 759
	saltSignatureProtocolMax = 760
)

// First protocol carrying the authenticate flag
const authenticateFlagProtocol = 766

// Shared keypair and settings for shim logins
type ServerAuth struct {
	Key         *rsa.PrivateKey
	PublicDER   []byte
	Online      bool
	SessionBase string
}

// Generates the keypair vanilla sized at 1024 bits
func NewServerAuth(online bool) (*ServerAuth, error) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	return &ServerAuth{Key: key, PublicDER: der, Online: online}, nil
}

// Streams and identity settled by one login
type AuthResult struct {
	Name    string
	Profile *mojang.Profile
	Secret  []byte
	R       io.Reader
	W       io.Writer
}

// Runs the server side encryption dance and session check
func (a *ServerAuth) Authenticate(ctx context.Context, r io.Reader, w io.Writer, protocol int32, ls *mcproto.LoginStart) (*AuthResult, error) {
	if !a.Online {
		return &AuthResult{Name: ls.Name, R: r, W: w}, nil
	}

	token := make([]byte, 4)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}

	if err := a.writeEncryptionRequest(w, protocol, token); err != nil {
		return nil, fmt.Errorf("encryption request failed: %w", err)
	}

	secret, err := a.readEncryptionResponse(r, protocol, token)
	if err != nil {
		return nil, fmt.Errorf("encryption response failed: %w", err)
	}

	cr, err := packet.NewCipherReader(r, secret)
	if err != nil {
		return nil, err
	}
	cw, err := packet.NewCipherWriter(w, secret)
	if err != nil {
		return nil, err
	}

	hash := mojang.Digest("", secret, a.PublicDER)
	profile, err := mojang.HasJoined(ctx, a.SessionBase, ls.Name, hash)
	if err != nil {
		// Kick rides the fresh cipher the client expects
		return &AuthResult{Name: ls.Name, Secret: secret, R: cr, W: cw}, err
	}

	return &AuthResult{Name: profile.Name, Profile: profile, Secret: secret, R: cr, W: cw}, nil
}

// Builds the encryption request for one protocol
func (a *ServerAuth) writeEncryptionRequest(w io.Writer, protocol int32, token []byte) error {
	var body bytes.Buffer
	if err := mcproto.WriteVarInt(&body, encryptionRequestID); err != nil {
		return err
	}
	if err := packet.WriteString(&body, ""); err != nil {
		return err
	}
	writeBytes := packet.WriteVarBytes
	if protocol <= shortArrayProtocolMax {
		writeBytes = packet.WriteShortBytes
	}
	if err := writeBytes(&body, a.PublicDER); err != nil {
		return err
	}
	if err := writeBytes(&body, token); err != nil {
		return err
	}
	if protocol >= authenticateFlagProtocol {
		if err := packet.WriteBool(&body, true); err != nil {
			return err
		}
	}
	return packet.WriteFrame(w, body.Bytes())
}

// Reads the response and recovers the shared secret
func (a *ServerAuth) readEncryptionResponse(r io.Reader, protocol int32, token []byte) ([]byte, error) {
	frame, err := packet.ReadFrame(r)
	if err != nil {
		return nil, err
	}
	if len(frame) > maxEncryptionFrameSize {
		return nil, fmt.Errorf("encryption response too big: %d", len(frame))
	}
	buf := bytes.NewReader(frame)
	id, err := mcproto.ReadVarInt(buf)
	if err != nil {
		return nil, err
	}
	if id != encryptionResponseID {
		return nil, fmt.Errorf("expected encryption response, got %d", id)
	}

	readBytes := func(br *bytes.Reader) ([]byte, error) { return packet.ReadVarBytes(br, maxEncryptionFrameSize) }
	if protocol <= shortArrayProtocolMax {
		readBytes = func(br *bytes.Reader) ([]byte, error) { return packet.ReadShortBytes(br, maxEncryptionFrameSize) }
	}

	encSecret, err := readBytes(buf)
	if err != nil {
		return nil, err
	}

	var encToken []byte
	if protocol >= saltSignatureProtocolMin && protocol <= saltSignatureProtocolMax {
		hasToken, err := packet.ReadBool(buf)
		if err != nil {
			return nil, err
		}
		if hasToken {
			if encToken, err = readBytes(buf); err != nil {
				return nil, err
			}
		}
		// Salt signature identity still proven by session check
	} else {
		if encToken, err = readBytes(buf); err != nil {
			return nil, err
		}
	}

	secret, err := rsa.DecryptPKCS1v15(rand.Reader, a.Key, encSecret)
	if err != nil {
		return nil, fmt.Errorf("secret decrypt failed: %w", err)
	}
	if len(secret) != 16 {
		return nil, fmt.Errorf("shared secret length %d", len(secret))
	}

	if encToken != nil {
		plainToken, err := rsa.DecryptPKCS1v15(rand.Reader, a.Key, encToken)
		if err != nil {
			return nil, fmt.Errorf("token decrypt failed: %w", err)
		}
		if !bytes.Equal(plainToken, token) {
			return nil, fmt.Errorf("verify token mismatch")
		}
	}

	return secret, nil
}
