package proxy

import (
	"testing"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

const (
	tcp       = v1.ModuleProtocol_MODULE_PROTOCOL_TCP
	udp       = v1.ModuleProtocol_MODULE_PROTOCOL_UDP
	mc        = v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT
	httpProto = v1.ModuleProtocol_MODULE_PROTOCOL_HTTP

	overTCP = v1.NetworkTransport_NETWORK_TRANSPORT_TCP
	overUDP = v1.NetworkTransport_NETWORK_TRANSPORT_UDP
)

func exclusive(port int, transport v1.NetworkTransport) Reservation {
	return Reservation{Port: port, Transport: transport, Kind: kindExclusive}
}

func socket(port int) Reservation {
	return Reservation{Port: port, Transport: overTCP, Kind: kindSocket}
}

func routed(port int, protocol v1.ModuleProtocol, hostname string) Reservation {
	return Reservation{Port: port, Transport: overTCP, Kind: kindRouted, Protocol: protocol, Hostname: hostname}
}

func relayRes(port int, transport v1.NetworkTransport) Reservation {
	return Reservation{Port: port, Transport: transport, Kind: kindRelay}
}

func TestReservationsConflict(t *testing.T) {
	cases := []struct {
		name string
		a, b Reservation
		want bool
	}{
		{"different ports never conflict", exclusive(8080, overTCP), exclusive(8081, overTCP), false},
		{"tcp and udp share a port number", exclusive(8080, overTCP), exclusive(8080, overUDP), false},
		{"two tcp binds collide", exclusive(8080, overTCP), exclusive(8080, overTCP), true},
		{"two udp binds collide", exclusive(19132, overUDP), exclusive(19132, overUDP), true},
		{"bind beats socket", exclusive(25565, overTCP), socket(25565), true},
		{"bind beats route", exclusive(25565, overTCP), routed(25565, mc, "a.example.com"), true},
		{"bind beats relay", exclusive(25565, overTCP), relayRes(25565, overTCP), true},
		{"two sockets collide", socket(25565), socket(25565), true},
		{"route rides its socket", socket(25565), routed(25565, mc, "a.example.com"), false},
		{"any protocol routes on one socket", socket(25565), routed(25565, httpProto, "a.example.com"), false},
		{"same hostname same protocol collides", routed(25565, mc, "a.example.com"), routed(25565, mc, "a.example.com"), true},
		{"hostname compare ignores case", routed(25565, mc, "A.Example.Com"), routed(25565, mc, "a.example.com"), true},
		{"different hostnames coexist", routed(25565, mc, "a.example.com"), routed(25565, mc, "b.example.com"), false},
		{"same hostname across protocols coexists", routed(25565, httpProto, "a.example.com"), routed(25565, mc, "a.example.com"), false},
		{"one catch all per protocol per port", routed(8080, httpProto, ""), routed(8080, httpProto, ""), true},
		{"catch all namespaces split by protocol", routed(8080, httpProto, ""), routed(8080, mc, ""), false},
		{"catch all coexists with named routes", routed(8080, httpProto, ""), routed(8080, httpProto, "a.example.com"), false},
		{"one tcp relay per port", relayRes(25565, overTCP), relayRes(25565, overTCP), true},
		{"tcp and udp relays coexist", relayRes(25565, overTCP), relayRes(25565, overUDP), false},
		{"relay coexists with routes", relayRes(25565, overTCP), routed(25565, mc, "a.example.com"), false},
		{"relay rides its socket", socket(25565), relayRes(25565, overTCP), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reservationsConflict(&tc.a, &tc.b); got != tc.want {
				t.Fatalf("conflict = %v, want %v", got, tc.want)
			}
			// Conflict evaluation must be symmetric
			if got := reservationsConflict(&tc.b, &tc.a); got != tc.want {
				t.Fatalf("reverse conflict = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReservationFromRequestValidation(t *testing.T) {
	m := &Manager{}
	owner := NetOwner{Kind: OwnerServer, ID: "s1"}

	if _, err := m.reservationFromRequest(owner, NetRequest{Port: 0, Protocol: tcp}); err == nil {
		t.Fatal("port zero must be rejected")
	}
	if _, err := m.reservationFromRequest(owner, NetRequest{Port: 70000, Protocol: tcp}); err == nil {
		t.Fatal("port above range must be rejected")
	}
	if _, err := m.reservationFromRequest(owner, NetRequest{Port: 25565, Protocol: mc, Routed: true, Hostname: "bad host"}); err == nil {
		t.Fatal("hostname with space must be rejected")
	}
	if _, err := m.reservationFromRequest(owner, NetRequest{Port: 25565, Protocol: mc, Routed: true, Hostname: "http://x"}); err == nil {
		t.Fatal("hostname with scheme must be rejected")
	}

	res, err := m.reservationFromRequest(owner, NetRequest{Port: 25565, Protocol: mc, Routed: true, Hostname: " Play.Example.COM "})
	if err != nil {
		t.Fatalf("valid hostname rejected: %v", err)
	}
	if res.Hostname != "play.example.com" {
		t.Fatalf("hostname not normalized, got %q", res.Hostname)
	}
	if res.Transport != overTCP {
		t.Fatalf("minecraft must ride tcp, got %v", res.Transport)
	}

	res, err = m.reservationFromRequest(owner, NetRequest{Port: 19132, Protocol: udp})
	if err != nil {
		t.Fatalf("udp request rejected: %v", err)
	}
	if res.Transport != overUDP || res.Kind != kindExclusive {
		t.Fatalf("udp bind misclassified: %+v", res)
	}
}

func TestReservationProto(t *testing.T) {
	res := Reservation{
		Port:      25565,
		Transport: overTCP,
		Kind:      kindRouted,
		Protocol:  mc,
		Hostname:  "play.example.com",
		OwnerKind: OwnerServer,
		OwnerID:   "s1",
		Detail:    "proxy route",
	}
	pb := res.Proto()
	if pb.Kind != v1.NetworkReservationKind_NETWORK_RESERVATION_KIND_ROUTED {
		t.Fatalf("kind mismatch: %v", pb.Kind)
	}
	if pb.OwnerKind != v1.NetworkOwnerKind_NETWORK_OWNER_KIND_SERVER {
		t.Fatalf("owner mismatch: %v", pb.OwnerKind)
	}
	if pb.Port != 25565 || pb.Hostname != "play.example.com" || pb.OwnerId != "s1" {
		t.Fatalf("fields lost: %+v", pb)
	}

	if OwnerKindProto("junk") != v1.NetworkOwnerKind_NETWORK_OWNER_KIND_UNSPECIFIED {
		t.Fatal("unknown owner must map to unspecified")
	}
}

func TestPortNetRequests(t *testing.T) {
	ports := []*v1.NetworkPort{
		{Name: "web", ContainerPort: 8100, HostPort: 8100, Protocol: httpProto, ProxyEnabled: true},
		{Name: "voice", ContainerPort: 19132, HostPort: 19132, Protocol: udp, ProxyEnabled: true},
		{Name: "raw", ContainerPort: 9000, HostPort: 9000, Protocol: tcp},
		{Name: "unset", ContainerPort: 9001, HostPort: 0, Protocol: tcp},
		{Name: "handshake", ContainerPort: 25565, HostPort: 25599, Protocol: mc, ProxyEnabled: true},
		{Name: "named", ContainerPort: 8200, HostPort: 8200, Protocol: httpProto, ProxyEnabled: true, Hostnames: []string{"Map.Example.Com"}},
	}

	reqs := PortNetRequests(ports, nil, true)
	if len(reqs) != 4 {
		t.Fatalf("want 4 requests, got %d: %+v", len(reqs), reqs)
	}
	if !reqs[0].Routed || reqs[0].Hostname != "" {
		t.Fatalf("http port must route as catch all: %+v", reqs[0])
	}
	// Proxied udp becomes the port's udp relay
	if !reqs[1].Relay || reqs[1].Routed {
		t.Fatalf("udp port must relay: %+v", reqs[1])
	}
	// Unproxied tcp stays a plain bind
	if reqs[2].Relay || reqs[2].Routed {
		t.Fatalf("direct port must stay a bind: %+v", reqs[2])
	}
	// Port hostname override wins and normalizes
	if !reqs[3].Routed || reqs[3].Hostname != "map.example.com" {
		t.Fatalf("hostname override must apply: %+v", reqs[3])
	}

	reqs = PortNetRequests(ports, []string{"play.example.com"}, true)
	if len(reqs) != 5 {
		t.Fatalf("want 5 requests with fallback hostname, got %d", len(reqs))
	}

	// Disabled proxy downgrades every port to a bind
	for _, req := range PortNetRequests(ports, []string{"play.example.com"}, false) {
		if req.Routed || req.Relay {
			t.Fatalf("proxy off must not produce proxy requests: %+v", req)
		}
	}
}
