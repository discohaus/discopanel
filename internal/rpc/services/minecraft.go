package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"connectrpc.com/connect"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/provisioner"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"github.com/discohaus/discopanel/pkg/protometa"
	"google.golang.org/protobuf/proto"
)

// Compile-time check that MinecraftService implements the interface
var _ discopanelv1connect.MinecraftServiceHandler = (*MinecraftService)(nil)

// Implements the Minecraft service
type MinecraftService struct {
	store  *storage.Store
	docker *docker.Client
	prov   *provisioner.Provisioner
	log    *logger.Logger
}

// Creates a new minecraft service
func NewMinecraftService(store *storage.Store, docker *docker.Client, prov *provisioner.Provisioner, log *logger.Logger) *MinecraftService {
	return &MinecraftService{
		store:  store,
		docker: docker,
		prov:   prov,
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

	latest, err := minecraft.GetLatestVersion()
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

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
		info.PinnableBuilds = provisioner.SupportsLoaderBuilds(info.Loader)
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

// MC versions travel into upstream URL paths
var versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)

// Lists installable builds of a loader for one MC version
func (s *MinecraftService) GetLoaderVersions(ctx context.Context, req *connect.Request[v1.GetLoaderVersionsRequest]) (*connect.Response[v1.GetLoaderVersionsResponse], error) {
	msg := req.Msg
	if !provisioner.SupportsLoaderBuilds(msg.ModLoader) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("mod loader %q has no selectable builds", protometa.Name(msg.ModLoader)))
	}
	if !versionPattern.MatchString(msg.McVersion) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid Minecraft version %q", msg.McVersion))
	}
	// Only real versions reach the upstreams and the cache
	if _, err := minecraft.GetVersionInfo(msg.McVersion); err != nil {
		if errors.Is(err, minecraft.ErrVersionUnknown) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown Minecraft version %q", msg.McVersion))
		}
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

	builds, err := s.prov.LoaderBuilds(ctx, msg.ModLoader, msg.McVersion)
	if err != nil {
		s.log.Warn("Failed to list %s builds for MC %s: %v", protometa.Name(msg.ModLoader), msg.McVersion, err)
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

	out := make([]*v1.LoaderBuild, 0, len(builds))
	for _, b := range builds {
		out = append(out, &v1.LoaderBuild{
			Version:     b.Version,
			Stable:      b.Stable,
			Recommended: b.Recommended,
		})
	}
	return connect.NewResponse(&v1.GetLoaderVersionsResponse{Builds: out}), nil
}
