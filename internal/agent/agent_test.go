package agent

import (
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"proxy-mono/internal/model"
)

func TestInitGeneratesUsableClientMaterialWithoutServerPrivateKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	cfg, err := Init(InitOptions{ConfigPath: path, Name: "SG", Server: "203.0.113.7", Manager: "https://manager.example", HY2Port: 443, VLESSPort: 443})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != cfg.ID || len(cfg.Endpoints) != 2 {
		t.Fatal("node identity/endpoints not persisted")
	}
	state := filepath.Join(filepath.Dir(path), "node.d")
	certPEM, err := os.ReadFile(filepath.Join(state, "server.crt"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.VerifyHostname("203.0.113.7"); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(cert.Raw)
	if cfg.Endpoints[0].Fingerprint != hex.EncodeToString(hash[:]) || cfg.Endpoints[0].SkipCertVerify {
		t.Fatal("HY2 must verify the pinned certificate")
	}
	xrayBytes, err := os.ReadFile(filepath.Join(state, "xray.json"))
	if err != nil {
		t.Fatal(err)
	}
	var xray struct {
		Inbounds []struct {
			Port           int
			StreamSettings struct {
				Reality struct {
					PrivateKey string   `json:"privateKey"`
					ShortIDs   []string `json:"shortIds"`
				} `json:"realitySettings"`
			} `json:"streamSettings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(xrayBytes, &xray); err != nil {
		t.Fatal(err)
	}
	reality := xray.Inbounds[0].StreamSettings.Reality
	privateBytes, err := base64.RawURLEncoding.DecodeString(reality.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	private, err := ecdh.X25519().NewPrivateKey(privateBytes)
	if err != nil {
		t.Fatal(err)
	}
	if base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()) != cfg.Endpoints[1].PublicKey || reality.ShortIDs[0] != cfg.Endpoints[1].ShortID || xray.Inbounds[0].Port != 443 {
		t.Fatal("REALITY client/server key or port mismatch")
	}
	agentJSON, _ := os.ReadFile(path)
	if strings.Contains(string(agentJSON), reality.PrivateKey) || strings.Contains(string(agentJSON), "PRIVATE KEY") {
		t.Fatal("server private key leaked to agent report config")
	}
	for _, file := range []string{path, filepath.Join(state, "hysteria.yaml"), filepath.Join(state, "xray.json"), filepath.Join(state, "server.key")} {
		stat, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if stat.Mode().Perm() != 0600 {
			t.Fatalf("%s permissions %o", file, stat.Mode().Perm())
		}
	}
	if _, err := Init(InitOptions{ConfigPath: path, Name: "SG", Server: "203.0.113.7", Manager: "https://manager.example"}); err == nil {
		t.Fatal("init overwrote existing identity")
	}
}

func testConfig(t *testing.T, manager string) (string, Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "node.json")
	cfg := Config{ID: "node-test", Name: "node", Manager: manager, AllowInsecureHTTP: true, EnrollToken: "enrollment-key", TokenFile: filepath.Join(filepath.Dir(path), "agent.token"), Endpoints: []model.Endpoint{{ID: "hy2-test", Name: "test HY2", Type: "hysteria2", Server: "203.0.113.7", Port: 443, Password: "client-password", Fingerprint: strings.Repeat("a", 64)}}}
	saveConfig(t, path, cfg)
	return path, cfg
}

func saveConfig(t *testing.T, path string, cfg Config) {
	t.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(path, b); err != nil {
		t.Fatal(err)
	}
}

func heartbeat(t *testing.T, reports <-chan model.HeartbeatRequest) model.HeartbeatRequest {
	t.Helper()
	select {
	case report := <-reports:
		return report
	case <-time.After(3 * time.Second):
		t.Fatal("timed out awaiting heartbeat")
		return model.HeartbeatRequest{}
	}
}

func finish(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent failed to exit after cancellation")
	}
}

func TestEnrollmentHeartbeatOnlyResendsChangedEndpoints(t *testing.T) {
	t.Setenv("ENROLL_TOKEN", "")
	t.Setenv("PROXY_ENROLL_TOKEN", "")
	reports := make(chan model.HeartbeatRequest, 64)
	var registrations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/agent/register":
			registrations.Add(1)
			if r.Header.Get("Authorization") != "Bearer enrollment-key" {
				t.Error("missing enrollment bearer")
			}
			var request model.RegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if len(request.Endpoints) != 1 || request.Endpoints[0].Password != "client-password" {
				t.Error("registration omitted client material")
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"node-secret"}`))
		case "/api/agent/heartbeat":
			if r.Header.Get("Authorization") != "Bearer node-secret" {
				t.Error("missing persisted node bearer")
			}
			var request model.HeartbeatRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			reports <- request
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	path, cfg := testConfig(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	runner := Runner{HeartbeatInterval: 15 * time.Millisecond}
	go func() { result <- runner.Run(ctx, path) }()
	first, second := heartbeat(t, reports), heartbeat(t, reports)
	if first.Endpoints == nil || second.Endpoints != nil || first.ConfigHash != second.ConfigHash {
		t.Fatal("ordinary heartbeat resent credentials, or first heartbeat omitted them")
	}
	cfg.Endpoints[0].Port = 9443
	saveConfig(t, path, cfg)
	for {
		next := heartbeat(t, reports)
		if next.ConfigHash == first.ConfigHash {
			continue
		}
		if next.Endpoints == nil || (*next.Endpoints)[0].Port != 9443 {
			t.Fatal("changed endpoint was not reported")
		}
		break
	}
	cancel()
	finish(t, result)
	if registrations.Load() != 1 {
		t.Fatal("unexpected re-registration")
	}
	b, err := os.ReadFile(cfg.TokenFile)
	if err != nil || string(b) != "node-secret\n" {
		t.Fatal("token not persisted")
	}
	stat, _ := os.Stat(cfg.TokenFile)
	if stat.Mode().Perm() != 0600 {
		t.Fatal("token permissions are not 0600")
	}
}

func TestRevokedNodeTokenNeverReenrolls(t *testing.T) {
	reports := make(chan struct{}, 64)
	var registrations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "register") {
			registrations.Add(1)
		}
		w.WriteHeader(http.StatusUnauthorized)
		reports <- struct{}{}
	}))
	defer server.Close()
	path, cfg := testConfig(t, server.URL)
	if err := atomicWrite(cfg.TokenFile, []byte("revoked-token\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	runner := Runner{RetryMin: time.Millisecond, RetryMax: 3 * time.Millisecond}
	go func() { result <- runner.Run(ctx, path) }()
	for range 2 {
		select {
		case <-reports:
		case <-time.After(2 * time.Second):
			t.Fatal("missing rejected heartbeat")
		}
	}
	cancel()
	finish(t, result)
	if registrations.Load() != 0 {
		t.Fatal("revoked identity re-enrolled")
	}
	b, _ := os.ReadFile(cfg.TokenFile)
	if string(b) != "revoked-token\n" {
		t.Fatal("revoked token was replaced")
	}
}

func TestSupervisorStopsCoreOnCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := newSupervisor(ctx, []Command{{Name: "sleep", Path: "/bin/sh", Args: []string{"-c", "exec sleep 100"}}})
	deadline := time.Now().Add(2 * time.Second)
	for !supervisor.status()[0].Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !supervisor.status()[0].Running {
		t.Fatal("core never started")
	}
	cancel()
	finished := make(chan error, 1)
	go func() { supervisor.wait(); finished <- nil }()
	finish(t, finished)
	if supervisor.status()[0].Running {
		t.Fatal("stopped core reported running")
	}
}

func TestManagerTLSUsesConfiguredCAAndNeverSkipsVerification(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := newHTTPClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := post(context.Background(), client, server.URL, "token", map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	if client.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Fatal("TLS verification disabled")
	}
}

func TestRemotePlainHTTPRejected(t *testing.T) {
	_, cfg := testConfig(t, "https://manager.example")
	cfg.Manager = "http://manager.example"
	if err := cfg.Validate(); err == nil {
		t.Fatal("remote HTTP accepted")
	}
	cfg.Manager = "http://127.0.0.1:8080"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
