package agent

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"proxy-mono/internal/clash"
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

func TestAdoptImportsExistingHysteriaAndXrayWithoutLeakingPrivateKeys(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM, expectedFP, err := selfSigned("bing.com")
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}

	hyPath := filepath.Join(dir, "hysteria.yaml")
	hyYAML := "listen: :24443,50000-55000\ntls:\n  cert: server.crt\n  key: server.key\nauth:\n  type: password\n  password: existing-hy2-secret\nmasquerade:\n  type: proxy\n"
	if err := os.WriteFile(hyPath, []byte(hyYAML), 0600); err != nil {
		t.Fatal(err)
	}

	realityKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privEncoded := base64.RawURLEncoding.EncodeToString(realityKey.Bytes())
	pubEncoded := base64.RawURLEncoding.EncodeToString(realityKey.PublicKey().Bytes())

	xrPath := filepath.Join(dir, "xray.json")
	xrJSONC := `{
  // Existing multi-inbound Xray configuration with JSONC comments
  "inbounds": [
    {
      "tag": "vless-reality",
      "port": 443,
      "protocol": "vless",
      "settings": {
        "clients": [{"id": "11111111-2222-4333-8444-555555555555", "flow": "xtls-rprx-vision"}],
        "decryption": "none"
      },
      "streamSettings": {
        "network": "tcp",
        "security": "reality",
        "realitySettings": {
          "dest": "www.microsoft.com:443",
          "serverNames": ["www.microsoft.com"],
          "privateKey": "` + privEncoded + `",
          "shortIds": ["a1b2c3d4"]
        }
      }
    },
    /* Secondary SOCKS5/HTTP/HTTPS inbounds */
    {
      "port": 1080,
      "protocol": "socks",
      "settings": {"auth": "password", "accounts": [{"user": "socks-u", "pass": "socks-p"}], "udp": true}
    },
    {
      "port": 8080,
      "protocol": "http",
      "settings": {"accounts": [{"user": "http-u", "pass": "http-p"}]}
    },
    {
      "port": "8443",
      "protocol": "http",
      "settings": {"accounts": [{"user": "https-u", "pass": "https-p"}]},
      "streamSettings": {
        "security": "tls",
        "tlsSettings": {"certificates": [{"certificateFile": "` + certPath + `", "keyFile": "` + keyPath + `"}]}
      }
    }
  ]
}`
	if err := os.WriteFile(xrPath, []byte(xrJSONC), 0600); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(dir, "agent", "config.json")
	cfg, err := Adopt(AdoptOptions{
		ConfigPath:     configPath,
		Server:         "203.0.113.35",
		Name:           "sg01",
		Manager:        "https://manager.example",
		HysteriaConfig: hyPath,
		XrayConfig:     xrPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Commands) != 0 {
		t.Fatalf("adopted node must not supervise child core commands: %+v", cfg.Commands)
	}
	if len(cfg.Services) != 2 || cfg.Services[0].Unit != "hysteria.service" || cfg.Services[1].Unit != "xray.service" {
		t.Fatalf("unexpected adopted services: %+v", cfg.Services)
	}
	if len(cfg.Endpoints) != 5 {
		t.Fatalf("expected 5 adopted endpoints, got %d: %+v", len(cfg.Endpoints), cfg.Endpoints)
	}
	hyEP := cfg.Endpoints[0]
	if hyEP.Type != "hysteria2" || hyEP.Port != 24443 || hyEP.SNI != "bing.com" || hyEP.Fingerprint != expectedFP || hyEP.SkipCertVerify {
		t.Fatalf("unexpected adopted HY2 endpoint: %+v", hyEP)
	}
	vlessEP := cfg.Endpoints[1]
	if vlessEP.Type != "vless" || vlessEP.Port != 443 || vlessEP.PublicKey != pubEncoded || vlessEP.ShortID != "a1b2c3d4" || vlessEP.SNI != "www.microsoft.com" {
		t.Fatalf("unexpected adopted VLESS endpoint: %+v", vlessEP)
	}
	if cfg.Endpoints[2].Type != "socks5" || !cfg.Endpoints[2].UDP || cfg.Endpoints[3].Type != "http" || cfg.Endpoints[4].Type != "https" || cfg.Endpoints[4].SNI != "bing.com" || cfg.Endpoints[4].Fingerprint != expectedFP {
		t.Fatalf("unexpected secondary Xray endpoints: %+v", cfg.Endpoints[2:])
	}

	rawConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawConfig), privEncoded) || strings.Contains(string(rawConfig), "PRIVATE KEY") {
		t.Fatal("private key leaked into adopted config.json")
	}
	rendered, err := clash.Render([]model.Node{{ID: cfg.ID, Name: cfg.Name, Enabled: true, Endpoints: cfg.Endpoints}}, nil)
	if err != nil || !strings.Contains(string(rendered), "existing-hy2-secret") || !strings.Contains(string(rendered), pubEncoded) {
		t.Fatalf("clash render failed on adopted endpoints: %v", err)
	}
	if _, err := Adopt(AdoptOptions{ConfigPath: configPath, Server: "203.0.113.35", Name: "sg01", Manager: "https://manager.example", HysteriaConfig: hyPath}); err == nil {
		t.Fatal("adopt overwrote existing config")
	}
}

