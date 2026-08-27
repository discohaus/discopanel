package seed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/grpcreflect"
	_ "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// Reflection over h2c exactly as every connect era panel serves it
func TestDiscoverConnectReadsLiveDescriptors(t *testing.T) {
	mux := http.NewServeMux()
	reflector := grpcreflect.NewStaticReflector(
		discopanelv1connect.ServerServiceName,
		discopanelv1connect.AuthServiceName,
	)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	defer srv.Close()

	surface, err := DiscoverConnect(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if surface.Era != "connect" {
		t.Fatalf("era %q", surface.Era)
	}
	var create *Operation
	for _, op := range surface.Ops {
		if op.Path == "/discopanel.v1.ServerService/CreateServer" {
			create = op
		}
	}
	if create == nil {
		t.Fatalf("CreateServer missing from %d procedures", len(surface.Ops))
	}
	if create.Input.Field("mc_version") == nil {
		t.Fatal("request shape lost mc_version")
	}
	loader := create.Input.Field("mod_loader")
	if loader == nil || loader.Shape.Kind != KindEnum || len(loader.Shape.Enum) == 0 {
		t.Fatalf("mod_loader shape wrong: %+v", loader)
	}
	for _, v := range loader.Shape.Enum {
		if v == "MOD_LOADER_UNSPECIFIED" {
			t.Fatal("enum kept its zero value")
		}
	}
	ports := create.Input.Field("additional_ports")
	if ports == nil || ports.Shape.Kind != KindList || ports.Shape.Elem.Kind != KindMessage {
		t.Fatalf("additional_ports shape wrong: %+v", ports)
	}
	if server := create.Output.Field("server"); server == nil || server.Shape.Field("id") == nil {
		t.Fatal("reply shape lost the server id")
	}
}
