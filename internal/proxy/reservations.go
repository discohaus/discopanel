package proxy

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	db "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
)

/* THE RULE OF PROXY / PORTS AND THEIR RESERVATIONS

Example w/ one port (one listener):

On its TCP socket:
unlimited minecraft routes (hostname namespace per protocol),
unlimited HTTP routes (own namespace, same hostname as an MC route is legal because the protocol sniff disambiguates before any hostname table is consulted),
plus at most one hostname-less TCP relay as the fallback when neither parser matches.

On its UDP socket:
at most one relay.


Dispatch is peek-and-descend: MC handshake? → MC table → HTTP Host? → HTTP table → TCP relay → descriptive error; UDP → relay → drop.
*/

// Owner kinds for network reservations
const (
	OwnerServer   = "server"
	OwnerModule   = "module"
	OwnerListener = "listener"
	OwnerPanel    = "panel"
)

// How a reservation occupies its port
type resKind int

const (
	// Direct host bind owns the port outright
	kindExclusive resKind = iota
	// Protocol neutral listener socket
	kindSocket
	// Hostname route inside a protocol lane
	kindRouted
	// Hostnameless fallback forward, one per transport
	kindRelay
)

// One checked out slice of the host network space
type Reservation struct {
	Port      int
	Transport v1.NetworkTransport
	Kind      resKind
	Protocol  v1.ModuleProtocol
	Hostname  string
	OwnerKind string
	OwnerID   string
	Detail    string
}

// Who is asking for or holding reservations
type NetOwner struct {
	Kind string
	ID   string
}

// One endpoint a caller wants to check out
type NetRequest struct {
	Port     int
	Protocol v1.ModuleProtocol
	Routed   bool
	Socket   bool
	Relay    bool
	Hostname string
	Detail   string
}

// Conflict raised when a checkout loses to a holder
type NetConflictError struct {
	Port   int
	Reason string
}

func (e *NetConflictError) Error() string {
	return e.Reason
}

// Pending checkout not yet persisted by its caller
type pendingClaim struct {
	owner   NetOwner
	held    []Reservation
	created time.Time
}

// Claims self expire when a caller leaks one
const claimTTL = 60 * time.Second

// Handle for a granted checkout, settle after persisting
type NetClaim struct {
	m  *Manager
	id uint64
}

// Marks the checkout persisted, derivation takes over
func (c *NetClaim) Confirm() {
	if c == nil || c.m == nil {
		return
	}
	logger := c.m.logger
	if !c.settle() {
		// Persist landed after the sweep, rows may now clash
		logger.Warn("Network claim confirmed after expiry, run a sync to surface conflicts")
	}
}

// Drops the checkout after a failed persist
func (c *NetClaim) Release() {
	c.settle()
}

func (c *NetClaim) settle() bool {
	if c == nil || c.m == nil {
		return true
	}
	c.m.mu.Lock()
	_, present := c.m.pendingClaims[c.id]
	delete(c.m.pendingClaims, c.id)
	c.m.mu.Unlock()
	c.m = nil
	return present
}

var hostnamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// Lowercases and trims a hostname for storage and matching
func NormalizeHostname(hostname string) string {
	return strings.ToLower(strings.TrimSpace(hostname))
}

// Reports whether a hostname passes the routing pattern
func ValidHostname(hostname string) bool {
	return hostnamePattern.MatchString(hostname)
}

// Turns one request into a validated reservation
func (m *Manager) reservationFromRequest(owner NetOwner, req NetRequest) (Reservation, error) {
	if req.Port < 1 || req.Port > 65535 {
		return Reservation{}, &NetConflictError{Port: req.Port, Reason: fmt.Sprintf("port %d is out of range", req.Port)}
	}
	res := Reservation{
		Port:      req.Port,
		Transport: db.TransportOf(req.Protocol),
		Kind:      kindExclusive,
		Protocol:  req.Protocol,
		OwnerKind: owner.Kind,
		OwnerID:   owner.ID,
		Detail:    req.Detail,
	}
	if req.Socket {
		res.Kind = kindSocket
		return res, nil
	}
	if req.Relay {
		res.Kind = kindRelay
		return res, nil
	}
	if req.Routed {
		res.Kind = kindRouted
		hostname := NormalizeHostname(req.Hostname)
		if hostname != "" && !hostnamePattern.MatchString(hostname) {
			return Reservation{}, &NetConflictError{Port: req.Port, Reason: fmt.Sprintf("invalid hostname %q", req.Hostname)}
		}
		res.Hostname = hostname
	}
	return res, nil
}

