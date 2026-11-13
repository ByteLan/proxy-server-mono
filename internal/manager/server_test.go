package manager

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"proxy-mono/internal/clash"
	"proxy-mono/internal/model"
	"reflect"
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
	mustCode(t, s, "POST", "/api/admin/subscriptions", adminTestToken, model.Subscription{Name: "bad-route", Routes: model.SiteRoutes{Claude: model.RouteGroup{Custom: true, NodeIDs: []string{"missing"}}}, Enabled: true}, 400)
	mustCode(t, s, "POST", "/api/agent/register", enrollTestToken, model.RegisterRequest{ID: "node", Name: "bad", Endpoints: []model.Endpoint{{ID: "a", Type: "hysteria2", Server: "http://wrong", Port: 443, Password: "x"}}}, 400)
}

func TestSiteRoutesPersistenceNodeDeletionAndLegacyMigration(t *testing.T) {
	dir := t.TempDir()
	// Simulate pre-routes database schema to verify automatic migration.
	legacyDB, err := sql.Open("sqlite", filepath.Join(dir, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacyDB.Exec(`CREATE TABLE subscriptions(id TEXT PRIMARY KEY,name TEXT NOT NULL,node_ids TEXT NOT NULL,protocols TEXT NOT NULL,enabled INTEGER NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL);
	INSERT INTO subscriptions VALUES('legacy1','legacy','[]','[]',1,'legacyhash',100,100);`)
	legacyDB.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "master.key"), bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}

	s := testServer(t, dir)
	enroll(t, s, "dirty-1", "192.0.2.10")
	enroll(t, s, "clean-1", "192.0.2.20")
	enroll(t, s, "clean-2", "192.0.2.30")

	subReq := model.Subscription{
		Name:    "split-ai",
		NodeIDs: []string{"dirty-1"},
		Routes: model.SiteRoutes{
			Claude:  model.RouteGroup{Custom: true, NodeIDs: []string{"clean-1"}},
			ChatGPT: model.RouteGroup{Custom: true, NodeIDs: []string{"clean-2"}},
			Google:  model.RouteGroup{Custom: false},
		},
		Enabled: true,
	}
	raw := mustCode(t, s, "POST", "/api/admin/subscriptions", adminTestToken, subReq, 201)
	var created struct {
		Subscription model.Subscription `json:"subscription"`
		URL          string             `json:"url"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	subPath := strings.TrimPrefix(created.URL, "https://manager.example")

	// Deleting clean-1 must NOT cause Claude to fall back to dirty-1; Custom stays true with empty NodeIDs -> REJECT only.
	mustCode(t, s, "DELETE", "/api/admin/nodes/clean-1", adminTestToken, nil, 204)

	yamlBytes := mustCode(t, s, "GET", subPath, "", nil, 200)
	var parsed struct {
		Groups []struct {
			Name    string   `yaml:"name"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal(yamlBytes, &parsed); err != nil {
		t.Fatal(err)
	}
	groups := map[string][]string{}
	for _, g := range parsed.Groups {
		groups[g.Name] = g.Proxies
	}
	if !reflect.DeepEqual(groups[clash.GroupClaude], []string{"REJECT"}) {
		t.Fatalf("deleted clean Claude node fell back to dirty node: %v", groups[clash.GroupClaude])
	}
	if !reflect.DeepEqual(groups[clash.GroupChatGPT], []string{"REJECT", "clean-2 Hysteria2"}) {
		t.Fatalf("unexpected ChatGPT group: %v", groups[clash.GroupChatGPT])
	}
	if !reflect.DeepEqual(groups[clash.GroupGoogle], []string{"REJECT", "dirty-1 Hysteria2"}) {
		t.Fatalf("unexpected Google default group: %v", groups[clash.GroupGoogle])
	}
	if groups[clash.GroupGlobal][0] != clash.GroupGeneral {
		t.Fatalf("GLOBAL first proxy = %s, want %s", groups[clash.GroupGlobal][0], clash.GroupGeneral)
	}
}

func TestRepeatableSubscriptionURLViewingAndDualPortHTTPSubListener(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Config{
		Listen:           "127.0.0.1:0",
		DataDir:          dir,
		AdminToken:       adminTestToken,
		EnrollToken:      enrollTestToken,
		PublicURL:        "https://203.0.113.35:9443",
		HTTPSubListen:    "127.0.0.1:19080",
		HTTPSubPublicURL: "http://203.0.113.35:9080",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Store.Close() })
	if s.SubHTTP == nil {
		t.Fatal("expected SubHTTP listener to be initialized")
	}

	enroll(t, s, "sg01", "203.0.113.35")
	subName := "主力订阅 Daily"
	raw := mustCode(t, s, "POST", "/api/admin/subscriptions", adminTestToken, model.Subscription{
		Name:    subName,
		NodeIDs: []string{"sg01"},
		Enabled: true,
	}, 201)
	var created struct {
		Subscription model.Subscription `json:"subscription"`
		URL          string             `json:"url"`
		HTTPURL      string             `json:"http_url"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.URL, "https://203.0.113.35:9443/sub/") || !strings.HasPrefix(created.HTTPURL, "http://203.0.113.35:9080/sub/") {
		t.Fatalf("unexpected URLs on create: url=%q http_url=%q", created.URL, created.HTTPURL)
	}
	tok := strings.TrimPrefix(created.URL, "https://203.0.113.35:9443/sub/")

	// Encrypted token in SQLite must never store raw subscription token in plaintext.
	var rawEnc []byte
	if err := s.Store.db.QueryRow(`SELECT token_enc FROM subscriptions WHERE id=?`, created.Subscription.ID).Scan(&rawEnc); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawEnc, []byte(tok)) {
		t.Fatal("subscription token stored in plaintext in SQLite")
	}

	// Admin GET /api/admin/subscriptions repeatedly returns both URLs (no burn-after-reading).
	listRaw := mustCode(t, s, "GET", "/api/admin/subscriptions", adminTestToken, nil, 200)
	var listed []model.Subscription
	if err := json.Unmarshal(listRaw, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].URL != created.URL || listed[0].HTTPURL != created.HTTPURL {
		t.Fatalf("subscription list did not persist viewable URLs: %+v", listed)
	}

	// Both main HTTP and SubHTTP serve subscription title headers without inner quotes.
	wantDisp := "attachment; filename*=UTF-8''%E4%B8%BB%E5%8A%9B%E8%AE%A2%E9%98%85%20Daily"
	wantTitle := "base64:" + base64.StdEncoding.EncodeToString([]byte(subName))
	mainSubRes := ut.PerformRequest(s.HTTP.Engine, "GET", "/sub/"+tok, nil)
	if got := mainSubRes.Header().Get("Content-Disposition"); got != wantDisp || strings.Contains(got, `"`) {
		t.Fatalf("main Content-Disposition = %q, want %q", got, wantDisp)
	}
	if got := mainSubRes.Header().Get("profile-title"); got != wantTitle {
		t.Fatalf("main profile-title = %q, want %q", got, wantTitle)
	}

	// SubHTTP serves /healthz and /sub/:token, and strictly rejects /api/admin/* and /api/agent/*.
	subRes := ut.PerformRequest(s.SubHTTP.Engine, "GET", "/sub/"+tok, nil)
	if subRes.Code != 200 || !bytes.Contains(subRes.Result().Body(), []byte("203.0.113.35")) {
		t.Fatalf("SubHTTP /sub/:token failed: code=%d body=%s", subRes.Code, subRes.Result().Body())
	}
	if got := subRes.Header().Get("Content-Disposition"); got != wantDisp || strings.Contains(got, `"`) {
		t.Fatalf("SubHTTP Content-Disposition = %q, want %q", got, wantDisp)
	}
	if got := subRes.Header().Get("profile-title"); got != wantTitle {
		t.Fatalf("SubHTTP profile-title = %q, want %q", got, wantTitle)
	}
	for _, blocked := range []string{"/api/admin/nodes", "/api/admin/subscriptions", "/api/agent/register", "/"} {
		res := ut.PerformRequest(s.SubHTTP.Engine, "GET", blocked, nil, ut.Header{Key: "Authorization", Value: "Bearer " + adminTestToken})
		if res.Code != 404 {
			t.Fatalf("SubHTTP unexpectedly exposed %s (status %d)", blocked, res.Code)
		}
	}

	// Rotating invalidates old URL on SubHTTP and updates the viewable URL in GET /subscriptions.
	rotRaw := mustCode(t, s, "POST", "/api/admin/subscriptions/"+created.Subscription.ID+"/rotate", adminTestToken, nil, 200)
	var rotated struct {
		Subscription model.Subscription `json:"subscription"`
		URL          string             `json:"url"`
		HTTPURL      string             `json:"http_url"`
	}
	if err := json.Unmarshal(rotRaw, &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.HTTPURL == created.HTTPURL || rotated.Subscription.HTTPURL != rotated.HTTPURL {
		t.Fatalf("rotate did not return new HTTPURL: %+v", rotated)
	}
	if ut.PerformRequest(s.SubHTTP.Engine, "GET", "/sub/"+tok, nil).Code != 404 {
		t.Fatal("old subscription token still worked on SubHTTP after rotate")
	}
	newTok := strings.TrimPrefix(rotated.HTTPURL, "http://203.0.113.35:9080/sub/")
	if ut.PerformRequest(s.SubHTTP.Engine, "GET", "/sub/"+newTok, nil).Code != 200 {
		t.Fatal("new subscription token failed on SubHTTP after rotate")
	}
}
