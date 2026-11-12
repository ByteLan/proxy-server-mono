package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var linuxAMD64 = platform{os: "linux", arch: "amd64"}

func testELF(machine elf.Machine) []byte {
	raw := make([]byte, 128)
	copy(raw, "\x7fELF")
	raw[4], raw[5], raw[6] = 2, 1, 1
	binary.LittleEndian.PutUint16(raw[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(raw[18:], uint16(machine))
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	binary.LittleEndian.PutUint16(raw[54:], 56)
	binary.LittleEndian.PutUint16(raw[58:], 64)
	copy(raw[64:], "verified test executable; never executed")
	return raw
}

func checksum(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

type fixture struct {
	server           *httptest.Server
	release          release
	binary           []byte
	manifest         []byte
	metadataRequests atomic.Int32
	assetRequests    atomic.Int32
	token            string
	externalURL      string
}

func newFixture(t *testing.T, raw []byte) *fixture {
	t.Helper()
	f := &fixture{binary: raw, token: "private-test-token"}
	f.manifest = []byte(checksum(raw) + "  proxy-server_linux_amd64\n")
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			t.Error("GitHub API request omitted private repository token")
		}
		if strings.Contains(r.URL.RawQuery, f.token) {
			t.Error("token placed in URL")
		}
		if strings.Contains(r.URL.Path, "/assets/") {
			f.assetRequests.Add(1)
			if r.Header.Get("Accept") != "application/octet-stream" {
				t.Error("asset request must use octet-stream")
			}
			switch r.URL.Path {
			case "/repos/ByteLan/proxy-server-mono/releases/assets/1":
				_, _ = w.Write(f.manifest)
			case "/repos/ByteLan/proxy-server-mono/releases/assets/2":
				if f.externalURL != "" {
					http.Redirect(w, r, f.externalURL, http.StatusFound)
					return
				}
				_, _ = w.Write(f.binary)
			default:
				http.NotFound(w, r)
			}
			return
		}
		f.metadataRequests.Add(1)
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Error("metadata accept header")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/ByteLan/proxy-server-mono/releases" {
			_ = json.NewEncoder(w).Encode([]release{f.release})
			return
		}
		_ = json.NewEncoder(w).Encode(f.release)
	}))
	f.release = release{Tag: "main-abcdef012345", Assets: []asset{
		{ID: 1, Name: "SHA256SUMS", URL: f.server.URL + "/repos/ByteLan/proxy-server-mono/releases/assets/1", Size: int64(len(f.manifest))},
		{ID: 2, Name: "proxy-server_linux_amd64", URL: f.server.URL + "/repos/ByteLan/proxy-server-mono/releases/assets/2", Size: int64(len(raw)), Digest: "sha256:" + checksum(raw)},
	}}
	t.Cleanup(f.server.Close)
	return f
}

func oldTarget(t *testing.T) (string, []byte) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "proxy-server")
	old := []byte("old executable")
	if err := os.WriteFile(target, old, 0755); err != nil {
		t.Fatal(err)
	}
	return target, old
}

func fixtureOptions(f *fixture, target string) Options {
	return Options{Repository: DefaultRepository, Token: f.token, APIBase: f.server.URL, Target: target, CurrentVersion: "old"}
}

