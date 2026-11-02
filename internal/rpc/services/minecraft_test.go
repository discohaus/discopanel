package services

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func TestGetLoaderVersionsRejectsBadInput(t *testing.T) {
	s := NewMinecraftService(nil, nil, nil, logger.New())
	tests := []struct {
		name   string
		loader v1.ModLoader
		mc     string
	}{
		{"loader without builds", v1.ModLoader_MOD_LOADER_PAPER, "1.21.1"},
		{"unspecified loader", v1.ModLoader_MOD_LOADER_UNSPECIFIED, "1.21.1"},
		{"empty version", v1.ModLoader_MOD_LOADER_FABRIC, ""},
		{"path traversal", v1.ModLoader_MOD_LOADER_FABRIC, "../../admin"},
		{"query injection", v1.ModLoader_MOD_LOADER_QUILT, "1.21.1?x=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.GetLoaderVersions(t.Context(), connect.NewRequest(&v1.GetLoaderVersionsRequest{ModLoader: tt.loader, McVersion: tt.mc}))
			var cerr *connect.Error
			if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInvalidArgument {
				t.Fatalf("err = %v, want invalid argument", err)
			}
		})
	}
}
