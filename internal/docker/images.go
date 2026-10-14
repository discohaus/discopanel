package docker

import (
	"fmt"
	"strconv"

	"github.com/discohaus/discopanel/pkg/javaversions"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Default discoruntime repo, overridable via docker.runtime_image
const DefaultRuntimeImage = "ghcr.io/discohaus/discoruntime"

// Java majors the runtime image publishes
var SupportedJavaVersions = javaversions.Supported

// Majors with a published GraalVM variant
var GraalJavaVersions = javaversions.Graal

// Resolves needed Java major, rounded up to nearest published image
func RequiredJavaMajor(mcVersion string) (int, error) {
	v, err := minecraft.GetJavaVersion(mcVersion)
	if err != nil {
		return 0, fmt.Errorf("java version for minecraft %s: %w", mcVersion, err)
	}
	required, err := strconv.Atoi(v)
	if err != nil || required <= 0 {
		return 0, fmt.Errorf("java version for minecraft %s: mojang metadata reports %q", mcVersion, v)
	}
	return publishedJavaMajor(required), nil
}

// Rounds a Java major up to the nearest published image
func publishedJavaMajor(required int) int {
	for _, supported := range SupportedJavaVersions {
		if supported >= required {
			return supported
		}
	}
	return SupportedJavaVersions[len(SupportedJavaVersions)-1]
}

// Returns the image tag for a Java major
func RuntimeImageTag(javaMajor int) string {
	return javaversions.Tag(javaMajor)
}

// Returns the GraalVM variant tag for a Java major
func GraalRuntimeImageTag(javaMajor int) string {
	return javaversions.GraalTag(javaMajor)
}

// Returns the runtime image tag for a Minecraft version
func OptimalRuntimeTag(mcVersion string) (string, error) {
	major, err := RequiredJavaMajor(mcVersion)
	if err != nil {
		return "", err
	}
	return RuntimeImageTag(major), nil
}

// Returns the configured runtime image repository
func (c *Client) runtimeRepository() string {
	if c.config.RuntimeImage != "" {
		return c.config.RuntimeImage
	}
	return DefaultRuntimeImage
}

// Returns the full runtime image reference for a Java major
func (c *Client) RuntimeImage(javaMajor int) string {
	return c.runtimeRepository() + ":" + RuntimeImageTag(javaMajor)
}

// Returns the full image reference for a stored tag
func (c *Client) RuntimeImageForTag(tag string) string {
	return c.runtimeRepository() + ":" + tag
}

// Stored tag wins, then resolved Java, then network lookup
func (c *Client) DesiredImage(server *v1.Server) (string, error) {
	if server.DockerImage != "" {
		return c.RuntimeImageForTag(server.DockerImage), nil
	}
	if major := int(server.JavaVersion); major > 0 {
		return c.RuntimeImage(publishedJavaMajor(major)), nil
	}
	major, err := RequiredJavaMajor(server.McVersion)
	if err != nil {
		return "", err
	}
	return c.RuntimeImage(major), nil
}

// Reports whether a tag matches a published runtime image
func IsValidRuntimeTag(tag string) bool {
	return javaversions.ValidTag(tag)
}

// Lists runtime image variants, newest first, GraalVM last
func RuntimeImages() []*v1.DockerImage {
	images := make([]*v1.DockerImage, 0, len(SupportedJavaVersions)+len(GraalJavaVersions))
	for i := len(SupportedJavaVersions) - 1; i >= 0; i-- {
		v := SupportedJavaVersions[i]
		images = append(images, &v1.DockerImage{
			Tag:         RuntimeImageTag(v),
			DisplayName: fmt.Sprintf("Java %d (discoruntime)", v),
			Description: fmt.Sprintf("Minimal Temurin %d JRE runtime, server files are provisioned by DiscoPanel", v),
			Recommended: len(images) == 0,
		})
	}
	for i := len(GraalJavaVersions) - 1; i >= 0; i-- {
		v := GraalJavaVersions[i]
		images = append(images, &v1.DockerImage{
			Tag:         GraalRuntimeImageTag(v),
			DisplayName: fmt.Sprintf("Java %d GraalVM (discoruntime)", v),
			Description: fmt.Sprintf("Oracle GraalVM %d JIT runtime; often faster ticks on modded servers, worth benchmarking per pack", v),
		})
	}
	return images
}
