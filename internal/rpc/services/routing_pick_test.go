package services

import (
	"testing"

	"github.com/discohaus/discopanel/internal/proxy"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Game route wins over udp wake relays and extra ports, ties are stable
func TestServerGameRoutePrefersNamedGameRoute(t *testing.T) {
	route := func(port int, protocol v1.ModuleProtocol, owner, hostname string) proxy.RouteEntry {
		return proxy.RouteEntry{Port: port, Route: &proxy.Route{
			OwnerKind: proxy.OwnerServer, OwnerID: owner, Hostname: hostname, Protocol: protocol,
		}}
	}
	udp, mc := v1.ModuleProtocol_MODULE_PROTOCOL_UDP, v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT
	entries := []proxy.RouteEntry{
		route(19132, udp, "srv", ""),
		route(25565, mc, "other", "a.example.com"),
		route(25565, mc, "srv", ""),
		route(25565, mc, "srv", "play.example.com"),
		route(25565, mc, "srv", "alt.example.com"),
		{Port: 1, Route: nil},
	}
	for range 3 {
		got := serverGameRoute(entries, "srv")
		if got == nil || got.Route.Hostname != "alt.example.com" {
			t.Fatalf("picked %+v, want alt.example.com", got)
		}
		// Map order varies, the pick must not
		entries[0], entries[len(entries)-2] = entries[len(entries)-2], entries[0]
	}

	// Only a wake relay left: it is still the server's route
	relayOnly := []proxy.RouteEntry{route(19132, udp, "srv", "")}
	if got := serverGameRoute(relayOnly, "srv"); got == nil || got.Port != 19132 {
		t.Fatalf("fallback pick %+v", got)
	}
	if serverGameRoute(entries, "missing") != nil {
		t.Fatal("unknown server must have no route")
	}
}

// Bedrock flag survives saves on udp ports and is dropped elsewhere
func TestNormalizeAdditionalPortsBedrockFlag(t *testing.T) {
	out, err := normalizeAdditionalPorts([]*v1.NetworkPort{
		{Name: "Bedrock", ContainerPort: 19132, HostPort: 19132, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_UDP, ProxyEnabled: true, Bedrock: true},
		{Name: "Query", ContainerPort: 25585, HostPort: 25585, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_TCP, Bedrock: true},
	}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !out[0].Bedrock || out[1].Bedrock {
		t.Fatalf("bedrock flags %v %v, want true false", out[0].Bedrock, out[1].Bedrock)
	}
}
