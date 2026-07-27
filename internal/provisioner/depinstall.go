package provisioner

import (
	"context"

	"github.com/discohaus/discopanel/pkg/indexers/fuego"
	"github.com/discohaus/discopanel/pkg/indexers/modrinth"
)

// Resolves a mod file url, CDN guess covers withheld urls
func (p *Provisioner) resolveModFileURL(ctx context.Context, client *fuego.Client, modID int, file *fuego.File) (string, error) {
	return client.ResolveDownloadURL(ctx, modID, file)
}

func primaryFile(version *modrinth.Version) *modrinth.File {
	for i := range version.Files {
		if version.Files[i].Primary {
			return &version.Files[i]
		}
	}
	if len(version.Files) > 0 {
		return &version.Files[0]
	}
	return nil
}

// Strongest available Modrinth hash for verification
func mrChecksum(h modrinth.Hashes) *checksum {
	if h.SHA512 != "" {
		return &checksum{algo: "sha512", value: h.SHA512}
	}
	if h.SHA1 != "" {
		return &checksum{algo: "sha1", value: h.SHA1}
	}
	return nil
}

// Strongest available mrpack hash for verification
func mrpackChecksum(hashes map[string]string) *checksum {
	if v := hashes["sha512"]; v != "" {
		return &checksum{algo: "sha512", value: v}
	}
	if v := hashes["sha1"]; v != "" {
		return &checksum{algo: "sha1", value: v}
	}
	return nil
}
