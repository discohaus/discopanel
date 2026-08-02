package services

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Compile-time check that SupportService implements the interface
var _ discopanelv1connect.SupportServiceHandler = (*SupportService)(nil)

// Largest slice read from the tail of the app log
const maxAppLogTailBytes = 1 << 20

// Implements the Support service
type SupportService struct {
	store  *storage.Store
	docker *docker.Client
	config *config.Config
	log    *logger.Logger
	// Guards the temporary bundle registry
	bundlesMu sync.Mutex
	bundles   map[string]*v1.GenerateSupportBundleResponse
}

// Creates a new support service
func NewSupportService(store *storage.Store, docker *docker.Client, config *config.Config, log *logger.Logger) *SupportService {
	return &SupportService{
		store:   store,
		docker:  docker,
		config:  config,
		log:     log,
		bundles: make(map[string]*v1.GenerateSupportBundleResponse),
	}
}

// Bundle archives live in the temp dir under their filename
func (s *SupportService) bundlePath(filename string) string {
	return filepath.Join(s.config.Storage.TempDir, filename)
}

// Assembles a support bundle archive on disk
func (s *SupportService) buildBundle(ctx context.Context, includeLogs, includeConfigs, includeSystemInfo bool, serverIDs []string) (*v1.GenerateSupportBundleResponse, error) {
	// Scratch space for the scrubbed database copy
	tempDir, err := os.MkdirTemp(s.config.Storage.TempDir, "support-bundle-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	bundleFileName := fmt.Sprintf("discopanel-support-%s.tar.gz", time.Now().Format("20060102-150405"))
	bundlePath := filepath.Join(s.config.Storage.TempDir, bundleFileName)

	bundleFile, err := os.Create(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create bundle file: %w", err)
	}
	defer bundleFile.Close()

	gzipWriter := gzip.NewWriter(bundleFile)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	if includeLogs {
		if err := s.addLogsToBundle(ctx, tarWriter, serverIDs); err != nil {
			s.log.Warn("Continuing without logs: %v", err)
		}
	}

	if includeConfigs {
		if err := s.addDatabaseToBundle(ctx, tarWriter, tempDir); err != nil {
			s.log.Warn("Continuing without database: %v", err)
		}
		if err := s.addServerPropertiesToBundle(ctx, tarWriter, serverIDs); err != nil {
			s.log.Warn("Continuing without server configs: %v", err)
		}
	}

	if includeSystemInfo {
		if err := s.addSystemInfoToBundle(ctx, tarWriter); err != nil {
			s.log.Warn("Continuing without system info: %v", err)
		}
	}

	// Close writers to flush all data
	tarWriter.Close()
	gzipWriter.Close()
	bundleFile.Close()

	fileInfo, err := os.Stat(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat bundle file: %w", err)
	}

	return &v1.GenerateSupportBundleResponse{
		BundleId:  uuid.New().String(),
		Filename:  bundleFileName,
		Size:      fileInfo.Size(),
		CreatedAt: timestamppb.Now(),
	}, nil
}

// Generates a support bundle
func (s *SupportService) GenerateSupportBundle(ctx context.Context, req *connect.Request[v1.GenerateSupportBundleRequest]) (*connect.Response[v1.GenerateSupportBundleResponse], error) {
	msg := req.Msg
	s.log.Info("Generating support bundle (logs=%v, configs=%v, system=%v)", msg.IncludeLogs, msg.IncludeConfigs, msg.IncludeSystemInfo)

	bundle, err := s.buildBundle(ctx, msg.IncludeLogs, msg.IncludeConfigs, msg.IncludeSystemInfo, msg.ServerIds)
	if err != nil {
		s.log.Error("Failed to build support bundle: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create support bundle"))
	}
	bundle.Message = "Support bundle created successfully"

	// Store bundle info for download
	s.bundlesMu.Lock()
	s.bundles[bundle.BundleId] = bundle
	s.bundlesMu.Unlock()

	// Clean up old bundles after 1 hour
	go func() {
		time.Sleep(1 * time.Hour)
		s.cleanupBundle(bundle.BundleId)
	}()

	return connect.NewResponse(bundle), nil
}