func TestPrivateDownloadAtomicInstallAndCopiedBackup(t *testing.T) {
	raw := testELF(elf.EM_X86_64)
	f := newFixture(t, raw)
	target, old := oldTarget(t)
	if err := os.Chmod(target, 0777|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(target)
	var output bytes.Buffer
	if err := run(context.Background(), "proxy-server", fixtureOptions(f, target), &output, linuxAMD64); err != nil {
		t.Fatal(err)
	}
	installed, _ := os.ReadFile(target)
	backup, _ := os.ReadFile(target + ".previous")
	if !bytes.Equal(installed, raw) || !bytes.Equal(backup, old) {
		t.Fatal("replacement or backup content mismatch")
	}
	current, _ := os.Stat(target)
	previous, _ := os.Stat(target + ".previous")
	if os.SameFile(before, current) || os.SameFile(before, previous) || os.SameFile(current, previous) {
		t.Fatal("replacement must be atomic and backup must not be a hard link")
	}
	if current.Mode().Perm() != 0755 || current.Mode()&os.ModeSetuid != 0 || previous.Mode()&os.ModeSetuid != 0 {
		t.Fatal("unsafe installed permissions")
	}
	if _, err := os.Lstat(target + ".update.lock"); !os.IsNotExist(err) {
		t.Fatal("update lock was not removed")
	}
	if !strings.Contains(output.String(), "systemctl restart proxy-server") || strings.Contains(output.String(), f.token) {
		t.Fatal("restart instruction missing or token leaked")
	}
	if f.assetRequests.Load() != 2 {
		t.Fatal("expected authenticated manifest and binary API requests")
	}
}

func TestExternalRedirectDoesNotReceiveAuthorization(t *testing.T) {
	raw := testELF(elf.EM_X86_64)
	var calls atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("credentials leaked to redirected asset host")
		}
		_, _ = w.Write(raw)
	}))
	defer external.Close()
	f := newFixture(t, raw)
	f.externalURL = external.URL + "/signed-asset?key=temporary"
	target, _ := oldTarget(t)
	if err := run(context.Background(), "proxy-server", fixtureOptions(f, target), io.Discard, linuxAMD64); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("external redirect not followed")
	}
}

func TestChecksumMismatchDoesNotChangeTarget(t *testing.T) {
	f := newFixture(t, testELF(elf.EM_X86_64))
	f.binary = append(f.binary[:len(f.binary)-1], 42)
	target, old := oldTarget(t)
	err := run(context.Background(), "proxy-server", fixtureOptions(f, target), io.Discard, linuxAMD64)
	if err == nil || !strings.Contains(err.Error(), "SHA256") {
		t.Fatalf("expected checksum failure: %v", err)
	}
	current, _ := os.ReadFile(target)
	if !bytes.Equal(current, old) {
		t.Fatal("checksum failure replaced target")
	}
	if _, err := os.Lstat(target + ".previous"); !os.IsNotExist(err) {
		t.Fatal("verification failure changed backup")
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".proxy-*"))
	if len(files) > 0 {
		t.Fatal("temporary files leaked after failure")
	}
}

func TestInvalidBinaryOrAssetDoesNotChangeTarget(t *testing.T) {
	for _, scenario := range []string{"html", "wrong-arch", "external-api", "wrong-repo", "oversized", "duplicate", "symlink-backup"} {
		t.Run(scenario, func(t *testing.T) {
			raw := testELF(elf.EM_X86_64)
			if scenario == "html" {
				raw = []byte("<html>not an executable</html>")
			}
			if scenario == "wrong-arch" {
				raw = testELF(elf.EM_AARCH64)
			}
			f := newFixture(t, raw)
			target, old := oldTarget(t)
			switch scenario {
			case "external-api":
				f.release.Assets[1].URL = "https://evil.example/repos/ByteLan/proxy-server-mono/releases/assets/2"
			case "wrong-repo":
				f.release.Assets[1].URL = f.server.URL + "/repos/another/repo/releases/assets/2"
			case "oversized":
				f.release.Assets[1].Size = maxBinary + 1
			case "duplicate":
				f.release.Assets = append(f.release.Assets, f.release.Assets[1])
			case "symlink-backup":
				if err := os.Symlink(target, target+".previous"); err != nil {
					t.Fatal(err)
				}
			}
			if err := run(context.Background(), "proxy-server", fixtureOptions(f, target), io.Discard, linuxAMD64); err == nil {
				t.Fatal("invalid asset accepted")
			}
			current, _ := os.ReadFile(target)
			if !bytes.Equal(current, old) {
				t.Fatal("invalid asset changed target")
			}
		})
	}
}

