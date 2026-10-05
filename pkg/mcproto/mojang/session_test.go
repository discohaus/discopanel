package mojang

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Digest matches the classic published vectors
func TestDigestVectors(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Notch", "4ed1f46bbe04bc756bcb17c0c7ce3e4632f06a48"},
		{"jeb_", "-7c9d5b0044c130109a5d7b5fb5c317c02b4e28c1"},
		{"simon", "88e16a1019277b15d58faf0541e11910eb756f6"},
	}
	for _, tc := range cases {
		if got := Digest(tc.input, nil, nil); got != tc.want {
			t.Fatalf("digest %q = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// Offline uuids stay stable with version bits set
func TestOfflineUUID(t *testing.T) {
	a := OfflineUUID("Steve")
	b := OfflineUUID("Steve")
	if a != b {
		t.Fatal("offline uuid must be deterministic")
	}
	if a[6]&0xf0 != 0x30 {
		t.Fatalf("version nibble = %x, want 3", a[6]>>4)
	}
	if a[8]&0xc0 != 0x80 {
		t.Fatal("variant bits wrong")
	}
	if OfflineUUID("Alex") == a {
		t.Fatal("names must differ")
	}
}

// Uuid text helpers agree with each other
func TestUUIDHelpers(t *testing.T) {
	compact := "069a79f444e94726a5befca90e38aaf5"
	dashed := DashedUUID(compact)
	if dashed != "069a79f4-44e9-4726-a5be-fca90e38aaf5" {
		t.Fatalf("dashed = %q", dashed)
	}
	raw, err := UUIDBytes(dashed)
	if err != nil {
		t.Fatalf("bytes failed %v", err)
	}
	if raw[0] != 0x06 || raw[15] != 0xf5 {
		t.Fatalf("bytes wrong: %x", raw)
	}
}

// Session check parses profiles and rejects misses
func TestHasJoined(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("username") == "Steve" {
			json.NewEncoder(w).Encode(Profile{
				ID:   "069a79f444e94726a5befca90e38aaf5",
				Name: "Steve",
				Properties: []Property{
					{Name: "textures", Value: "abc"},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	profile, err := HasJoined(context.Background(), srv.URL, "Steve", "hash")
	if err != nil {
		t.Fatalf("hasjoined failed %v", err)
	}
	if profile.Name != "Steve" || len(profile.Properties) != 1 {
		t.Fatalf("profile wrong: %+v", profile)
	}

	if _, err := HasJoined(context.Background(), srv.URL, "Ghost", "hash"); err == nil {
		t.Fatal("miss must fail")
	}
}