// Reports whether two reservations fight over the same slice
func reservationsConflict(a, b *Reservation) bool {
	if a.Port != b.Port || a.Transport != b.Transport {
		return false
	}
	if a.Kind == kindExclusive || b.Kind == kindExclusive {
		return true
	}
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case kindSocket, kindRelay:
		return true
	case kindRouted:
		// Hostnames only collide inside one protocol lane
		return a.Protocol == b.Protocol && strings.EqualFold(a.Hostname, b.Hostname)
	}
	return false
}

// Explains why a holder blocks the requested slice
func conflictReason(want, holder *Reservation) string {
	who := holder.OwnerKind
	if holder.Detail != "" {
		who = fmt.Sprintf("%s %s", holder.OwnerKind, holder.Detail)
	}
	transport := protometa.Name(want.Transport)
	if want.Kind == kindRouted && holder.Kind == kindRouted {
		if want.Hostname == "" {
			return fmt.Sprintf("port %d already has a catch all %s route (%s)", want.Port, protometa.Name(want.Protocol), who)
		}
		return fmt.Sprintf("hostname %s is already routed for %s on port %d (%s)", want.Hostname, protometa.Name(want.Protocol), want.Port, who)
	}
	if want.Kind == kindRelay && holder.Kind == kindRelay {
		return fmt.Sprintf("port %d already has a %s relay (%s)", want.Port, transport, who)
	}
	return fmt.Sprintf("port %d/%s is already in use by %s", want.Port, transport, who)
}

// Builds reservation requests for a module's ports
func (m *Manager) ModuleNetRequests(module *v1.Module, serverHostname string) []NetRequest {
	m.mu.Lock()
	enabled := m.enabled
	m.mu.Unlock()
	return PortNetRequests(module.Ports, serverHostname, enabled)
}

// One request per port, shape decided by protocol and flags
func PortNetRequests(ports []*v1.NetworkPort, fallbackHostname string, proxyOn bool) []NetRequest {
	var reqs []NetRequest
	for _, port := range ports {
		if port == nil || port.HostPort <= 0 {
			continue
		}
		req := NetRequest{
			Port:     int(port.HostPort),
			Protocol: port.Protocol,
			Detail:   fmt.Sprintf("port %s", port.Name),
		}
		if port.ProxyEnabled && proxyOn {
			switch port.Protocol {
			case v1.ModuleProtocol_MODULE_PROTOCOL_HTTP, v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT:
				hostname := NormalizeHostname(port.Hostname)
				if hostname == "" {
					hostname = fallbackHostname
				}
				// Handshake routing cannot match without a hostname
				if hostname == "" && port.Protocol == v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT {
					continue
				}
				req.Routed = true
				req.Hostname = hostname
			default:
				req.Relay = true
			}
		}
		reqs = append(reqs, req)
	}
	return reqs
}

// Builds reservation requests for a direct mode server
func ServerDirectNetRequests(port int, additional []*v1.NetworkPort, proxyOn bool) []NetRequest {
	reqs := []NetRequest{
		{Port: port, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_TCP, Detail: "game port"},
		{Port: port + docker.RCONPortOffset, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_TCP, Detail: "rcon port"},
	}
	return append(reqs, PortNetRequests(additional, "", proxyOn)...)
}

// Builds reservation requests for a proxied server
func ServerProxiedNetRequests(hostname string, listenerPort int, additional []*v1.NetworkPort, proxyOn bool) []NetRequest {
	reqs := []NetRequest{
		{
			Port:     listenerPort,
			Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT,
			Routed:   true,
			Hostname: hostname,
			Detail:   "proxy route",
		},
	}
	return append(reqs, PortNetRequests(additional, hostname, proxyOn)...)
}