// Downloads a support bundle
func (s *SupportService) DownloadSupportBundle(ctx context.Context, req *connect.Request[v1.DownloadSupportBundleRequest]) (*connect.Response[v1.DownloadSupportBundleResponse], error) {
	s.bundlesMu.Lock()
	bundle, exists := s.bundles[req.Msg.BundleId]
	s.bundlesMu.Unlock()
	if !exists {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("bundle not found or expired"))
	}

	// Read the bundle file
	bundleData, err := os.ReadFile(s.bundlePath(bundle.Filename))
	if err != nil {
		s.log.Error("Failed to read bundle file: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read bundle"))
	}

	// Clean up the bundle after download
	go s.cleanupBundle(req.Msg.BundleId)

	return connect.NewResponse(&v1.DownloadSupportBundleResponse{
		Content:  bundleData,
		Filename: bundle.Filename,
		MimeType: "application/gzip",
	}), nil
}

// Generates and uploads a support bundle to server
func (s *SupportService) UploadSupportBundle(ctx context.Context, req *connect.Request[v1.UploadSupportBundleRequest]) (*connect.Response[v1.UploadSupportBundleResponse], error) {
	msg := req.Msg
	s.log.Info("Generating support bundle for upload (logs=%v, configs=%v, system=%v)", msg.IncludeLogs, msg.IncludeConfigs, msg.IncludeSystemInfo)

	bundle, err := s.buildBundle(ctx, msg.IncludeLogs, msg.IncludeConfigs, msg.IncludeSystemInfo, msg.ServerIds)
	if err != nil {
		s.log.Error("Failed to build support bundle: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create support bundle"))
	}
	defer os.Remove(s.bundlePath(bundle.Filename))

	// Upload the bundle to support server
	referenceID, err := s.uploadBundleToServer(s.bundlePath(bundle.Filename), bundle.Filename, msg)
	if err != nil {
		s.log.Error("Failed to upload support bundle: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to upload support bundle: %v", err))
	}

	return connect.NewResponse(&v1.UploadSupportBundleResponse{
		ReferenceId: referenceID,
		Message:     "Support bundle uploaded successfully",
		Success:     true,
	}), nil
}

