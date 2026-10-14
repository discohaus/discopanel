package metrics

import (
	"testing"
	"time"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func packFile(id string, release v1.ReleaseType, serverPack bool, at time.Time) *v1.IndexedModpackFile {
	return &v1.IndexedModpackFile{Id: id, DisplayName: id, ReleaseType: release, IsServerPack: serverPack, FileDate: timestamppb.New(at)}
}

func TestNewerModpackFile(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	files := []*v1.IndexedModpackFile{
		packFile("beta-1.3", v1.ReleaseType_RELEASE_TYPE_BETA, false, base.Add(72*time.Hour)),
		packFile("server-1.2", v1.ReleaseType_RELEASE_TYPE_RELEASE, true, base.Add(49*time.Hour)),
		packFile("release-1.2", v1.ReleaseType_RELEASE_TYPE_RELEASE, false, base.Add(48*time.Hour)),
		packFile("release-1.1", v1.ReleaseType_RELEASE_TYPE_RELEASE, false, base.Add(24*time.Hour)),
		packFile("release-1.0", v1.ReleaseType_RELEASE_TYPE_RELEASE, false, base),
		packFile("beta-0.9", v1.ReleaseType_RELEASE_TYPE_BETA, false, base.Add(-24*time.Hour)),
	}

	provisioned := base.Add(30 * time.Hour)
	if got := NewerModpackFile(files, "release-1.0", provisioned); got == nil || got.Id != "release-1.2" {
		t.Fatalf("release install should see release-1.2, got %v", got)
	}
	if got := NewerModpackFile(files, "beta-0.9", provisioned); got == nil || got.Id != "beta-1.3" {
		t.Fatalf("beta install should see beta-1.3, got %v", got)
	}
	if got := NewerModpackFile(files, "server-1.2", provisioned); got != nil {
		t.Fatalf("server pack install has no newer server pack, got %v", got)
	}
	if got := NewerModpackFile(files, "release-1.2", provisioned); got != nil {
		t.Fatalf("latest release has no update, got %v", got)
	}
	// Deleted upstream file falls back to releases after the provision time
	if got := NewerModpackFile(files, "deleted", provisioned); got == nil || got.Id != "release-1.2" {
		t.Fatalf("unlisted install should use provision time, got %v", got)
	}
	if got := NewerModpackFile(files, "deleted", time.Time{}); got != nil {
		t.Fatalf("unlisted install without a provision time cannot be compared, got %v", got)
	}
}
