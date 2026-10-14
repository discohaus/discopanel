// Package mojang talks to the session servers
package mojang

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Default session server base
const DefaultSessionBase = "https://sessionserver.mojang.com"

// One signed profile property such as textures
type Property struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Signature string `json:"signature,omitempty"`
}

// Authenticated player profile from the session server
type Profile struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Properties []Property `json:"properties"`
}

// Digest for the join hash, minecraft flavored hex
func Digest(serverID string, secret, publicKeyDER []byte) string {
	h := sha1.New()
	h.Write([]byte(serverID))
	h.Write(secret)
	h.Write(publicKeyDER)
	sum := h.Sum(nil)
	n := new(big.Int).SetBytes(sum)
	negative := sum[0]&0x80 != 0
	if negative {
		// Twos complement magnitude for the minus form
		n.Sub(new(big.Int).Lsh(big.NewInt(1), uint(len(sum)*8)), n)
		return "-" + n.Text(16)
	}
	return n.Text(16)
}

// Checks the client joined this server with mojang
func HasJoined(ctx context.Context, base, username, hash string) (*Profile, error) {
	if base == "" {
		base = DefaultSessionBase
	}
	endpoint := fmt.Sprintf("%s/session/minecraft/hasJoined?username=%s&serverId=%s",
		strings.TrimSuffix(base, "/"), url.QueryEscape(username), url.QueryEscape(hash))

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNoContent, http.StatusForbidden:
		return nil, fmt.Errorf("session check refused for %s", username)
	default:
		return nil, fmt.Errorf("session server answered %d", resp.StatusCode)
	}

	profile := &Profile{}
	if err := json.NewDecoder(resp.Body).Decode(profile); err != nil {
		return nil, fmt.Errorf("session profile unreadable: %w", err)
	}
	if profile.ID == "" || profile.Name == "" {
		return nil, fmt.Errorf("session profile empty for %s", username)
	}
	return profile, nil
}

// Offline mode uuid the vanilla way
func OfflineUUID(name string) [16]byte {
	sum := md5.Sum([]byte("OfflinePlayer:" + name))
	sum[6] = sum[6]&0x0f | 0x30
	sum[8] = sum[8]&0x3f | 0x80
	return sum
}

// Dashed uuid text from the compact profile id
func DashedUUID(id string) string {
	if len(id) != 32 {
		return id
	}
	return id[0:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:32]
}

// Compact uuid bytes from the profile id
func UUIDBytes(id string) ([16]byte, error) {
	var out [16]byte
	clean := strings.ReplaceAll(id, "-", "")
	raw, err := hex.DecodeString(clean)
	if err != nil || len(raw) != 16 {
		return out, fmt.Errorf("bad uuid %q", id)
	}
	copy(out[:], raw)
	return out, nil
}