// Uploads a bundle file to the support server
func (s *SupportService) uploadBundleToServer(bundlePath, fileName string, userInfo *v1.UploadSupportBundleRequest) (string, error) {
	supportURL := s.getUploadSupportUrl()

	// Open the bundle file
	file, err := os.Open(bundlePath)
	if err != nil {
		return "", fmt.Errorf("failed to open bundle file: %w", err)
	}
	defer file.Close()

	// Get file info
	fileInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to stat bundle file: %w", err)
	}

	// Create multipart form
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add file field
	part, err := writer.CreateFormFile("bundle", fileName)
	if err != nil {
		return "", fmt.Errorf("failed to create form file: %w", err)
	}

	// Copy file content
	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("failed to copy file content: %w", err)
	}

	// Add metadata fields
	writer.WriteField("timestamp", time.Now().Format(time.RFC3339))
	writer.WriteField("size", fmt.Sprintf("%d", fileInfo.Size()))

	// Add user-provided contact and issue information
	if userInfo != nil {
		if userInfo.DiscordUsername != "" {
			writer.WriteField("discord_username", userInfo.DiscordUsername)
		}
		if userInfo.Email != "" {
			writer.WriteField("email", userInfo.Email)
		}
		if userInfo.GithubUsername != "" {
			writer.WriteField("github_username", userInfo.GithubUsername)
		}
		if userInfo.IssueDescription != "" {
			writer.WriteField("issue_description", userInfo.IssueDescription)
		}
		if userInfo.StepsToReproduce != "" {
			writer.WriteField("steps_to_reproduce", userInfo.StepsToReproduce)
		}
	}

	// Close multipart writer
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// Create request
	req, err := http.NewRequest(http.MethodPost, supportURL, body)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Send request
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to upload bundle: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upload failed with status: %d", resp.StatusCode)
	}

	var uploadResp struct {
		URL         string `json:"url"`
		Message     string `json:"message"`
		ReferenceID string `json:"reference_id"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&uploadResp); err != nil {
		// Falls back to support URL if response can't decode
		return supportURL, nil
	}

	// Return the reference ID if provided
	if uploadResp.ReferenceID != "" {
		return uploadResp.ReferenceID, nil
	}

	return uploadResp.URL, nil
}

// Helper method to get the support server URL
func (s *SupportService) getSupportUrl() string {
	url := os.Getenv("SUPPORT_BASE_URL")
	if url == "" {
		url = "https://support.discopanel.app"
	}
	return url
}

// Helper method to get the upload support URL
func (s *SupportService) getUploadSupportUrl() string {
	return s.getSupportUrl() + "/api/v1/uploads"
}

// Removes a bundle from memory and disk
func (s *SupportService) cleanupBundle(bundleID string) {
	s.bundlesMu.Lock()
	bundleInfo, exists := s.bundles[bundleID]
	if exists {
		delete(s.bundles, bundleID)
	}
	s.bundlesMu.Unlock()

	if exists {
		os.Remove(s.bundlePath(bundleInfo.Filename))
		s.log.Debug("Cleaned up support bundle %s", bundleID)
	}
}

// Adds logs to the tar archive
func (s *SupportService) addLogsToBundle(ctx context.Context, tarWriter *tar.Writer, serverIDs []string) error {
	// Get recent logs from memory buffer
	recentLogs := s.log.GetRecentLogs()
	recentLogsContent := strings.Join(recentLogs, "\n")

	// Add recent logs
	header := &tar.Header{
		Name:    "logs/recent-logs.txt",
		Size:    int64(len(recentLogsContent)),
		Mode:    0644,
		ModTime: time.Now(),
	}

	if err := tarWriter.WriteHeader(header); err != nil {
		return fmt.Errorf("failed to write recent logs header: %w", err)
	}

	if _, err := tarWriter.Write([]byte(recentLogsContent)); err != nil {
		return fmt.Errorf("failed to write recent logs content: %w", err)
	}

	// Add log file if it exists
	logFilePath := s.log.GetLogFilePath()
	if logFilePath != "" && fileExists(logFilePath) {
		if err := addFileToTar(tarWriter, logFilePath, "logs/discopanel.log"); err != nil {
			return fmt.Errorf("failed to add log file: %w", err)
		}
	}

	// Add any rotated log files
	if logFilePath != "" {
		logDir := filepath.Dir(logFilePath)
		if logDir != "" && logDir != "." {
			files, _ := os.ReadDir(logDir)
			for _, file := range files {
				if strings.HasPrefix(file.Name(), "discopanel") && strings.Contains(file.Name(), ".log") {
					fullPath := filepath.Join(logDir, file.Name())
					if err := addFileToTar(tarWriter, fullPath, filepath.Join("logs", file.Name())); err != nil {
						s.log.Warn("Failed to add log file %s: %v", file.Name(), err)
					}
				}
			}
		}
	}

	// Add server-specific logs if requested
	if len(serverIDs) > 0 {
		for _, serverID := range serverIDs {
			server, err := s.store.GetServer(ctx, serverID)
			if err != nil {
				s.log.Warn("Failed to get server %s for logs: %v", serverID, err)
				continue
			}

			// Add server's latest.log if it exists
			latestLogPath := filepath.Join(server.DataPath, "logs", "latest.log")
			if fileExists(latestLogPath) {
				targetPath := fmt.Sprintf("servers/%s/latest.log", server.Name)
				if err := addFileToTar(tarWriter, latestLogPath, targetPath); err != nil {
					s.log.Warn("Failed to add server log for %s: %v", server.Name, err)
				}
			}
		}
	}

	return nil
}

// Substrings marking a name as secret bearing
var secretNameMarkers = []string{"secret", "password", "passwd", "api_key", "apikey", "token"}

// True when a column or key name holds secret material
func isSensitiveName(name string) bool {
	n := strings.ToLower(name)
	if strings.HasPrefix(n, "is_") || strings.HasSuffix(n, "_id") {
		return false
	}
	for _, marker := range secretNameMarkers {
		if strings.Contains(n, marker) {
			return true
		}
	}
	return false
}

// Adds a secret-scrubbed database snapshot to the tar archive
func (s *SupportService) addDatabaseToBundle(ctx context.Context, tarWriter *tar.Writer, tempDir string) error {
	copyPath := filepath.Join(tempDir, "discopanel-scrubbed.db")

	// Snapshot through the live connection stays WAL consistent
	snapshotSQL := fmt.Sprintf("VACUUM INTO '%s'", strings.ReplaceAll(copyPath, "'", "''"))
	if err := s.store.DB().WithContext(ctx).Exec(snapshotSQL).Error; err != nil {
		return fmt.Errorf("failed to snapshot database: %w", err)
	}

	if err := scrubDatabaseCopy(copyPath); err != nil {
		return fmt.Errorf("failed to scrub database copy: %w", err)
	}

	return addFileToTar(tarWriter, copyPath, "database/discopanel.db")
}

// Overwrites secret columns and truncates sessions in the copy
func scrubDatabaseCopy(path string) error {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return fmt.Errorf("failed to open database copy: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to get database handle: %w", err)
	}
	defer sqlDB.Close()

	var tables []string
	if err := db.Table("sqlite_master").Where("type = ?", "table").Pluck("name", &tables).Error; err != nil {
		return fmt.Errorf("failed to list tables: %w", err)
	}

	for _, table := range tables {
		// Session rows are live JWTs, drop them whole
		if table == "sessions" {
			if err := db.Exec("DELETE FROM sessions").Error; err != nil {
				return fmt.Errorf("failed to truncate sessions: %w", err)
			}
			continue
		}

		var cols []struct{ Name string }
		if err := db.Raw(fmt.Sprintf("PRAGMA table_info(%q)", table)).Scan(&cols).Error; err != nil {
			return fmt.Errorf("failed to read columns of %s: %w", table, err)
		}
		for _, col := range cols {
			redact := isSensitiveName(col.Name)
			// Invite codes and pins gate account creation
			if table == "registration_invites" && (strings.EqualFold(col.Name, "code") || strings.EqualFold(col.Name, "pin_hash")) {
				redact = true
			}
			if !redact {
				continue
			}
			stmt := fmt.Sprintf("UPDATE %q SET %q = 'REDACTED' WHERE %q IS NOT NULL AND %q != ''", table, col.Name, col.Name, col.Name)
			if err := db.Exec(stmt).Error; err != nil {
				return fmt.Errorf("failed to scrub %s.%s: %w", table, col.Name, err)
			}
		}

		// Key value settings hide secrets behind the key name
		if table == "system_settings" {
			if err := db.Exec("UPDATE system_settings SET value = 'REDACTED' WHERE " + settingKeyPredicate()).Error; err != nil {
				return fmt.Errorf("failed to scrub system settings: %w", err)
			}
		}

		// Webhook secrets hide inside task config JSON
		if table == "scheduled_tasks" {
			if err := scrubTaskConfigs(db); err != nil {
				return fmt.Errorf("failed to scrub task configs: %w", err)
			}
		}
	}

	// Vacuum drops overwritten row images from free pages
	if err := db.Exec("VACUUM").Error; err != nil {
		return fmt.Errorf("failed to vacuum database copy: %w", err)
	}
	return nil
}

// Builds a where clause matching secret bearing setting keys
func settingKeyPredicate() string {
	parts := make([]string, 0, len(secretNameMarkers))
	for _, marker := range secretNameMarkers {
		parts = append(parts, fmt.Sprintf("lower(key) LIKE '%%%s%%'", marker))
	}
	return strings.Join(parts, " OR ")
}

// Redacts secret shaped keys in each webhook task config
func scrubTaskConfigs(db *gorm.DB) error {
	var rows []struct {
		ID            string
		WebhookConfig string
	}
	if err := db.Raw("SELECT id, webhook_config FROM scheduled_tasks WHERE webhook_config IS NOT NULL AND webhook_config != ''").Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var m map[string]any
		if json.Unmarshal([]byte(row.WebhookConfig), &m) != nil {
			continue
		}
		changed := false
		for k, v := range m {
			if str, ok := v.(string); ok && str != "" && isSensitiveName(k) {
				m[k] = "REDACTED"
				changed = true
			}
		}
		if !changed {
			continue
		}
		out, err := json.Marshal(m)
		if err != nil {
			continue
		}
		if err := db.Exec("UPDATE scheduled_tasks SET webhook_config = ? WHERE id = ?", string(out), row.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

// Adds server configuration files to the bundle
func (s *SupportService) addServerPropertiesToBundle(ctx context.Context, tarWriter *tar.Writer, serverIDs []string) error {
	var servers []*v1.Server
	var err error

	if len(serverIDs) > 0 {
		// Get specific servers
		for _, id := range serverIDs {
			server, err := s.store.GetServer(ctx, id)
			if err == nil {
				servers = append(servers, server)
			}
		}
	} else {
		// Get all servers
		servers, err = s.store.ListServers(ctx)
		if err != nil {
			return fmt.Errorf("failed to list servers: %w", err)
		}
	}

	// Add each server's configuration
	for _, server := range servers {
		// Get server config from database
		serverConfig, err := s.store.GetServerProperties(ctx, server.Id)
		if err != nil {
			s.log.Warn("Failed to get config for server %s: %v", server.Name, err)
			continue
		}

		// Marshal server config to JSON with secrets redacted
		configData, err := redactedConfigJSON(serverConfig)
		if err != nil {
			s.log.Warn("Failed to marshal config for server %s: %v", server.Name, err)
			continue
		}

		// Add to tar
		header := &tar.Header{
			Name:    fmt.Sprintf("configs/servers/%s_config.json", server.Name),
			Size:    int64(len(configData)),
			Mode:    0644,
			ModTime: time.Now(),
		}

		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("failed to write config header for %s: %w", server.Name, err)
		}

		if _, err := tarWriter.Write(configData); err != nil {
			return fmt.Errorf("failed to write config content for %s: %w", server.Name, err)
		}

		// Also add server.properties if it exists
		serverPropsPath := filepath.Join(server.DataPath, "server.properties")
		if fileExists(serverPropsPath) {
			targetPath := fmt.Sprintf("configs/servers/%s_server.properties", server.Name)
			if err := addRedactedPropertiesToTar(tarWriter, serverPropsPath, targetPath); err != nil {
				s.log.Warn("Failed to add server.properties for %s: %v", server.Name, err)
			}
		}
	}

	return nil
}

// Marshals a settings message with secret values redacted
func redactedConfigJSON(cfg proto.Message) ([]byte, error) {
	raw, err := protojson.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for k, v := range m {
		if str, ok := v.(string); ok && str != "" && isSensitiveName(k) {
			m[k] = "REDACTED"
		}
	}
	return json.MarshalIndent(m, "", "  ")
}

// Copies a properties file with secret values redacted
func addRedactedPropertiesToTar(tw *tar.Writer, sourcePath, destPath string) error {
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, val, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(val) == "" {
			continue
		}
		// Property keys use dots and dashes, normalize before matching
		normalized := strings.NewReplacer(".", "_", "-", "_").Replace(strings.TrimSpace(key))
		if isSensitiveName(normalized) {
			lines[i] = strings.TrimSpace(key) + "=REDACTED"
		}
	}

	content := []byte(strings.Join(lines, "\n"))
	header := &tar.Header{
		Name:    destPath,
		Size:    int64(len(content)),
		Mode:    0644,
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err = tw.Write(content)
	return err
}

// Adds system and configuration information to bundle
func (s *SupportService) addSystemInfoToBundle(ctx context.Context, tarWriter *tar.Writer) error {
	servers, _ := s.store.ListServers(ctx)

	// Collect Docker information if available
	var dockerInfo *v1.DockerInfo
	if s.docker != nil {
		// Get Docker version and info
		dockerClient := s.docker.GetDockerClient()
		if dockerClient != nil {
			info, err := dockerClient.Info(ctx)
			if err == nil {
				dockerInfo = &v1.DockerInfo{
					Version:     info.ServerVersion,
					Containers:  int32(info.Containers),
					Images:      int32(info.Images),
					MemoryLimit: info.MemTotal,
					Cpus:        int32(info.NCPU),
				}
			}
		}
	}

	// Database rows carry the live proxy truth
	proxyEnabled := false
	var proxyPorts []int32
	if proxyConfig, _, err := s.store.GetProxyConfig(ctx); err == nil {
		proxyEnabled = proxyConfig.Enabled
	}
	listeners, listenersErr := s.store.ListProxyListeners(ctx)
	if listenersErr == nil {
		for _, l := range listeners {
			if l.Enabled {
				proxyPorts = append(proxyPorts, l.Port)
			}
		}
	}

	// Build config info
	configInfo := &v1.ConfigInfo{
		StorageDir:     s.config.Storage.DataDir,
		TempDir:        s.config.Storage.TempDir,
		DatabasePath:   s.config.Database.Path,
		ProxyEnabled:   proxyEnabled,
		ProxyPorts:     proxyPorts,
		ServerHost:     s.config.Server.Host,
		ServerPort:     s.config.Server.Port,
		LoggingEnabled: s.config.Logging.Enabled,
		LogFilePath:    s.config.Logging.FilePath,
		LogMaxSize:     int32(s.config.Logging.MaxSize),
	}

	// Build proxy config if enabled
	var proxyConfigInfo *v1.ProxyConfigInfo
	if proxyEnabled {
		proxyConfig, _, err := s.store.GetProxyConfig(ctx)
		if err == nil {
			proxyConfigInfo = &v1.ProxyConfigInfo{
				Enabled:   proxyConfig.Enabled,
				Hostnames: proxyConfig.Hostnames,
			}
			if listenersErr == nil {
				for _, listener := range listeners {
					proxyConfigInfo.Listeners = append(proxyConfigInfo.Listeners, &v1.ProxyListenerInfo{
						Name:      listener.Name,
						Port:      int32(listener.Port),
						Enabled:   listener.Enabled,
						IsDefault: listener.IsDefault,
					})
				}
			}
		}
	}

	// Build server summaries
	serverSummaries := make([]*v1.ServerSummary, 0, len(servers))
	for _, server := range servers {
		serverSummaries = append(serverSummaries, &v1.ServerSummary{
			Id:          server.Id,
			Name:        server.Name,
			ModLoader:   server.ModLoader,
			McVersion:   server.McVersion,
			Status:      server.Status,
			Port:        int32(server.Port),
			Memory:      int32(server.Memory),
			AutoStart:   server.AutoStart,
			DockerImage: server.DockerImage,
		})
	}

	// Build the complete system info
	systemInfo := &v1.SystemInfo{
		Timestamp:   time.Now().Format(time.RFC3339),
		Version:     getVersionInfo(),
		ServerCount: int32(len(servers)),
		Config:      configInfo,
		Docker:      dockerInfo,
		ProxyConfig: proxyConfigInfo,
		Servers:     serverSummaries,
	}

	jsonData, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(systemInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal system info: %w", err)
	}

	header := &tar.Header{
		Name:    "system-info.json",
		Size:    int64(len(jsonData)),
		Mode:    0644,
		ModTime: time.Now(),
	}

	if err := tarWriter.WriteHeader(header); err != nil {
		return fmt.Errorf("failed to write system info header: %w", err)
	}

	if _, err := tarWriter.Write(jsonData); err != nil {
		return fmt.Errorf("failed to write system info content: %w", err)
	}

	return nil
}

// Adds a file to the tar archive
func addFileToTar(tw *tar.Writer, sourcePath, destPath string) error {
	file, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return err
	}

	header := &tar.Header{
		Name:    destPath,
		Size:    stat.Size(),
		Mode:    0644,
		ModTime: stat.ModTime(),
	}

	if err := tw.WriteHeader(header); err != nil {
		return err
	}

	_, err = io.Copy(tw, file)
	return err
}

// Checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// Gets version information for the application
func getVersionInfo() string {
	// Check env var first
	if appV := os.Getenv("APP_VERSION"); appV != "" {
		return appV
	}

	// Check version file stored in home
	if home, err := os.UserHomeDir(); err == nil {
		versionFile := filepath.Join(home, ".discopanel")
		if data, err := os.ReadFile(versionFile); err == nil {
			if v := strings.TrimSpace(string(data)); v != "" {
				return v
			}
		}
	}

	info, err := buildinfo.ReadFile(os.Args[0])
	if err != nil {
		return "unknown"
	}

	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}

	return "unknown"
}

// Returns the application log file content
func (s *SupportService) GetApplicationLogs(ctx context.Context, req *connect.Request[v1.GetApplicationLogsRequest]) (*connect.Response[v1.GetApplicationLogsResponse], error) {
	logFilePath := s.log.GetLogFilePath()
	if logFilePath == "" {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("logging to file is not enabled"))
	}

	if !fileExists(logFilePath) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("log file not found"))
	}

	logFile, err := os.Open(logFilePath)
	if err != nil {
		s.log.Error("Failed to open log file: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read log file"))
	}
	defer logFile.Close()

	fileInfo, err := logFile.Stat()
	if err != nil {
		s.log.Error("Failed to stat log file: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get log file info"))
	}

	// Reads at most the last megabyte of the file
	readSize := fileInfo.Size()
	var offset int64
	if readSize > maxAppLogTailBytes {
		offset = readSize - maxAppLogTailBytes
		readSize = maxAppLogTailBytes
	}
	buf := make([]byte, readSize)
	n, err := logFile.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		s.log.Error("Failed to read log file: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read log file"))
	}
	content := buf[:n]

	// Drops the partial first line after a mid file start
	if offset > 0 {
		if i := bytes.IndexByte(content, '\n'); i >= 0 {
			content = content[i+1:]
		}
	}

	// If tail is specified, only return the last N lines
	tail := int(req.Msg.Tail)
	if tail > 0 {
		lines := strings.Split(string(content), "\n")
		if len(lines) > tail {
			lines = lines[len(lines)-tail:]
		}
		content = []byte(strings.Join(lines, "\n"))
	}

	return connect.NewResponse(&v1.GetApplicationLogsResponse{
		Content:  string(content),
		Filename: filepath.Base(logFilePath),
		Size:     fileInfo.Size(),
	}), nil
}
