package proxy

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Recent misses kept for diagnostics
const edgeMissCap = 64

// Hostname diagnostics probes send, never counted
const ProbeHostname = "discopanel-probe.invalid"

// One arrival no route answered
type EdgeMiss struct {
	At       time.Time
	Lane     string
	Hostname string
	Remote   string
}

// Traffic shape counters every socket feeds
type EdgeObserver struct {
	httpTotal     atomic.Int64
	httpForwarded atomic.Int64
	httpHostIP    atomic.Int64
	mcTotal       atomic.Int64
	mcHostIP      atomic.Int64

	mu     sync.Mutex
	misses []EdgeMiss
	next   int
}

// Point in time copy for diagnostics
type EdgeSnapshot struct {
	HTTPTotal     int64
	HTTPForwarded int64
	HTTPHostIP    int64
	MCTotal       int64
	MCHostIP      int64
	Misses        []EdgeMiss
}

func NewEdgeObserver() *EdgeObserver {
	return &EdgeObserver{misses: make([]EdgeMiss, 0, edgeMissCap)}
}

// Records one http arrival and its routing outcome
func (o *EdgeObserver) noteHTTP(hostname string, forwarded, matched bool, remote string) {
	if o == nil || hostname == ProbeHostname {
		return
	}
	o.httpTotal.Add(1)
	if forwarded {
		o.httpForwarded.Add(1)
	}
	if net.ParseIP(hostname) != nil {
		o.httpHostIP.Add(1)
	}
	if !matched {
		o.miss("http", hostname, remote)
	}
}

// Records one minecraft handshake and its routing outcome
func (o *EdgeObserver) noteMC(hostname string, matched bool, remote string) {
	if o == nil || hostname == ProbeHostname {
		return
	}
	o.mcTotal.Add(1)
	if net.ParseIP(hostname) != nil {
		o.mcHostIP.Add(1)
	}
	if !matched {
		o.miss("minecraft", hostname, remote)
	}
}

// Appends a miss to the ring, oldest entry drops first
func (o *EdgeObserver) miss(lane, hostname, remote string) {
	entry := EdgeMiss{At: time.Now(), Lane: lane, Hostname: hostname, Remote: remote}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.misses) < edgeMissCap {
		o.misses = append(o.misses, entry)
		return
	}
	o.misses[o.next] = entry
	o.next = (o.next + 1) % edgeMissCap
}

// Copies counters and misses, newest miss first
func (o *EdgeObserver) Snapshot() EdgeSnapshot {
	if o == nil {
		return EdgeSnapshot{}
	}
	snap := EdgeSnapshot{
		HTTPTotal:     o.httpTotal.Load(),
		HTTPForwarded: o.httpForwarded.Load(),
		HTTPHostIP:    o.httpHostIP.Load(),
		MCTotal:       o.mcTotal.Load(),
		MCHostIP:      o.mcHostIP.Load(),
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(o.misses)
	snap.Misses = make([]EdgeMiss, 0, n)
	for i := 1; i <= n; i++ {
		idx := (o.next - i + n) % n
		if n < edgeMissCap {
			idx = n - i
		}
		snap.Misses = append(snap.Misses, o.misses[idx])
	}
	return snap
}

// Traffic shape snapshot for diagnostics
func (m *Manager) EdgeSnapshot() EdgeSnapshot {
	return m.edge.Snapshot()
}

// Ports with a tcp socket and whether each accepts
func (m *Manager) SocketStates() map[int]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[int]bool, len(m.tcpSockets))
	for port, sock := range m.tcpSockets {
		out[port] = sock.IsRunning()
	}
	return out
}
