package modupdates

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/discohaus/discopanel/pkg/hub"
)

// CDN hosts update jars are fetched from, the index host is added when on
var downloadHosts = []string{"cdn.modrinth.com", "edge.forgecdn.net", "mediafilez.forgecdn.net"}

// Body cap when upstream published no size
const maxJarBytes = 256 << 20

// Slack over a published size before a body counts as oversized
const sizeSlack = 64 << 10

// Redirects followed per download, all on the original host
const maxRedirects = 3

// Fetches update jars from known CDNs over https only
type Downloader struct {
	client    *http.Client
	userAgent string
	hosts     func() []string
}

func NewDownloader(userAgent string) *Downloader {
	return newDownloader(userAgent, &http.Client{Transport: hub.Transport(nil)}, allowedHosts)
}

func newDownloader(userAgent string, client *http.Client, hosts func() []string) *Downloader {
	c := *client
	c.Timeout = 5 * time.Minute
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
			return fmt.Errorf("refusing redirect to %s", req.URL.Host)
		}
		return nil
	}
	return &Downloader{client: &c, userAgent: userAgent, hosts: hosts}
}

// CDN hosts plus the index host while upstreams route through it
func allowedHosts() []string {
	hosts := slices.Clone(downloadHosts)
	if hub.IndexEnabled() {
		if u, err := url.Parse(hub.IndexBase()); err == nil && u.Host != "" {
			hosts = append(hosts, strings.ToLower(u.Host))
		}
	}
	return hosts
}

// Checks scheme and host before any request goes out
func (d *Downloader) allowed(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("invalid download url")
	}
	if u.Scheme != "https" {
		return errors.New("download url is not https")
	}
	if !slices.Contains(d.hosts(), strings.ToLower(u.Host)) {
		return fmt.Errorf("download host %s is not an allowed CDN", u.Host)
	}
	return nil
}

// Fetches url into a new file at dest, verifying size and checksum
func (d *Downloader) Fetch(ctx context.Context, rawURL, dest, algo, sum string, size int64) error {
	if err := d.allowed(rawURL); err != nil {
		return err
	}
	h := newHash(algo)
	if h == nil || sum == "" {
		return errors.New("no usable checksum for the download")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if d.userAgent != "" {
		req.Header.Set("User-Agent", d.userAgent)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download answered %d", resp.StatusCode)
	}
	limit := int64(maxJarBytes)
	if size > 0 {
		limit = min(limit, size+sizeSlack)
	}
	if resp.ContentLength > limit {
		return errors.New("download is larger than expected")
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = errors.New("download is larger than expected")
	}
	if err == nil && size > 0 && n != size {
		err = errors.New("download size does not match the published size")
	}
	// The digest of an unexpected body is never echoed back
	if err == nil && !digestEqual(h.Sum(nil), sum) {
		err = errors.New("download does not match the published checksum")
	}
	if err != nil {
		os.Remove(dest)
	}
	return err
}