// Derives every live reservation from the database and config
func (m *Manager) reservationsLocked(ctx context.Context) ([]Reservation, error) {
	var all []Reservation

	// Panel web port stays off limits
	if m.appCfg != nil {
		if p, err := strconv.Atoi(m.appCfg.Server.Port); err == nil && p > 0 {
			all = append(all, Reservation{
				Port:      p,
				Transport: v1.NetworkTransport_NETWORK_TRANSPORT_TCP,
				Kind:      kindExclusive,
				OwnerKind: OwnerPanel,
				OwnerID:   OwnerPanel,
				Detail:    "web interface",
			})
		}
	}

	// Listener rows hold their ports even while disabled
	listeners, err := m.store.ListProxyListeners(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list proxy listeners: %w", err)
	}
	listenersByID := make(map[string]*v1.ProxyListener, len(listeners))
	for _, l := range listeners {
		listenersByID[l.Id] = l
		all = append(all, Reservation{
			Port:      int(l.Port),
			Transport: v1.NetworkTransport_NETWORK_TRANSPORT_TCP,
			Kind:      kindSocket,
			OwnerKind: OwnerListener,
			OwnerID:   l.Id,
			Detail:    l.Name,
		})
	}

	servers, err := m.store.ListServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list servers: %w", err)
	}
	serversByID := make(map[string]*v1.Server, len(servers))
	for _, s := range servers {
		serversByID[s.Id] = s
		owner := NetOwner{Kind: OwnerServer, ID: s.Id}
		var reqs []NetRequest
		if s.ProxyHostname != "" {
			listener := listenersByID[s.ProxyListenerId]
			if listener == nil {
				reqs = PortNetRequests(s.AdditionalPorts, s.ProxyHostname, m.enabled)
			} else {
				reqs = ServerProxiedNetRequests(s.ProxyHostname, int(listener.Port), s.AdditionalPorts, m.enabled)
			}
		} else if s.Port > 0 {
			reqs = ServerDirectNetRequests(int(s.Port), s.AdditionalPorts, m.enabled)
		} else {
			reqs = PortNetRequests(s.AdditionalPorts, "", m.enabled)
		}
		for _, req := range reqs {
			if res, err := m.reservationFromRequest(owner, req); err == nil {
				all = append(all, res)
			}
		}
	}

	modules, err := m.store.ListModules(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list modules: %w", err)
	}
	for _, mod := range modules {
		owner := NetOwner{Kind: OwnerModule, ID: mod.Id}
		hostname := ""
		if srv := serversByID[mod.ServerId]; srv != nil {
			hostname = srv.ProxyHostname
		}
		for _, req := range PortNetRequests(mod.Ports, hostname, m.enabled) {
			if res, err := m.reservationFromRequest(owner, req); err == nil {
				all = append(all, res)
			}
		}
	}

	return all, nil
}

// Snapshot of persisted reservations for the topology surface
func (m *Manager) Reservations(ctx context.Context) ([]Reservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reservationsLocked(ctx)
}

// Maps an owner kind string onto its wire enum
func OwnerKindProto(kind string) v1.NetworkOwnerKind {
	switch kind {
	case OwnerServer:
		return v1.NetworkOwnerKind_NETWORK_OWNER_KIND_SERVER
	case OwnerModule:
		return v1.NetworkOwnerKind_NETWORK_OWNER_KIND_MODULE
	case OwnerListener:
		return v1.NetworkOwnerKind_NETWORK_OWNER_KIND_LISTENER
	case OwnerPanel:
		return v1.NetworkOwnerKind_NETWORK_OWNER_KIND_PANEL
	}
	return v1.NetworkOwnerKind_NETWORK_OWNER_KIND_UNSPECIFIED
}

