// Package hub is the panel's one door to the discohaus hub: the support
// service, the upstream index, the install id every hub request carries,
// and the tenant token a hosted panel presents on its heartbeat.
package hub

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// Header every request to a hub host carries
const InstallIDHeader = "x-discohaus-install"

// Authorization scheme a hosted panel presents on its heartbeat
const TenantAuthScheme = "Tenant"

// Where the hub lives when nothing overrides it
const (
	DefaultSupportBase = "https://support.discohaus.app"
	DefaultIndexBase   = "https://index.discohaus.app"
)

// Every hub request shares this budget
const requestTimeout = 20 * time.Second

// Returned when a request to a hub host is attempted before Configure ran
var ErrNotConfigured = errors.New("hub: install id not configured")

// Where the hub is and who this panel is
type Settings struct {
	SupportBase string
	IndexBase   string
	// Route the upstreams through the index instead of their own hosts
	IndexEnabled bool
	InstallID    string
	TenantToken  string
}

// Parsed settings the accessors read
type state struct {
	support     *url.URL
	index       *url.URL
	indexOn     bool
	installID   string
	tenantToken string
}

var current atomic.Pointer[state]

func init() {
	current.Store(mustState(Settings{SupportBase: DefaultSupportBase, IndexBase: DefaultIndexBase}))
}

// Parses a base, refusing anything that is not an http origin
func parseBase(name, raw string) (*url.URL, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return nil, fmt.Errorf("%s base url is empty", name)
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%s base url %q: %w", name, raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%s base url %q must be an http or https origin", name, raw)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("%s base url %q must not carry a query, fragment, or credentials", name, raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

func newState(s Settings) (*state, error) {
	support, err := parseBase("support", s.SupportBase)
	if err != nil {
		return nil, err
	}
	index, err := parseBase("index", s.IndexBase)
	if err != nil {
		return nil, err
	}
	if s.InstallID != "" && !ValidInstallID(s.InstallID) {
		return nil, fmt.Errorf("install id %q is not 32 lowercase hex characters", s.InstallID)
	}
	return &state{
		support:     support,
		index:       index,
		indexOn:     s.IndexEnabled,
		installID:   s.InstallID,
		tenantToken: strings.TrimSpace(s.TenantToken),
	}, nil
}

func mustState(s Settings) *state {
	st, err := newState(s)
	if err != nil {
		panic(err)
	}
	return st
}

// Installs the process wide hub settings, validating every base
func Configure(s Settings) error {
	st, err := newState(s)
	if err != nil {
		return err
	}
	current.Store(st)
	return nil
}

// Support service origin, no trailing slash
func SupportBase() string {
	return current.Load().support.String()
}

// Upstream index origin, no trailing slash
func IndexBase() string {
	return current.Load().index.String()
}

// True when upstreams are routed through the index
func IndexEnabled() bool {
	return current.Load().indexOn
}

// This install's id, empty until Configure ran with one
func InstallID() string {
	return current.Load().installID
}

// True on a discohaus managed panel, one holding a tenant token
func Hosted() bool {
	return current.Load().tenantToken != ""
}

// Host of the support service
func SupportHost() string {
	return current.Load().support.Host
}

// Host of the upstream index
func IndexHost() string {
	return current.Load().index.Host
}

// True when the url points at the support service or the index
func (st *state) isHubHost(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(u.Host)
	return host == strings.ToLower(st.support.Host) || host == strings.ToLower(st.index.Host)
}

// True when the url points at the support service or the index
func IsHubURL(u *url.URL) bool {
	return current.Load().isHubHost(u)
}

// Adds the install id to every request bound for a hub host
type transport struct {
	base http.RoundTripper
}

// Wraps a transport so hub hosts receive the install id header
// A nil base uses http.DefaultTransport
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &transport{base: base}
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	st := current.Load()
	if !st.isHubHost(req.URL) {
		return t.base.RoundTrip(req)
	}
	if st.installID == "" {
		return nil, ErrNotConfigured
	}
	out := req.Clone(req.Context())
	out.Header.Set(InstallIDHeader, st.installID)
	return t.base.RoundTrip(out)
}

// An http client whose requests to hub hosts carry the install id
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: Transport(nil)}
}

// Client every hub rpc goes through
var rpcClient = NewHTTPClient(requestTimeout)