func TestCheckAndSameVersionNeverDownloadOrLock(t *testing.T) {
	for _, scenario := range []string{"check", "same-version"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t, testELF(elf.EM_X86_64))
			opts := fixtureOptions(f, "/does/not/exist")
			if scenario == "check" {
				opts.CheckOnly = true
			} else {
				opts.CurrentVersion = f.release.Tag
			}
			if err := run(context.Background(), "proxy-server", opts, io.Discard, linuxAMD64); err != nil {
				t.Fatal(err)
			}
			if f.assetRequests.Load() != 0 {
				t.Fatal("check/idempotent update downloaded assets")
			}
		})
	}
}

func TestReleaseSelectionMainStablePinnedAndPagination(t *testing.T) {
	for _, scenario := range []string{"main", "stable", "pinned", "pagination"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch scenario {
				case "stable":
					if !strings.HasSuffix(r.URL.Path, "/latest") {
						t.Error("stable did not use latest")
					}
					_ = json.NewEncoder(w).Encode(release{Tag: "v1.2.3"})
				case "pinned":
					if !strings.HasSuffix(r.URL.Path, "/tags/v1.0.0") {
						t.Error("pinned did not use tag API")
					}
					_ = json.NewEncoder(w).Encode(release{Tag: "v1.0.0"})
				case "main":
					_ = json.NewEncoder(w).Encode([]release{
						{Tag: "main-ffffffffffff", Draft: true, PublishedAt: time.Now().Add(time.Hour)},
						{Tag: "main-aaaaaaaaaaaa", PublishedAt: time.Unix(10, 0)},
						{Tag: "v3.0.0", PublishedAt: time.Unix(99, 0)},
						{Tag: "main-bbbbbbbbbbbb", PublishedAt: time.Unix(20, 0)},
						{Tag: "main-cccccccccccc", CreatedAt: time.Unix(30, 0)},
						{Tag: "main-invalid", PublishedAt: time.Unix(99, 0)},
					})
				case "pagination":
					if r.URL.Query().Get("page") == "1" {
						_ = json.NewEncoder(w).Encode(make([]release, 100))
					} else {
						_ = json.NewEncoder(w).Encode([]release{{Tag: "main-cccccccccccc"}})
					}
				}
			}))
			defer server.Close()
			api, err := newGitHub(Options{Repository: DefaultRepository, APIBase: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			channel, version := "main", ""
			if scenario == "stable" {
				channel = "stable"
			}
			if scenario == "pinned" {
				version = "v1.0.0"
			}
			selected, err := api.release(context.Background(), channel, version)
			if err != nil {
				t.Fatal(err)
			}
			want := "main-cccccccccccc"
			if scenario == "stable" {
				want = "v1.2.3"
			}
			if scenario == "pinned" {
				want = "v1.0.0"
			}
			if selected.Tag != want {
				t.Fatalf("got %s want %s", selected.Tag, want)
			}
			if scenario == "pagination" && calls.Load() != 2 {
				t.Fatal("pagination not used")
			}
		})
	}
}

func TestUnsupportedArchitecturesFailBeforeHTTP(t *testing.T) {
	for _, p := range []platform{{os: "windows", arch: "amd64"}, {os: "linux", arch: "386"}, {os: "linux", arch: "arm", arm: "6"}, {os: "darwin", arch: "arm64"}} {
		if err := run(context.Background(), "proxy-server", Options{}, io.Discard, p); err == nil {
			t.Fatalf("unsupported platform accepted: %+v", p)
		}
	}
	if target, err := (platform{os: "linux", arch: "arm", arm: "7"}).target(); err != nil || target != "linux_armv7" {
		t.Fatal("GOARM=7 not supported")
	}
}

