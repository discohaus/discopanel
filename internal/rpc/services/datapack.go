package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/files"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Proto view of one datapack
func datapackProto(p minecraft.Datapack) *v1.Datapack {
	return &v1.Datapack{
		Name:        p.Name,
		IsDirectory: p.IsDir,
		Enabled:     p.Enabled,
		Size:        p.Size,
		ModifiedAt:  timestamppb.New(p.ModTime),
		Description: p.Description,
		PackFormat:  p.PackFormat,
	}
}

// Resolves the server's world datapacks dirs
func (s *ModService) datapackDirs(ctx context.Context, serverID string) (*v1.Server, string, string, error) {
	server, err := getServer(ctx, s.store, serverID)
	if err != nil {
		return nil, "", "", err
	}
	enabled, disabled, err := files.DatapacksDirs(server.DataPath)
	if err != nil {
		return nil, "", "", connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return server, enabled, disabled, nil
}

// Reports whether the world beside the datapacks dir has been generated.
// World detection keys on level.dat, so an early datapacks dir never passes
// for a world, and the game applies its packs when it creates the world
func worldGenerated(enabledDir string) bool {
	_, err := os.Stat(filepath.Join(filepath.Dir(enabledDir), "level.dat"))
	return err == nil
}

// Maps datapack helper errors onto rpc codes
func datapackError(err error, fallback string) error {
	switch {
	case errors.Is(err, minecraft.ErrDatapackName), errors.Is(err, minecraft.ErrDatapackNoMeta), errors.Is(err, minecraft.ErrDatapackNotZip):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, minecraft.ErrDatapackNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, minecraft.ErrDatapackExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	}
	return connect.NewError(connect.CodeInternal, errors.New(fallback))
}

// ListDatapacks lists zips and pack folders in the world
func (s *ModService) ListDatapacks(ctx context.Context, req *connect.Request[v1.ListDatapacksRequest]) (*connect.Response[v1.ListDatapacksResponse], error) {
	server, enabledDir, disabledDir, err := s.datapackDirs(ctx, req.Msg.ServerId)
	if err != nil {
		return nil, err
	}
	packs := minecraft.ListDatapacks(enabledDir, disabledDir)
	out := make([]*v1.Datapack, 0, len(packs))
	for _, p := range packs {
		out = append(out, datapackProto(p))
	}
	rel, _ := filepath.Rel(server.DataPath, enabledDir)
	return connect.NewResponse(&v1.ListDatapacksResponse{
		Datapacks:   out,
		Directory:   filepath.ToSlash(rel),
		WorldExists: worldGenerated(enabledDir),
	}), nil
}

// ImportUploadedDatapack places an uploaded zip in the world datapacks dir
func (s *ModService) ImportUploadedDatapack(ctx context.Context, req *connect.Request[v1.ImportUploadedDatapackRequest]) (*connect.Response[v1.ImportUploadedDatapackResponse], error) {
	msg := req.Msg
	if msg.UploadSessionId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("upload_session_id is required"))
	}
	server, enabledDir, disabledDir, err := s.datapackDirs(ctx, msg.ServerId)
	if err != nil {
		return nil, err
	}
	tempPath, name, err := s.uploadManager.GetTempPath(msg.UploadSessionId)
	if err != nil {
		s.log.Error("Failed to get upload session: %v", err)
		return nil, connect.NewError(connect.CodeNotFound, errors.New("upload session not found or not completed"))
	}
	// The session is spent whatever the outcome
	defer func() {
		s.uploadManager.CleanupSession(msg.UploadSessionId)
		os.Remove(tempPath)
	}()

	if err := minecraft.ValidDatapackName(name); err != nil || !strings.EqualFold(filepath.Ext(name), ".zip") {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("datapacks must be uploaded as a .zip file with a plain file name"))
	}
	prefix, rewrite, err := minecraft.DatapackZipRoot(tempPath)
	if err != nil {
		return nil, datapackError(err, "failed to read datapack")
	}
	dst, err := files.ResolveUnder(enabledDir, name)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if info, err := os.Lstat(dst); err == nil && (info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("a folder of that name already exists in the datapacks directory"))
	}
	_, replacedEnabled := os.Lstat(dst)
	_, replacedDisabled := os.Lstat(filepath.Join(disabledDir, name))
	replaced := replacedEnabled == nil || replacedDisabled == nil

	if err := os.MkdirAll(enabledDir, 0755); err != nil {
		s.log.Error("Failed to create datapacks directory: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create datapacks directory"))
	}
	// Staged under a dot name the listing skips, then swapped in whole
	staged := filepath.Join(enabledDir, "."+name+".uploading")
	defer os.Remove(staged)
	if rewrite {
		err = minecraft.RebaseDatapackZip(tempPath, staged, prefix)
	} else if err = os.Rename(tempPath, staged); err != nil {
		err = files.CopyFile(tempPath, staged)
	}
	if err != nil {
		s.log.Error("Failed to stage datapack %s: %v", name, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to save datapack"))
	}
	if err := os.Rename(staged, dst); err != nil {
		s.log.Error("Failed to place datapack %s: %v", name, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to save datapack"))
	}
	// Fresh zip replaces a disabled twin of the same name
	if err := os.Remove(filepath.Join(disabledDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log.Error("Failed to remove disabled copy of %s: %v", name, err)
	}
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_DATAPACK_INSTALL, metrics.Attrs{"file": name}, "installed datapack %s", name)

	pack, _, ok := minecraft.FindDatapack(enabledDir, disabledDir, name)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, errors.New("datapack saved but could not be read back"))
	}
	return connect.NewResponse(&v1.ImportUploadedDatapackResponse{
		Datapack:    datapackProto(pack),
		Replaced:    replaced,
		WorldExists: worldGenerated(enabledDir),
	}), nil
}

