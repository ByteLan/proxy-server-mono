package manager

import (
	"bytes"
	"encoding/json"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"os"
	"path/filepath"
	"proxy-mono/internal/model"
	"strings"
	"testing"
)

const adminTestToken = "admin-very-long-test-credential-123456789"
const enrollTestToken = "enroll-very-long-test-credential-123456789"

func testServer(t *testing.T, dir string) *Server {
	t.Helper()
	s, err := New(Config{Listen: "127.0.0.1:0", DataDir: dir, AdminToken: adminTestToken, EnrollToken: enrollTestToken, PublicURL: "https://manager.example"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Store.Close() })
	return s
}
func request(s *Server, method, path, token string, body any) (int, []byte) {
	var b *ut.Body
	if body != nil {
		raw, _ := json.Marshal(body)
		b = &ut.Body{Body: bytes.NewReader(raw), Len: len(raw)}
	}
	r := ut.PerformRequest(s.HTTP.Engine, method, path, b, ut.Header{Key: "Authorization", Value: "Bearer " + token}, ut.Header{Key: "Content-Type", Value: "application/json"})
	out := r.Result().Body()
	return r.Code, out
}
func mustCode(t *testing.T, s *Server, method, path, token string, body any, want int) []byte {
	t.Helper()
	status, raw := request(s, method, path, token, body)
	if status != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, status, want, raw)
	}
	return raw
}
func enroll(t *testing.T, s *Server, id, host string) string {
	t.Helper()
	r := model.RegisterRequest{ID: id, Name: id, OS: "linux", Arch: "amd64", Version: "dev", Endpoints: []model.Endpoint{{ID: "hy2", Name: "HY2", Type: "hysteria2", Server: host, Port: 443, Password: "super-secret-test-password", SNI: "example.com", Fingerprint: strings.Repeat("ab", 32)}}}
	raw := mustCode(t, s, "POST", "/api/agent/register", enrollTestToken, r, 201)
	var token struct {
		Token string `json:"token"`
	}
	json.Unmarshal(raw, &token)
	if token.Token == "" {
		t.Fatal("missing node token")
	}
	return token.Token
}
func TestDynamicSubscriptionsAndCredentials(t *testing.T) {
	dir := t.TempDir()
	s := testServer(t, dir)
	mustCode(t, s, "GET", "/api/admin/nodes", "wrong", nil, 401)
	token1 := enroll(t, s, "node-1", "192.0.2.1")
	token2 := enroll(t, s, "node-2", "192.0.2.2")
	// Enrollment credentials cannot replace an existing node identity.
	mustCode(t, s, "POST", "/api/agent/register", enrollTestToken, model.RegisterRequest{ID: "node-1", Name: "attack"}, 409)
	mustCode(t, s, "POST", "/api/agent/heartbeat", token1, model.HeartbeatRequest{ID: "node-2"}, 401)
	mustCode(t, s, "POST", "/api/agent/heartbeat", token1, model.HeartbeatRequest{ID: "node-1", Version: "test", ConfigHash: "a", Cores: []model.CoreStatus{{Name: "hysteria2", Running: true}}}, 200)
	raw := mustCode(t, s, "GET", "/api/admin/nodes", adminTestToken, nil, 200)
	if bytes.Contains(raw, []byte("super-secret")) {
		t.Fatal("node list leaked credential")
	}
	p := model.Subscription{Name: "work", NodeIDs: []string{"node-1"}, Protocols: []string{"hysteria2"}, Enabled: true}
	raw = mustCode(t, s, "POST", "/api/admin/subscriptions", adminTestToken, p, 201)
	var created struct {
		Subscription model.Subscription `json:"subscription"`
		URL          string             `json:"url"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	subPath := strings.TrimPrefix(created.URL, "https://manager.example")
	raw = mustCode(t, s, "GET", subPath, "", nil, 200)
	if !bytes.Contains(raw, []byte("192.0.2.1")) || bytes.Contains(raw, []byte("192.0.2.2")) {
		t.Fatal("wrong initial nodes")
	}
	// Same URL picks up add and remove operations.
	p.NodeIDs = []string{"node-2"}
	mustCode(t, s, "PUT", "/api/admin/subscriptions/"+created.Subscription.ID, adminTestToken, p, 200)
	raw = mustCode(t, s, "GET", subPath, "", nil, 200)
	if bytes.Contains(raw, []byte("192.0.2.1")) || !bytes.Contains(raw, []byte("192.0.2.2")) {
		t.Fatal("URL did not reflect edited membership")
	}
	// Persisted encrypted endpoint material and subscription survive manager restart.
	if err := s.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s = testServer(t, dir)
	mustCode(t, s, "GET", subPath, "", nil, 200)
	var enc []byte
	if err := s.Store.db.QueryRow(`SELECT endpoints FROM nodes LIMIT 1`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, []byte("super-secret")) {
		t.Fatal("plaintext credential in storage")
	}
	mustCode(t, s, "PATCH", "/api/admin/nodes/node-2", adminTestToken, map[string]any{"name": "node-2", "enabled": false}, 200)
	raw = mustCode(t, s, "GET", subPath, "", nil, 200)
	if bytes.Contains(raw, []byte("192.0.2.2")) {
		t.Fatal("disabled node still exported")
	}
	mustCode(t, s, "DELETE", "/api/admin/nodes/node-2", adminTestToken, nil, 204)
	mustCode(t, s, "POST", "/api/agent/register", enrollTestToken, model.RegisterRequest{ID: "node-2", Name: "revoked"}, 409)
	mustCode(t, s, "POST", "/api/agent/heartbeat", token2, model.HeartbeatRequest{ID: "node-2"}, 401)
	raw = mustCode(t, s, "GET", subPath, "", nil, 200)
	if !bytes.Contains(raw, []byte("DIRECT")) {
		t.Fatal("empty subscription invalid")
	}
	raw = mustCode(t, s, "POST", "/api/admin/subscriptions/"+created.Subscription.ID+"/rotate", adminTestToken, nil, 200)
	mustCode(t, s, "GET", subPath, "", nil, 404)
	var rotated struct {
		URL string `json:"url"`
	}
	json.Unmarshal(raw, &rotated)
	subPath = strings.TrimPrefix(rotated.URL, "https://manager.example")
	mustCode(t, s, "GET", subPath, "", nil, 200)
	mustCode(t, s, "DELETE", "/api/admin/subscriptions/"+created.Subscription.ID, adminTestToken, nil, 204)
	mustCode(t, s, "GET", subPath, "", nil, 404)
}
func TestMissingKeyDoesNotOverwriteDatabase(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err = os.Remove(filepath.Join(dir, "master.key")); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(dir, ""); err == nil {
		t.Fatal("silently recreated lost encryption key")
	}
}
func TestInvalidSelectionAndEndpoints(t *testing.T) {
	s := testServer(t, t.TempDir())
	mustCode(t, s, "POST", "/api/admin/subscriptions", adminTestToken, model.Subscription{Name: "bad", NodeIDs: []string{"missing"}, Enabled: true}, 400)
	mustCode(t, s, "POST", "/api/agent/register", enrollTestToken, model.RegisterRequest{ID: "node", Name: "bad", Endpoints: []model.Endpoint{{ID: "a", Type: "hysteria2", Server: "http://wrong", Port: 443, Password: "x"}}}, 400)
}