// Maps a reservation onto its wire message
func (r Reservation) Proto() *v1.NetworkReservation {
	kind := v1.NetworkReservationKind_NETWORK_RESERVATION_KIND_UNSPECIFIED
	switch r.Kind {
	case kindExclusive:
		kind = v1.NetworkReservationKind_NETWORK_RESERVATION_KIND_EXCLUSIVE
	case kindSocket:
		kind = v1.NetworkReservationKind_NETWORK_RESERVATION_KIND_SOCKET
	case kindRouted:
		kind = v1.NetworkReservationKind_NETWORK_RESERVATION_KIND_ROUTED
	case kindRelay:
		kind = v1.NetworkReservationKind_NETWORK_RESERVATION_KIND_RELAY
	}
	return &v1.NetworkReservation{
		Port:      int32(r.Port),
		Transport: r.Transport,
		Kind:      kind,
		Protocol:  r.Protocol,
		Hostname:  r.Hostname,
		OwnerKind: OwnerKindProto(r.OwnerKind),
		OwnerId:   r.OwnerID,
		Detail:    r.Detail,
	}
}

// Sweeps leaked claims, caller must hold the lock
func (m *Manager) sweepClaimsLocked() {
	now := time.Now()
	for id, claim := range m.pendingClaims {
		if now.Sub(claim.created) > claimTTL {
			m.logger.Warn("Network claim by %s %s expired unsettled, conflicts possible", claim.owner.Kind, claim.owner.ID)
			delete(m.pendingClaims, id)
		}
	}
}

// Live reservations plus unexpired pending claims
func (m *Manager) reservationsWithPendingLocked(ctx context.Context, exclude NetOwner) ([]Reservation, error) {
	m.sweepClaimsLocked()
	all, err := m.reservationsLocked(ctx)
	if err != nil {
		return nil, err
	}
	held := all[:0]
	for _, r := range all {
		if r.OwnerKind == exclude.Kind && r.OwnerID == exclude.ID {
			continue
		}
		held = append(held, r)
	}
	for _, claim := range m.pendingClaims {
		if claim.owner == exclude {
			continue
		}
		held = append(held, claim.held...)
	}
	return held, nil
}

// Checks out network slices, settle the claim after persisting
func (m *Manager) CheckoutNetwork(ctx context.Context, owner NetOwner, reqs []NetRequest) (*NetClaim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	wanted := make([]Reservation, 0, len(reqs))
	for _, req := range reqs {
		res, err := m.reservationFromRequest(owner, req)
		if err != nil {
			return nil, err
		}
		wanted = append(wanted, res)
	}

	// Requests in one checkout must not fight each other
	for i := range wanted {
		for j := i + 1; j < len(wanted); j++ {
			if reservationsConflict(&wanted[i], &wanted[j]) {
				return nil, &NetConflictError{
					Port:   wanted[j].Port,
					Reason: conflictReason(&wanted[j], &wanted[i]),
				}
			}
		}
	}

	held, err := m.reservationsWithPendingLocked(ctx, owner)
	if err != nil {
		return nil, err
	}
	for i := range wanted {
		for j := range held {
			if reservationsConflict(&wanted[i], &held[j]) {
				return nil, &NetConflictError{
					Port:   wanted[i].Port,
					Reason: conflictReason(&wanted[i], &held[j]),
				}
			}
		}
	}

	m.claimSeq++
	id := m.claimSeq
	m.pendingClaims[id] = pendingClaim{owner: owner, held: wanted, created: time.Now()}
	return &NetClaim{m: m, id: id}, nil
}

// Distinct ports a request set wants a listener on
func ListenerPortsNeeded(reqs []NetRequest) []int {
	seen := make(map[int]bool)
	var ports []int
	for _, req := range reqs {
		if (req.Routed || req.Relay) && !seen[req.Port] {
			seen[req.Port] = true
			ports = append(ports, req.Port)
		}
	}
	sort.Ints(ports)
	return ports
}