// ToggleDatapack moves a datapack in or out of the disabled dir
func (s *ModService) ToggleDatapack(ctx context.Context, req *connect.Request[v1.ToggleDatapackRequest]) (*connect.Response[v1.ToggleDatapackResponse], error) {
	msg := req.Msg
	server, enabledDir, disabledDir, err := s.datapackDirs(ctx, msg.ServerId)
	if err != nil {
		return nil, err
	}
	pack, _, ok := minecraft.FindDatapack(enabledDir, disabledDir, msg.Name)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, minecraft.ErrDatapackNotFound)
	}
	if pack.Enabled != msg.Enabled {
		if err := minecraft.SetDatapackEnabled(enabledDir, msg.Name, msg.Enabled); err != nil {
			s.log.Error("Failed to toggle datapack %s: %v", msg.Name, err)
			return nil, datapackError(err, "failed to toggle datapack")
		}
		kind, verb := v1.ServerActionKind_SERVER_ACTION_KIND_DATAPACK_DISABLE, "disabled"
		if msg.Enabled {
			kind, verb = v1.ServerActionKind_SERVER_ACTION_KIND_DATAPACK_ENABLE, "enabled"
		}
		s.rec.Record(ctx, server.Id, kind, metrics.Attrs{"file": msg.Name}, "%s datapack %s", verb, msg.Name)
		if pack, _, ok = minecraft.FindDatapack(enabledDir, disabledDir, msg.Name); !ok {
			return nil, connect.NewError(connect.CodeInternal, errors.New("datapack moved but could not be read back"))
		}
	}
	return connect.NewResponse(&v1.ToggleDatapackResponse{Datapack: datapackProto(pack)}), nil
}

// DeleteDatapack removes a datapack zip or folder
func (s *ModService) DeleteDatapack(ctx context.Context, req *connect.Request[v1.DeleteDatapackRequest]) (*connect.Response[v1.DeleteDatapackResponse], error) {
	msg := req.Msg
	server, enabledDir, disabledDir, err := s.datapackDirs(ctx, msg.ServerId)
	if err != nil {
		return nil, err
	}
	pack, dir, ok := minecraft.FindDatapack(enabledDir, disabledDir, msg.Name)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, minecraft.ErrDatapackNotFound)
	}
	path, err := files.ResolveUnder(dir, pack.Name)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if pack.IsDir {
		err = os.RemoveAll(path)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		s.log.Error("Failed to delete datapack %s: %v", pack.Name, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete datapack"))
	}
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_DATAPACK_DELETE, metrics.Attrs{"file": pack.Name}, "deleted datapack %s", pack.Name)
	return connect.NewResponse(&v1.DeleteDatapackResponse{}), nil
}
