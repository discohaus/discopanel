package docker

import (
	"context"

	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// Removes containers no longer tracked in the database
func (c *Client) CleanupOrphanedContainers(ctx context.Context, trackedContainerIDs map[string]bool, log *logger.Logger) error {
	// List all containers managed by discopanel
	filterArgs := filters.NewArgs()
	filterArgs.Add("label", "discopanel.managed=true")

	containers, err := c.docker.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return err
	}

	for _, cont := range containers {
		// Check if this container is tracked in the database
		if !trackedContainerIDs[cont.ID] {
			log.Info("Found orphaned container %s (%s), removing...", cont.ID[:12], cont.Names[0])

			// Stop container if running
			if cont.State == "running" {
				timeout := 30
				if err := c.docker.ContainerStop(ctx, cont.ID, container.StopOptions{
					Timeout: &timeout,
				}); err != nil {
					log.Error("Failed to stop orphaned container %s: %v", cont.ID[:12], err)
				}
			}

			// Remove container
			if err := c.docker.ContainerRemove(ctx, cont.ID, container.RemoveOptions{
				Force: true,
			}); err != nil {
				log.Error("Failed to remove orphaned container %s: %v", cont.ID[:12], err)
			} else {
				log.Info("Successfully removed orphaned container %s", cont.ID[:12])
			}
		}
	}

	return nil
}