// Creates missing listener rows for routed and relay ports
func (m *Manager) EnsureListenersFor(ctx context.Context, reqs []NetRequest) error {
	ports := ListenerPortsNeeded(reqs)
	if len(ports) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	listeners, err := m.store.ListProxyListeners(ctx)
	if err != nil {
		return err
	}
	have := make(map[int]bool, len(listeners))
	for _, l := range listeners {
		have[int(l.Port)] = true
	}
	for _, port := range ports {
		if have[port] {
			continue
		}
		if _, err := m.createListenerRowLocked(ctx, port); err != nil {
			return err
		}
	}
	return nil
}

// Persists one auto listener row after a socket claim check
func (m *Manager) createListenerRowLocked(ctx context.Context, port int) (*v1.ProxyListener, error) {
	listener := &v1.ProxyListener{
		Id:          uuid.New().String(),
		Port:        int32(port),
		Name:        fmt.Sprintf("Port %d", port),
		Enabled:     true,
		AutoCreated: true,
	}
	want := Reservation{
		Port:      port,
		Transport: v1.NetworkTransport_NETWORK_TRANSPORT_TCP,
		Kind:      kindSocket,
		OwnerKind: OwnerListener,
		OwnerID:   listener.Id,
		Detail:    listener.Name,
	}
	held, err := m.reservationsWithPendingLocked(ctx, NetOwner{})
	if err != nil {
		return nil, err
	}
	for i := range held {
		if reservationsConflict(&want, &held[i]) {
			return nil, &NetConflictError{Port: port, Reason: conflictReason(&want, &held[i])}
		}
	}
	if err := m.store.CreateProxyListener(ctx, listener); err != nil {
		return nil, fmt.Errorf("failed to create listener for port %d: %w", port, err)
	}
	m.logger.Info("Auto created listener for port %d", port)
	return listener, nil
}

// Validates requests without holding a claim
func (m *Manager) ValidateNetwork(ctx context.Context, owner NetOwner, reqs []NetRequest) error {
	claim, err := m.CheckoutNetwork(ctx, owner, reqs)
	if claim != nil {
		claim.Release()
	}
	return err
}

// Options for scanning out a free port
type FreePortOpts struct {
	Protocol   v1.ModuleProtocol
	Start      int
	End        int
	RconShadow bool
	Exclude    map[int]bool
}

// Finds the first port free across the whole registry
func (m *Manager) FindFreePort(ctx context.Context, opts FreePortOpts) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.findFreePortLocked(ctx, opts)
}

// Free port scan, caller must hold the lock
func (m *Manager) findFreePortLocked(ctx context.Context, opts FreePortOpts) (int, error) {
	held, err := m.reservationsWithPendingLocked(ctx, NetOwner{})
	if err != nil {
		return 0, err
	}

	busy := make(map[int]map[v1.NetworkTransport]bool, len(held))
	for _, r := range held {
		if busy[r.Port] == nil {
			busy[r.Port] = make(map[v1.NetworkTransport]bool, 2)
		}
		busy[r.Port][r.Transport] = true
	}

	transport := db.TransportOf(opts.Protocol)
	tcp := v1.NetworkTransport_NETWORK_TRANSPORT_TCP
	if opts.Start < 1 {
		opts.Start = 1
	}
	if opts.End > 65535 || opts.End < 1 {
		opts.End = 65535
	}
	for port := opts.Start; port <= opts.End; port++ {
		if opts.Exclude[port] || busy[port][transport] {
			continue
		}
		if opts.RconShadow && (busy[port+docker.RCONPortOffset][tcp] || opts.Exclude[port+docker.RCONPortOffset]) {
			continue
		}
		return port, nil
	}
	return 0, fmt.Errorf("no free port between %d and %d", opts.Start, opts.End)
}

// Every port with any reservation, for UI hints
func (m *Manager) UsedNetworkPorts(ctx context.Context) ([]int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	held, err := m.reservationsWithPendingLocked(ctx, NetOwner{})
	if err != nil {
		return nil, err
	}
	seen := make(map[int]bool, len(held))
	for _, r := range held {
		seen[r.Port] = true
	}
	ports := make([]int32, 0, len(seen))
	for p := range seen {
		ports = append(ports, int32(p))
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return ports, nil
}