func TestRunnerReportsAdoptedServiceStatusesInHeartbeat(t *testing.T) {
	reports := make(chan model.HeartbeatRequest, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/agent/register":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"adopted-node-secret"}`))
		case "/api/agent/heartbeat":
			var req model.HeartbeatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			reports <- req
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	path, cfg := testConfig(t, server.URL)
	cfg.Commands = nil
	cfg.Services = []ExternalService{
		{Name: "hysteria", Unit: "hysteria.service"},
		{Name: "xray", Unit: "xray.service"},
	}
	saveConfig(t, path, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	runner := Runner{
		HeartbeatInterval: 15 * time.Millisecond,
		ServiceProbe: func(_ context.Context, unit string) (bool, string) {
			if unit == "hysteria.service" {
				return true, ""
			}
			return false, "systemd unit " + unit + " is not active"
		},
	}
	go func() { result <- runner.Run(ctx, path) }()

	hb := heartbeat(t, reports)
	if len(hb.Cores) != 2 || !hb.Cores[0].Running || hb.Cores[0].Name != "hysteria" || hb.Cores[1].Running || hb.Cores[1].Name != "xray" {
		t.Fatalf("unexpected service probe statuses in heartbeat: %+v", hb.Cores)
	}
	cancel()
	finish(t, result)
}

func TestMultiManagerConcurrentEnrollmentAndLifecycle(t *testing.T) {
	// Manager 1: primary manager using ENROLL_TOKEN environment variable + TLS CA 1
	reports1 := make(chan model.HeartbeatRequest, 16)
	mgr1 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/agent/register":
			if r.Header.Get("Authorization") != "Bearer enroll-mgr-1-secret-token-123456" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"node-token-from-mgr1"}`))
		case "/api/agent/heartbeat":
			if r.Header.Get("Authorization") != "Bearer node-token-from-mgr1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var req model.HeartbeatRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			reports1 <- req
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mgr1.Close()

	// Manager 2: secondary manager added via AddManager with its own enroll token + TLS CA 2
	reports2 := make(chan model.HeartbeatRequest, 16)
	mgr2 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/agent/register":
			if r.Header.Get("Authorization") != "Bearer enroll-mgr-2-secret-token-654321" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"node-token-from-mgr2"}`))
		case "/api/agent/heartbeat":
			if r.Header.Get("Authorization") != "Bearer node-token-from-mgr2" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var req model.HeartbeatRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			reports2 <- req
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mgr2.Close()

	dir := t.TempDir()
	ca1Path := filepath.Join(dir, "ca1.pem")
	ca2Path := filepath.Join(dir, "ca2.pem")
	_ = os.WriteFile(ca1Path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: mgr1.Certificate().Raw}), 0600)
	_ = os.WriteFile(ca2Path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: mgr2.Certificate().Raw}), 0600)

	configPath := filepath.Join(dir, "config.json")
	if _, err := Init(InitOptions{
		ConfigPath: configPath,
		Server:     "203.0.113.35",
		Name:       "sg01",
		Manager:    mgr1.URL,
		CAFile:     ca1Path,
		HY2Port:    8443,
		VLESSPort:  9443,
	}); err != nil {
		t.Fatal(err)
	}
	// Disable child process spawning in unit test
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Commands = nil
	saveConfig(t, configPath, cfg)

	// Add second manager using AddManager while ENROLL_TOKEN holds mgr2's key
	t.Setenv("ENROLL_TOKEN", "enroll-mgr-2-secret-token-654321")
	updated, err := AddManager(AddManagerOptions{
		ConfigPath: configPath,
		Manager:    mgr2.URL,
		CAFile:     ca2Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.EffectiveManagers()) != 2 {
		t.Fatalf("expected 2 managers, got %d", len(updated.EffectiveManagers()))
	}
	// Duplicate manager must be rejected
	if _, err := AddManager(AddManagerOptions{ConfigPath: configPath, Manager: mgr2.URL}); err == nil {
		t.Fatal("duplicate manager URL was accepted")
	}

	// Simulate node.env still holding mgr1's ENROLL_TOKEN at runtime!
	// mgr1 uses ENROLL_TOKEN from env, while mgr2 uses its own stored EnrollToken without collision.
	t.Setenv("ENROLL_TOKEN", "enroll-mgr-1-secret-token-123456")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	runner := Runner{HeartbeatInterval: 20 * time.Millisecond}
	go func() { done <- runner.Run(ctx, configPath) }()

	hb1 := heartbeat(t, reports1)
	hb2 := heartbeat(t, reports2)
	if hb1.ID != cfg.ID || hb2.ID != cfg.ID || hb1.Endpoints == nil || hb2.Endpoints == nil {
		t.Fatalf("both managers must receive initial registration and endpoint heartbeats: %+v / %+v", hb1, hb2)
	}
	cancel()
	finish(t, done)

	statuses, err := ListManagers(configPath)
	if err != nil || len(statuses) != 2 || !statuses[0].Enrolled || !statuses[1].Enrolled {
		t.Fatalf("expected both managers enrolled, got %+v (err=%v)", statuses, err)
	}

	afterRemove, err := RemoveManager(configPath, mgr2.URL)
	if err != nil || len(afterRemove.EffectiveManagers()) != 1 {
		t.Fatalf("RemoveManager failed: %+v (err=%v)", afterRemove, err)
	}
	if _, err := RemoveManager(configPath, mgr1.URL); err == nil {
		t.Fatal("RemoveManager allowed removing the last remaining manager")
	}
}

