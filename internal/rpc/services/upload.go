package services

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"github.com/discohaus/discopanel/pkg/transfer"
)

// Compile-time check that UploadService implements the interface
var _ discopanelv1connect.UploadServiceHandler = (*UploadService)(nil)

// UploadService implements the Upload service
type UploadService struct {
	manager *transfer.UploadManager
	cfg     *config.Config
	log     *logger.Logger
}

// NewUploadService creates a new upload service
func NewUploadService(manager *transfer.UploadManager, cfg *config.Config, log *logger.Logger) *UploadService {
	return &UploadService{
		manager: manager,
		cfg:     cfg,
		log:     log,
	}
}

// InitUpload creates a new upload session
func (s *UploadService) InitUpload(ctx context.Context, req *connect.Request[v1.InitUploadRequest]) (*connect.Response[v1.InitUploadResponse], error) {
	msg := req.Msg

	if msg.Filename == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("filename is required"))
	}

	if msg.TotalSize <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("total_size must be positive"))
	}

	chunkSize := msg.ChunkSize // Client override of chunk size
	if chunkSize > int32(s.cfg.Upload.MaxChunkSize) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("max chunk size exceeded by client override during init session"))
	}

	if chunkSize <= 0 {
		chunkSize = int32(s.cfg.Upload.DefaultChunkSize) // Set to server default
	}

	session, err := s.manager.InitSession(msg.Filename, msg.TotalSize, chunkSize)
	if err != nil {
		if errors.Is(err, transfer.ErrFileTooLarge) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("file exceeds maximum allowed size"))
		}
		s.log.Error("Failed to init upload session: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to initialize upload session"))
	}

	return connect.NewResponse(&v1.InitUploadResponse{
		SessionId:   session.ID,
		TotalChunks: session.TotalChunks,
	}), nil
}

// GetUploadStatus returns the status of an upload session
func (s *UploadService) GetUploadStatus(ctx context.Context, req *connect.Request[v1.GetUploadStatusRequest]) (*connect.Response[v1.GetUploadStatusResponse], error) {
	msg := req.Msg

	if msg.SessionId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id is required"))
	}

	bytesReceived, totalBytes, chunksReceived, totalChunks, completed, _, err := s.manager.GetSessionStatus(msg.SessionId)
	if err != nil {
		if errors.Is(err, transfer.ErrSessionNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("upload session not found"))
		}
		if errors.Is(err, transfer.ErrSessionExpired) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("upload session expired"))
		}
		s.log.Error("Failed to get upload status: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get upload status"))
	}

	missing, err := s.manager.GetMissingChunks(msg.SessionId)
	if err != nil {
		missing = nil // Ignore error for missing chunks
	}

	return connect.NewResponse(&v1.GetUploadStatusResponse{
		SessionId:      msg.SessionId,
		BytesReceived:  bytesReceived,
		TotalBytes:     totalBytes,
		ChunksReceived: chunksReceived,
		TotalChunks:    totalChunks,
		MissingChunks:  missing,
		Completed:      completed,
	}), nil
}

// UploadChunk uploads a single chunk
func (s *UploadService) UploadChunk(ctx context.Context, req *connect.Request[v1.UploadChunkRequest]) (*connect.Response[v1.UploadChunkResponse], error) {
	msg := req.Msg

	if msg.SessionId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id is required"))
	}

	if len(msg.Data) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("chunk data is required"))
	}

	completed, err := s.manager.WriteChunk(msg.SessionId, msg.ChunkIndex, msg.Data)
	if err != nil {
		if errors.Is(err, transfer.ErrSessionNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("upload session not found"))
		}
		if errors.Is(err, transfer.ErrSessionExpired) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("upload session expired"))
		}
		if errors.Is(err, transfer.ErrSessionCompleted) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("upload already completed"))
		}
		if errors.Is(err, transfer.ErrInvalidChunk) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid chunk index"))
		}
		s.log.Error("Failed to write chunk: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to write chunk"))
	}

	// Get updated session status
	bytesReceived, _, chunksReceived, _, _, _, _ := s.manager.GetSessionStatus(msg.SessionId)

	return connect.NewResponse(&v1.UploadChunkResponse{
		SessionId:      msg.SessionId,
		BytesReceived:  bytesReceived,
		ChunksReceived: chunksReceived,
		Completed:      completed,
	}), nil
}

// CancelUpload cancels an upload session and cleans up
func (s *UploadService) CancelUpload(ctx context.Context, req *connect.Request[v1.CancelUploadRequest]) (*connect.Response[v1.CancelUploadResponse], error) {
	msg := req.Msg

	if msg.SessionId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id is required"))
	}

	err := s.manager.Cancel(msg.SessionId)
	if err != nil {
		if errors.Is(err, transfer.ErrSessionNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("upload session not found"))
		}
		s.log.Error("Failed to cancel upload: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to cancel upload"))
	}

	return connect.NewResponse(&v1.CancelUploadResponse{}), nil
}

// GetManager returns the upload manager (for use by other services)
func (s *UploadService) GetManager() *transfer.UploadManager {
	return s.manager
}
