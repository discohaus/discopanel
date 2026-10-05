package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

const testInstallID = "0123456789abcdef0123456789abcdef"

// Points the package at test servers, restoring the previous settings afterwards
func configureForTest(t *testing.T, s Settings) {
	t.Helper()
	prev := current.Load()
	if err := Configure(s); err != nil {
		t.Fatalf("configure: %v", err)
	}
	t.Cleanup(func() { current.Store(prev) })
}

// Records the headers of every request it serves
func recordingServer(t *testing.T, status int, body string) (*httptest.Server, *[]http.Header) {
	t.Helper()
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestConfigureRejectsBadInput(t *testing.T) {
	cases := []Settings{
		{SupportBase: "", IndexBase: DefaultIndexBase},
		{SupportBase: "support.example", IndexBase: DefaultIndexBase},
		{SupportBase: "ftp://support.example", IndexBase: DefaultIndexBase},
		{SupportBase: DefaultSupportBase, IndexBase: "https://index.example/?q=1"},
		{SupportBase: DefaultSupportBase, IndexBase: DefaultIndexBase, InstallID: "SHOUTING"},
		{SupportBase: DefaultSupportBase, IndexBase: DefaultIndexBase, InstallID: "abc"},
	}
	for _, c := range cases {
		if err := Configure(c); err == nil {
			t.Errorf("Configure(%+v) should fail", c)
		}
	}
}

func TestConfigureTrimsSlashes(t *testing.T) {
	configureForTest(t, Settings{SupportBase: "https://support.example/", IndexBase: "https://index.example/base//", InstallID: testInstallID})
	if got := SupportBase(); got != "https://support.example" {
		t.Errorf("support base = %q", got)
	}
	if got := IndexBase(); got != "https://index.example/base" {
		t.Errorf("index base = %q", got)
	}
	if got := SupportHost(); got != "support.example" {
		t.Errorf("support host = %q", got)
	}
	if got := IndexHost(); got != "index.example" {
		t.Errorf("index host = %q", got)
	}
}

// Hub hosts get the install id header, every other host does not
func TestTransportHeaderSelection(t *testing.T) {
	support, supportSeen := recordingServer(t, http.StatusOK, "{}")
	index, indexSeen := recordingServer(t, http.StatusOK, "{}")
	other, otherSeen := recordingServer(t, http.StatusOK, "{}")
	configureForTest(t, Settings{SupportBase: support.URL, IndexBase: index.URL, InstallID: testInstallID, TenantToken: "tenant-secret"})

	client := NewHTTPClient(5 * time.Second)
	for _, u := range []string{support.URL + "/health", index.URL + "/modrinth/v2/search", other.URL + "/anything"} {
		resp, err := client.Get(u)
		if err != nil {
			t.Fatalf("get %s: %v", u, err)
		}
		resp.Body.Close()
	}
	for name, seen := range map[string]*[]http.Header{"support": supportSeen, "index": indexSeen} {
		if len(*seen) != 1 {
			t.Fatalf("%s served %d requests", name, len(*seen))
		}
		if got := (*seen)[0].Get(InstallIDHeader); got != testInstallID {
			t.Errorf("%s saw %s=%q, want the install id", name, InstallIDHeader, got)
		}
		if got := (*seen)[0].Get("Authorization"); got != "" {
			t.Errorf("%s saw Authorization=%q on a plain request, the tenant token belongs to the heartbeat only", name, got)
		}
	}
	if len(*otherSeen) != 1 {
		t.Fatalf("other served %d requests", len(*otherSeen))
	}
	if got := (*otherSeen)[0].Get(InstallIDHeader); got != "" {
		t.Errorf("other host saw %s=%q, the header must stay on hub hosts", InstallIDHeader, got)
	}
}

// Hub hosts are matched by host alone, case insensitively, never by path or scheme
func TestIsHubURL(t *testing.T) {
	configureForTest(t, Settings{SupportBase: "https://support.example", IndexBase: "https://index.example:8443/base", InstallID: testInstallID})
	cases := map[string]bool{
		"https://support.example/health":                   true,
		"http://SUPPORT.example/discohaus.support.v1.X/Y":  true,
		"https://index.example:8443/modrinth/v2/search":    true,
		"https://index.example/modrinth/v2/search":         false,
		"https://api.modrinth.com/v2/search":               false,
		"https://support.example.evil.test/health":         false,
		"https://notsupport.example/health":                false,
		"https://github.com/discohaus/discopanel/releases": false,
	}
	for raw, want := range cases {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := IsHubURL(u); got != want {
			t.Errorf("IsHubURL(%s) = %v, want %v", raw, got, want)
		}
	}
	if IsHubURL(nil) {
		t.Error("a nil url is not a hub url")
	}
}

// A hub request before the install id exists must fail rather than go out bare
func TestTransportRefusesUnconfiguredHubRequest(t *testing.T) {
	support, seen := recordingServer(t, http.StatusOK, "{}")
	configureForTest(t, Settings{SupportBase: support.URL, IndexBase: DefaultIndexBase})
	_, err := NewHTTPClient(5 * time.Second).Get(support.URL + "/health")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
	if len(*seen) != 0 {
		t.Errorf("the request reached the hub without an install id")
	}
}

// The heartbeat is a connect json post carrying the install header and the tenant token
func TestHeartbeatWire(t *testing.T) {
	var gotPath, gotBody string
	var gotHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"latestVersion":"v1.9.0","latestReleaseUrl":"https://github.com/discohaus/discopanel/releases/tag/v1.9.0","nextInSeconds":1800}`)
	}))
	t.Cleanup(srv.Close)
	configureForTest(t, Settings{SupportBase: srv.URL, IndexBase: DefaultIndexBase, InstallID: testInstallID, TenantToken: "tenant-secret"})

	resp, err := Heartbeat(context.Background(), &HeartbeatRequest{
		InstallID:     testInstallID,
		Version:       "v1.8.0",
		OS:            "linux",
		Arch:          "amd64",
		DockerVersion: "28.5.2",
		Servers:       []ServerSummary{{Loader: "fabric", MCVersion: "1.21.1", Count: 2}},
		Modules:       []string{"bluemap"},
		RuntimeImages: []string{"ghcr.io/discohaus/discoruntime:java21"},
		PanelPort:     8080,
		UptimeSeconds: 61,
	})
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if gotPath != "/discohaus.support.v1.SupportService/Heartbeat" {
		t.Errorf("path = %q", gotPath)
	}
	if gotHeader.Get("Content-Type") != "application/json" || gotHeader.Get("Connect-Protocol-Version") != "1" {
		t.Errorf("headers = %v", gotHeader)
	}
	if gotHeader.Get(InstallIDHeader) != testInstallID {
		t.Errorf("install header = %q", gotHeader.Get(InstallIDHeader))
	}
	if gotHeader.Get("Authorization") != "Tenant tenant-secret" {
		t.Errorf("authorization = %q", gotHeader.Get("Authorization"))
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(gotBody), &decoded); err != nil {
		t.Fatalf("body %q: %v", gotBody, err)
	}
	if decoded["installId"] != testInstallID || decoded["uptimeSeconds"] != "61" || decoded["panelPort"] != float64(8080) {
		t.Errorf("body = %s", gotBody)
	}
	if resp.LatestVersion != "v1.9.0" || resp.NextInSeconds != 1800 || resp.Notice != "" {
		t.Errorf("response = %+v", resp)
	}
}

// Without a tenant token no authorization header goes out
func TestHeartbeatWithoutTenantToken(t *testing.T) {
	srv, seen := recordingServer(t, http.StatusOK, `{}`)
	configureForTest(t, Settings{SupportBase: srv.URL, IndexBase: DefaultIndexBase, InstallID: testInstallID})
	if _, err := Heartbeat(context.Background(), &HeartbeatRequest{InstallID: testInstallID}); err != nil {
		t.Fatal(err)
	}
	if got := (*seen)[0].Get("Authorization"); got != "" {
		t.Errorf("authorization = %q, want none", got)
	}
}

// Connect errors decode into a typed hub error with the status and code
func TestHeartbeatErrors(t *testing.T) {
	cases := []struct {
		status      int
		body        string
		retryAfter  string
		paused      bool
		rateLimited bool
		wantRetry   time.Duration
		wantMessage string
	}{
		{http.StatusServiceUnavailable, `{"code":"unavailable","message":"telemetry is paused"}`, "", true, false, 0, "telemetry is paused"},
		{http.StatusTooManyRequests, `{"code":"resource_exhausted","message":"too many requests from this install"}`, "120", false, true, 2 * time.Minute, "too many requests from this install"},
		{http.StatusBadRequest, `{"code":"invalid_argument","message":"version exceeds 64 characters"}`, "", false, false, 0, "version exceeds 64 characters"},
		{http.StatusInternalServerError, `{"error":"internal error"}`, "", false, false, 0, "internal error"},
		{http.StatusBadGateway, `not json`, "", false, false, 0, ""},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c.retryAfter != "" {
				w.Header().Set("Retry-After", c.retryAfter)
			}
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		}))
		configureForTest(t, Settings{SupportBase: srv.URL, IndexBase: DefaultIndexBase, InstallID: testInstallID})
		_, err := Heartbeat(context.Background(), &HeartbeatRequest{})
		srv.Close()
		he := AsError(err)
		if he == nil {
			t.Fatalf("status %d: err = %v, want a hub error", c.status, err)
		}
		if he.Status != c.status || he.Paused() != c.paused || he.RateLimited() != c.rateLimited || he.RetryAfter != c.wantRetry || he.Message != c.wantMessage {
			t.Errorf("status %d: got %+v", c.status, he)
		}
	}
}

// A dead hub is a plain error, not a hub error
func TestHeartbeatNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	configureForTest(t, Settings{SupportBase: base, IndexBase: DefaultIndexBase, InstallID: testInstallID})
	_, err := Heartbeat(context.Background(), &HeartbeatRequest{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if AsError(err) != nil {
		t.Errorf("network failure decoded as a hub error: %v", err)
	}
}

// The relay call never carries the tenant token and reports what the hub saw
func TestReachabilityCheckWire(t *testing.T) {
	var gotBody string
	var gotHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		if r.URL.Path != "/discohaus.support.v1.SupportService/ReachabilityCheck" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, `{"reachable":true,"observedIp":"203.0.113.9","latencyMs":42,"checkedAt":"2026-09-11T00:00:00Z"}`)
	}))
	t.Cleanup(srv.Close)
	configureForTest(t, Settings{SupportBase: srv.URL, IndexBase: DefaultIndexBase, InstallID: testInstallID, TenantToken: "tenant-secret"})
	res, err := ReachabilityCheck(context.Background(), 25565, "minecraft", "discopanel-probe.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if gotHeader.Get("Authorization") != "" {
		t.Errorf("relay carried authorization %q", gotHeader.Get("Authorization"))
	}
	if gotHeader.Get(InstallIDHeader) != testInstallID {
		t.Errorf("relay install header = %q", gotHeader.Get(InstallIDHeader))
	}
	want := `{"installId":"` + testInstallID + `","port":25565,"protocol":"minecraft","hostname":"discopanel-probe.invalid"}`
	if gotBody != want {
		t.Errorf("body = %s, want %s", gotBody, want)
	}
	if !res.Reachable || res.ObservedIP != "203.0.113.9" || res.LatencyMs != 42 {
		t.Errorf("result = %+v", res)
	}
}

// Hosted follows the tenant token
func TestHosted(t *testing.T) {
	t.Cleanup(func() {
		current.Store(mustState(Settings{SupportBase: DefaultSupportBase, IndexBase: DefaultIndexBase}))
	})
	if err := Configure(Settings{SupportBase: DefaultSupportBase, IndexBase: DefaultIndexBase}); err != nil {
		t.Fatal(err)
	}
	if Hosted() {
		t.Error("no tenant token should mean self hosted")
	}
	if err := Configure(Settings{SupportBase: DefaultSupportBase, IndexBase: DefaultIndexBase, TenantToken: " tok "}); err != nil {
		t.Fatal(err)
	}
	if !Hosted() {
		t.Error("a tenant token should mean hosted")
	}
}