func TestSupervisorCapsConsecutiveCrashRestartsAtTen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell test")
	}
	dir := t.TempDir()
	counterFile := filepath.Join(dir, "runs.txt")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sup := newSupervisorWithOptions(ctx, []Command{{
		Name: "crashy",
		Path: "/bin/sh",
		Args: []string{"-c", "printf x >> " + quote(counterFile) + "; exit 2"},
	}}, supervisorOptions{
		minBackoff:   time.Millisecond,
		maxBackoff:   2 * time.Millisecond,
		healthyAfter: time.Hour,
		maxAttempts:  10,
	})

	finished := make(chan error, 1)
	go func() { sup.wait(); finished <- nil }()
	finish(t, finished)

	statuses := sup.status()
	if len(statuses) != 1 || statuses[0].Running || !strings.Contains(statuses[0].Error, "gave up after 10 recovery attempts") {
		t.Fatalf("unexpected supervisor terminal status: %+v", statuses)
	}
	runs, err := os.ReadFile(counterFile)
	if err != nil {
		t.Fatal(err)
	}
	// 1 initial launch + 10 backoff recovery attempts = 11 executions total.
	if len(runs) != 11 {
		t.Fatalf("expected 11 total launches (initial + 10 retries), got %d", len(runs))
	}
}

