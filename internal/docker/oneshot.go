package docker

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/pkg/stdcopy"
)

// Configures a single foreground command run in a container
type OneShotOptions struct {
	Image      string
	Cmd        []string
	DataPath   string // Host path mounted at /data
	WorkingDir string
	User       string // Format is uid then gid
	Name       string
	Labels     map[string]string
}

// Pulls image, runs command, removes container, errors on nonzero exit
func (c *Client) RunOneShot(ctx context.Context, opts OneShotOptions, logFn func(line string)) error {
	if err := c.ensureImage(ctx, opts.Image, logFn); err != nil {
		return err
	}

	// Remove any stale container from a previous interrupted run
	if opts.Name != "" {
		_ = c.docker.ContainerRemove(ctx, opts.Name, container.RemoveOptions{Force: true})
	}

	// Apply global labels from config
	labels := map[string]string{}
	maps.Copy(labels, c.config.Labels)
	maps.Copy(labels, opts.Labels)

	config := &container.Config{
		Image:      opts.Image,
		Entrypoint: opts.Cmd,
		WorkingDir: opts.WorkingDir,
		User:       opts.User,
		Labels:     labels,
	}
	hostConfig := &container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:        mount.TypeBind,
				Source:      TranslateToHostPath(opts.DataPath),
				Target:      "/data",
				BindOptions: &mount.BindOptions{CreateMountpoint: true},
			},
		},
		AutoRemove: false, // Removed explicitly after log collection
	}
	if c.config.DNS != "" {
		hostConfig.DNS = []string{c.config.DNS}
	}

	resp, err := c.docker.ContainerCreate(ctx, config, hostConfig, nil, nil, opts.Name)
	if err != nil {
		return fmt.Errorf("failed to create installer container: %w", err)
	}
	containerID := resp.ID
	defer c.docker.ContainerRemove(context.WithoutCancel(ctx), containerID, container.RemoveOptions{Force: true})

	if err := c.docker.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start installer container: %w", err)
	}

	// Stream output while the command runs
	logsDone := make(chan struct{})
	var lastLines []string
	go func() {
		defer close(logsDone)
		reader, err := c.docker.ContainerLogs(ctx, containerID, container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true,
		})
		if err != nil {
			return
		}
		defer reader.Close()

		lw := &lineWriter{fn: func(line string) {
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				return
			}
			lastLines = append(lastLines, line)
			if len(lastLines) > 20 {
				lastLines = lastLines[1:]
			}
			if logFn != nil {
				logFn(line)
			}
		}}
		defer lw.Close()
		_, _ = stdcopy.StdCopy(lw, lw, reader)
	}()

	statusCh, errCh := c.docker.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("installer container wait failed: %w", err)
		}
	case status := <-statusCh:
		<-logsDone
		if status.StatusCode != 0 {
			tail := strings.Join(lastLines, "\n")
			return fmt.Errorf("installer exited with code %d:\n%s", status.StatusCode, tail)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

// Invokes fn once per full line written to it
type lineWriter struct {
	fn  func(string)
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		idx := -1
		for i, b := range w.buf {
			if b == '\n' {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		w.fn(string(w.buf[:idx]))
		w.buf = w.buf[idx+1:]
	}
	return len(p), nil
}

func (w *lineWriter) Close() error {
	if len(w.buf) > 0 {
		w.fn(string(w.buf))
		w.buf = nil
	}
	return nil
}
