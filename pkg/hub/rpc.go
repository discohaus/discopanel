package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Connect rpc paths on the support service
const (
	heartbeatPath         = "/discohaus.support.v1.SupportService/Heartbeat"
	reachabilityCheckPath = "/discohaus.support.v1.SupportService/ReachabilityCheck"
)

// Largest rpc answer the panel reads
const maxRPCBody = 256 << 10

// Servers sharing one loader and Minecraft version
type ServerSummary struct {
	Loader    string `json:"loader"`
	MCVersion string `json:"mcVersion"`
	Count     int32  `json:"count"`
}

// One heartbeat, proto3 json of discohaus.support.v1.HeartbeatRequest
type HeartbeatRequest struct {
	InstallID         string          `json:"installId"`
	Version           string          `json:"version"`
	OS                string          `json:"os"`
	Arch              string          `json:"arch"`
	DockerVersion     string          `json:"dockerVersion"`
	Servers           []ServerSummary `json:"servers"`
	Modules           []string        `json:"modules"`
	RuntimeImages     []string        `json:"runtimeImages"`
	DiagnosticsPassed int32           `json:"diagnosticsPassed"`
	DiagnosticsWarned int32           `json:"diagnosticsWarned"`
	DiagnosticsFailed int32           `json:"diagnosticsFailed"`
	PanelPort         int32           `json:"panelPort"`
	UptimeSeconds     int64           `json:"uptimeSeconds,string"`
}

// What the hub answers a heartbeat with
type HeartbeatResponse struct {
	LatestVersion    string `json:"latestVersion"`
	LatestReleaseURL string `json:"latestReleaseUrl"`
	Notice           string `json:"notice"`
	NextInSeconds    int32  `json:"nextInSeconds"`
}

// Asks the hub to connect back to this panel's public address
type ReachabilityCheckRequest struct {
	InstallID string `json:"installId"`
	Port      int32  `json:"port"`
	// tcp or minecraft
	Protocol string `json:"protocol"`
	Hostname string `json:"hostname"`
}

// What the hub saw when it connected back
type ReachabilityCheckResponse struct {
	Reachable  bool   `json:"reachable"`
	ObservedIP string `json:"observedIp"`
	LatencyMs  int32  `json:"latencyMs"`
	Error      string `json:"error"`
}

// A non 200 answer from the hub, decoded from its connect error body
type Error struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("hub answered HTTP %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("hub answered HTTP %d", e.Status)
}

// True when the hub asked this panel to slow down
func (e *Error) RateLimited() bool {
	return e.Status == http.StatusTooManyRequests
}

// True when the hub has paused telemetry
func (e *Error) Paused() bool {
	return e.Status == http.StatusServiceUnavailable
}

// Hub error carried by err, nil for network and decode failures
func AsError(err error) *Error {
	var he *Error
	if errors.As(err, &he) {
		return he
	}
	return nil
}

// Retry-After header as a duration, zero when absent or unreadable
func retryAfter(h http.Header) time.Duration {
	raw := h.Get("Retry-After")
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(raw); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}

// Posts one connect rpc as json and decodes the answer into out
func call(ctx context.Context, path string, in any, out any, tenantToken string) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, SupportBase()+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if tenantToken != "" {
		req.Header.Set("Authorization", TenantAuthScheme+" "+tenantToken)
	}
	resp, err := rpcClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRPCBody))
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		he := &Error{Status: resp.StatusCode, RetryAfter: retryAfter(resp.Header)}
		var connectErr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(raw, &connectErr) == nil {
			he.Code = connectErr.Code
			he.Message = connectErr.Message
			if he.Message == "" {
				he.Message = connectErr.Error
			}
		}
		return he
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// Sends one heartbeat, presenting the tenant token when this panel has one
func Heartbeat(ctx context.Context, req *HeartbeatRequest) (*HeartbeatResponse, error) {
	var out HeartbeatResponse
	if err := call(ctx, heartbeatPath, req, &out, current.Load().tenantToken); err != nil {
		return nil, err
	}
	return &out, nil
}

// Asks the hub whether a port answers on this panel's public address
func ReachabilityCheck(ctx context.Context, port int, protocol, hostname string) (*ReachabilityCheckResponse, error) {
	req := ReachabilityCheckRequest{InstallID: InstallID(), Port: int32(port), Protocol: protocol, Hostname: hostname}
	var out ReachabilityCheckResponse
	if err := call(ctx, reachabilityCheckPath, &req, &out, ""); err != nil {
		return nil, err
	}
	return &out, nil
}