func TestBodyBoundsAndStrictManifest(t *testing.T) {
	if _, err := readBounded(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("oversized body accepted")
	}
	for _, raw := range []string{"", strings.Repeat("a", 64) + "  missing", strings.Repeat("z", 64) + "  binary", strings.Repeat("a", 64) + "  ../binary", strings.Repeat("a", 64) + "  binary\n" + strings.Repeat("b", 64) + "  binary"} {
		if _, err := manifestChecksum([]byte(raw), "binary"); err == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
	f := newFixture(t, testELF(elf.EM_X86_64))
	f.manifest = []byte(strings.Repeat("x", maxManifest+1))
	f.release.Assets[0].Size = 1 // Malicious response also cannot bypass streamed bounds.
	target, old := oldTarget(t)
	if err := run(context.Background(), "proxy-server", fixtureOptions(f, target), io.Discard, linuxAMD64); err == nil {
		t.Fatal("oversized manifest accepted")
	}
	current, _ := os.ReadFile(target)
	if !bytes.Equal(current, old) {
		t.Fatal("oversized manifest changed target")
	}
}

func TestLockAndResolvedSymlink(t *testing.T) {
	target, _ := oldTarget(t)
	alias := target + "-link"
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveTarget(alias)
	canonical, _ := filepath.EvalSymlinks(target)
	if err != nil || resolved != canonical {
		t.Fatalf("resolve target: %s %v", resolved, err)
	}
	unlock, err := acquireLock(target)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := acquireLock(target); err == nil {
		t.Fatal("concurrent update lock acquired")
	}
}

func TestTokenEnvironmentPrecedenceAndNoErrorLeak(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "github-environment-secret")
	t.Setenv("GH_TOKEN", "gh-environment-secret")
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(403)
		_, _ = fmt.Fprintln(w, "secret echoed: ", got)
	}))
	defer server.Close()
	err := run(context.Background(), "proxy-server", Options{APIBase: server.URL}, io.Discard, linuxAMD64)
	if err == nil || strings.Contains(err.Error(), "environment-secret") {
		t.Fatal("error body leaked token")
	}
	if got != "Bearer github-environment-secret" {
		t.Fatal("GITHUB_TOKEN should take precedence")
	}
	for _, origin := range []string{"http://api.github.com", "https://evil.example", "https://api.github.com.evil.example", "https://user:pass@api.github.com", "https://api.github.com/path"} {
		if _, err := newGitHub(Options{Repository: DefaultRepository, APIBase: origin}); err == nil {
			t.Fatalf("invalid API origin accepted: %s", origin)
		}
	}
}

func TestProductionRedirectRejectsHTTPAndStripsExternalCredentials(t *testing.T) {
	api, err := newGitHub(Options{Repository: DefaultRepository, Token: "private-secret"})
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{"http://release-assets.githubusercontent.com/signed", "https://release-assets.githubusercontent.com/signed", "https://api.github.com/repos/ByteLan/proxy-server-mono/releases/assets/1"} {
		u, _ := url.Parse(destination)
		request := &http.Request{URL: u, Header: http.Header{"Authorization": []string{"Bearer private-secret"}, "Cookie": []string{"private-cookie"}, "Proxy-Authorization": []string{"private-proxy"}}}
		err := api.client.CheckRedirect(request, []*http.Request{{}})
		if u.Scheme == "http" && err == nil {
			t.Fatal("production redirected credentials to plain HTTP")
		}
		if u.Host != "api.github.com" && request.Header.Get("Authorization") != "" {
			t.Fatal("external redirect retained bearer token")
		}
		if u.Host == "api.github.com" && request.Header.Get("Authorization") != "Bearer private-secret" {
			t.Fatal("same-origin API redirect lost private authentication")
		}
		if request.Header.Get("Cookie") != "" || request.Header.Get("Proxy-Authorization") != "" {
			t.Fatal("redirect retained other credentials")
		}
	}
}

func TestAssetURLAcceptsCanonicalRepositoryCase(t *testing.T) {
	api, err := newGitHub(Options{Repository: "bytelan/proxy-server-mono"})
	if err != nil {
		t.Fatal(err)
	}
	a := asset{ID: 42, Name: "proxy-server_linux_amd64", URL: "https://api.github.com/repos/ByteLan/Proxy-Server-Mono/releases/assets/42", Size: 100}
	if err := api.validateAsset(a, maxBinary); err != nil {
		t.Fatalf("canonical case should be accepted: %v", err)
	}
	a.URL = "https://api.github.com/repos/ByteLan/other-repository/releases/assets/42"
	if err := api.validateAsset(a, maxBinary); err == nil {
		t.Fatal("a different repository was accepted")
	}
}