func TestServiceMonitorExponentialBackoffCapsAtTenAndResetsOnRecovery(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	var xrayUp atomic.Bool
	var startCalls atomic.Int32
	var startRecoversOnCall atomic.Int32

	mon := newServiceMonitor(
		func(_ context.Context, unit string) (bool, string) {
			if unit == "hysteria.service" {
				return true, ""
			}
			if xrayUp.Load() {
				return true, ""
			}
			return false, "systemd unit " + unit + " is not active"
		},
		func(_ context.Context, unit string) error {
			call := startCalls.Add(1)
			if target := startRecoversOnCall.Load(); target > 0 && call == target {
				xrayUp.Store(true)
				return nil
			}
			return errors.New("failed to start")
		},
		2*time.Second,
		5*time.Minute,
	)
	mon.now = func() time.Time { return now }

	svcs := []ExternalService{
		{Name: "hysteria", Unit: "hysteria.service"},
		{Name: "xray", Unit: "xray.service"},
	}

	// Phase 1: xray fails attempt 1, backs off during cooldown, then recovers on attempt 2.
	startRecoversOnCall.Store(2)
	st := mon.check(context.Background(), svcs)
	if st[1].Running || startCalls.Load() != 1 || !strings.Contains(st[1].Error, "attempt 1/10 failed") {
		t.Fatalf("expected attempt 1 failure, got %+v (calls=%d)", st[1], startCalls.Load())
	}
	// 1s later (< 2s backoff): must NOT invoke ServiceStart again yet.
	now = now.Add(time.Second)
	st = mon.check(context.Background(), svcs)
	if st[1].Running || startCalls.Load() != 1 || !strings.Contains(st[1].Error, "backing off") {
		t.Fatalf("expected cooldown backoff without extra start call, got %+v (calls=%d)", st[1], startCalls.Load())
	}
	// At +2s: attempt 2 runs and brings xray back online immediately!
	now = now.Add(time.Second)
	st = mon.check(context.Background(), svcs)
	if !st[1].Running || st[1].Error != "" || startCalls.Load() != 2 {
		t.Fatalf("expected recovery on attempt 2, got %+v (calls=%d)", st[1], startCalls.Load())
	}

	// Phase 2: xray goes down permanently; verify fresh 10-attempt budget and hard cap at 10!
	startCalls.Store(0)
	startRecoversOnCall.Store(0)
	xrayUp.Store(false)

	for i := 1; i <= 10; i++ {
		st = mon.check(context.Background(), svcs)
		if st[1].Running || int(startCalls.Load()) != i {
			t.Fatalf("iteration %d: unexpected status %+v (calls=%d)", i, st[1], startCalls.Load())
		}
		if i < 10 && !strings.Contains(st[1].Error, "failed") {
			t.Fatalf("iteration %d: expected failed message, got %q", i, st[1].Error)
		}
		if i == 10 && !strings.Contains(st[1].Error, "gave up after 10 recovery attempts") {
			t.Fatalf("iteration 10: expected gave up message, got %q", st[1].Error)
		}
		now = now.Add(10 * time.Minute)
	}

	// Further checks hours later must NOT call ServiceStart past 10 attempts while down.
	for range 3 {
		now = now.Add(time.Hour)
		st = mon.check(context.Background(), svcs)
		if st[1].Running || startCalls.Load() != 10 || !strings.Contains(st[1].Error, "gave up after 10 recovery attempts") {
			t.Fatalf("expected hard cap at 10 attempts, got %+v (calls=%d)", st[1], startCalls.Load())
		}
	}

	// Once operator fixes xray manually, status returns to Running and budget resets.
	xrayUp.Store(true)
	st = mon.check(context.Background(), svcs)
	if !st[1].Running || st[1].Error != "" {
		t.Fatalf("expected healthy status after manual fix, got %+v", st[1])
	}
}
