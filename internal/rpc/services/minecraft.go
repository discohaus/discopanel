package services

import (
	"context"

	"connectrpc.com/connect"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/provisioner"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"google.golang.org/protobuf/proto"
)

// Compile-time check that MinecraftService implements the interface
var _ discopanelv1connect.MinecraftServiceHandler = (*MinecraftService)(nil)

// Implements the Minecraft service
type MinecraftService struct {
	store  *storage.Store
	docker *docker.Client
	log    *logger.Logger
}

// Creates a new minecraft service
func NewMinecraftService(store *storage.Store, docker *docker.Client, log *logger.Logger) *MinecraftService {
	return &MinecraftService{
		store:  store,
		docker: docker,
		log:    log,
	}
}

// Gets available Minecraft versions
func (s *MinecraftService) GetMinecraftVersions(ctx context.Context, req *connect.Request[v1.GetMinecraftVersionsRequest]) (*connect.Response[v1.GetMinecraftVersionsResponse], error) {
	// Get all versions (includes all types)
	allVersionIDs := minecraft.GetAllVersions()

	// Convert to proto format by fetching info for each version
	versions := make([]*v1.MinecraftVersion, 0, len(allVersionIDs))
	for _, versionID := range allVersionIDs {
		versionInfo, err := minecraft.GetVersionInfo(versionID)
		if err != nil {
			continue // Skip versions we can't get info for
		}

		versions = append(versions, &v1.MinecraftVersion{
			Id:          versionInfo.ID,
			Type:        versionInfo.Type,
			ReleaseTime: versionInfo.ReleaseTime.Format("2006-01-02T15:04:05Z"),
			Url:         versionInfo.URL,
		})
	}

	// Get latest version
	latest := minecraft.GetLatestVersion()

	return connect.NewResponse(&v1.GetMinecraftVersionsResponse{
		Versions: versions,
		Latest:   latest,
	}), nil
}

// Gets available mod loaders
func (s *MinecraftService) GetModLoaders(ctx context.Context, req *connect.Request[v1.GetModLoadersRequest]) (*connect.Response[v1.GetModLoadersResponse], error) {
	rows := minecraft.Loaders()
	protoLoaders := make([]*v1.ModLoaderInfo, 0, len(rows))
	for _, row := range rows {
		info, _ := proto.Clone(row.Info).(*v1.ModLoaderInfo)
		info.Provisionable = provisioner.HasNativeInstaller(info.Loader)
		protoLoaders = append(protoLoaders, info)
	}

	return connect.NewResponse(&v1.GetModLoadersResponse{
		Modloaders: protoLoaders,
	}), nil
}

// Lists the published discoruntime image variants
func (s *MinecraftService) GetDockerImages(ctx context.Context, req *connect.Request[v1.GetDockerImagesRequest]) (*connect.Response[v1.GetDockerImagesResponse], error) {
	return connect.NewResponse(&v1.GetDockerImagesResponse{
		Images: docker.RuntimeImages(),
	}), nil
}
