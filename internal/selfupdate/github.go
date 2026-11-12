package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maxMetadata = 4 << 20
	maxManifest = 64 << 10
	maxBinary   = 128 << 20
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var mainTagPattern = regexp.MustCompile(`^main-[0-9a-f]{12,40}$`)
var tagPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+~-]{0,127}$`)
var assetNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type release struct {
	Tag         string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Assets      []asset   `json:"assets"`
	PublishedAt time.Time `json:"published_at"`
	CreatedAt   time.Time `json:"created_at"`
}

type asset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

type github struct {
	base          *url.URL
	repository    string
	token         string
	client        *http.Client
	allowLoopback bool
}

func newGitHub(options Options) (*github, error) {
	if len(options.Repository) > 200 || !repositoryPattern.MatchString(options.Repository) {
		return nil, fmt.Errorf("repository must have the form owner/repo")
	}
	base := options.APIBase
	if base == "" {
		base = "https://api.github.com"
	}
	u, err := url.Parse(base)
	if err != nil || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("invalid GitHub API origin")
	}
	loopback := isLoopback(u.Hostname()) && options.APIBase != ""
	if !(u.Scheme == "https" && strings.EqualFold(u.Host, "api.github.com")) && !(loopback && (u.Scheme == "http" || u.Scheme == "https")) {
		return nil, fmt.Errorf("GitHub API must use https://api.github.com (or an injected loopback test server)")
	}
	u.Path = ""
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Minute}
	}
	copyClient := *client
	if copyClient.Timeout == 0 {
		copyClient.Timeout = 3 * time.Minute
	}
	api := &github{base: u, repository: options.Repository, token: options.Token, client: &copyClient, allowLoopback: loopback}
	// Cookies from a caller's jar must not become another cross-host credential.
	copyClient.Jar = nil
	copyClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		request.Header.Del("Authorization")
		request.Header.Del("Proxy-Authorization")
		request.Header.Del("Cookie")
		request.Header.Del("Referer")
		if len(via) >= 10 {
			return fmt.Errorf("too many GitHub asset redirects")
		}
		if request.URL.User != nil || request.URL.Scheme != "https" && !(api.allowLoopback && request.URL.Scheme == "http" && isLoopback(request.URL.Hostname())) {
			return fmt.Errorf("insecure GitHub asset redirect refused")
		}
		if sameOrigin(request.URL, api.base) && api.token != "" {
			request.Header.Set("Authorization", "Bearer "+api.token)
		}
		return nil
	}
	return api, nil
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}
func sameOrigin(a, b *url.URL) bool { return a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host) }
func (g *github) repoPath() string  { return "/repos/" + g.repository + "/releases" }

func (g *github) release(ctx context.Context, channel, version string) (release, error) {
	var selected release
	if version != "" {
		if !tagPattern.MatchString(version) {
			return selected, fmt.Errorf("invalid release version tag")
		}
		if err := g.json(ctx, g.repoPath()+"/tags/"+url.PathEscape(version), &selected); err != nil {
			return selected, err
		}
		if selected.Tag != version {
			return selected, fmt.Errorf("GitHub returned a different release tag")
		}
	} else if channel == "stable" {
		if err := g.json(ctx, g.repoPath()+"/latest", &selected); err != nil {
			return selected, err
		}
		if selected.Prerelease {
			return selected, fmt.Errorf("latest stable release is marked prerelease")
		}
	} else {
		// Construct pagination ourselves: never follow an untrusted Link header
		// while carrying repository credentials.
		for page := 1; page <= 20; page++ {
			var releases []release
			if err := g.json(ctx, g.repoPath()+"?per_page=100&page="+strconv.Itoa(page), &releases); err != nil {
				return selected, err
			}
			for _, candidate := range releases {
				if !candidate.Draft && mainTagPattern.MatchString(candidate.Tag) && (selected.Tag == "" || candidate.date().After(selected.date())) {
					selected = candidate
				}
			}
			if selected.Tag != "" {
				return selected, nil
			}
			if len(releases) < 100 {
				break
			}
		}
		return selected, fmt.Errorf("no published main-<commit> release found in the first 2000 releases")
	}
	if selected.Draft || !tagPattern.MatchString(selected.Tag) {
		return selected, fmt.Errorf("release is draft or has an invalid tag")
	}
	return selected, nil
}

func (r release) date() time.Time {
	if !r.PublishedAt.IsZero() {
		return r.PublishedAt
	}
	return r.CreatedAt
}

func (g *github) json(ctx context.Context, path string, output any) error {
	response, err := g.get(ctx, g.base.String()+path, "application/vnd.github+json")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := readBounded(response.Body, maxMetadata)
	if err != nil {
		return fmt.Errorf("release metadata exceeds size limit or could not be read")
	}
	if err := json.Unmarshal(raw, output); err != nil {
		return fmt.Errorf("invalid GitHub release metadata")
	}
	return nil
}

func (g *github) releaseAssets(r release, name string) (asset, asset, error) {
	var binary, manifest asset
	for _, a := range r.Assets {
		if a.Name == name {
			if binary.Name != "" {
				return binary, manifest, fmt.Errorf("release contains duplicate binary assets")
			}
			binary = a
		}
		if a.Name == "SHA256SUMS" {
			if manifest.Name != "" {
				return binary, manifest, fmt.Errorf("release contains duplicate checksum assets")
			}
			manifest = a
		}
	}
	if binary.Name == "" || manifest.Name == "" {
		return binary, manifest, fmt.Errorf("release must contain %s and SHA256SUMS", name)
	}
	if err := g.validateAsset(binary, maxBinary); err != nil {
		return binary, manifest, err
	}
	if err := g.validateAsset(manifest, maxManifest); err != nil {
		return binary, manifest, err
	}
	return binary, manifest, nil
}

func (g *github) validateAsset(a asset, limit int64) error {
	u, err := url.Parse(a.URL)
	if err != nil || u.User != nil || !sameOrigin(u, g.base) || u.RawQuery != "" || u.Fragment != "" || a.ID <= 0 || !strings.EqualFold(u.EscapedPath(), g.repoPath()+"/assets/"+strconv.FormatInt(a.ID, 10)) {
		return fmt.Errorf("release asset API URL is outside the selected repository")
	}
	if a.Size <= 0 || a.Size > limit {
		return fmt.Errorf("release asset has an invalid size")
	}
	if a.Digest != "" {
		if _, err := digest(a.Digest); err != nil {
			return err
		}
	}
	return nil
}

func (g *github) get(ctx context.Context, address, accept string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid GitHub API request")
	}
	if !sameOrigin(request.URL, g.base) {
		return nil, fmt.Errorf("GitHub request is outside the trusted API origin")
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("User-Agent", "proxy-mono-selfupdate")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.token != "" {
		request.Header.Set("Authorization", "Bearer "+g.token)
	}
	response, err := g.client.Do(request)
	if err != nil {
		// net/http errors can contain signed redirect URLs or proxy credentials.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("GitHub request failed (check network access, TLS trust and proxy settings)")
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("GitHub request returned HTTP %d (private repositories require a token with Contents:read permission)", response.StatusCode)
	}
	return response, nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("body exceeds size limit")
	}
	return raw, nil
}

func digest(s string) ([]byte, error) {
	if !strings.HasPrefix(s, "sha256:") || len(s) != len("sha256:")+64 {
		return nil, fmt.Errorf("invalid release asset SHA256 digest")
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(s, "sha256:"))
	if err != nil {
		return nil, fmt.Errorf("invalid release asset SHA256 digest")
	}
	return raw, nil
}

func verifyDigest(a asset, raw []byte) error {
	if a.Digest == "" {
		return nil
	}
	expected, err := digest(a.Digest)
	if err != nil {
		return err
	}
	actual := sha256.Sum256(raw)
	if !equalDigest(expected, actual[:]) {
		return fmt.Errorf("release asset does not match its API SHA256 digest")
	}
	return nil
}
