package manager

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/network/standard"
	"io/fs"
	"mime"
	"net/url"
	"path"
	"proxy-mono/internal/clash"
	"proxy-mono/internal/model"
	"proxy-mono/internal/webui"
	"regexp"
	"strings"
	"time"
)

type Config struct {
	Listen, DataDir, AdminToken, EnrollToken, PublicURL, MasterKey string
	TLSCertFile, TLSKeyFile                                        string
}
type Server struct {
	Store  *Store
	Config Config
	HTTP   *server.Hertz
}

func New(c Config) (*Server, error) {
	if len(c.AdminToken) < 24 || len(c.EnrollToken) < 24 {
		return nil, fmt.Errorf("ADMIN_TOKEN and ENROLL_TOKEN must be at least 24 characters")
	}
	if c.AdminToken == c.EnrollToken {
		return nil, fmt.Errorf("admin and enrollment tokens must be different")
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("PUBLIC_URL must be an http(s) origin, e.g. https://proxy.example.com")
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	store, err := Open(c.DataDir, c.MasterKey)
	if err != nil {
		return nil, err
	}
	h := server.New(server.WithHostPorts(c.Listen), server.WithTransport(standard.NewTransporter), server.WithMaxRequestBodySize(128<<10), server.WithReadTimeout(10*time.Second), server.WithWriteTimeout(15*time.Second), server.WithIdleTimeout(60*time.Second))
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		store.Close()
		return nil, fmt.Errorf("TLS_CERT_FILE and TLS_KEY_FILE must be configured together")
	}
	if c.TLSCertFile != "" {
		cert, err := tls.LoadX509KeyPair(c.TLSCertFile, c.TLSKeyFile)
		if err != nil {
			store.Close()
			return nil, fmt.Errorf("load TLS certificate: %w", err)
		}
		h = server.New(server.WithHostPorts(c.Listen), server.WithTransport(standard.NewTransporter), server.WithTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}), server.WithMaxRequestBodySize(128<<10), server.WithReadTimeout(10*time.Second), server.WithWriteTimeout(15*time.Second), server.WithIdleTimeout(60*time.Second))
	}
	s := &Server{store, c, h}
	s.routes()
	return s, nil
}
func bearer(c *app.RequestContext) string {
	auth := string(c.GetHeader("Authorization"))
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}
func equal(a, b string) bool                             { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func fail(c *app.RequestContext, status int, msg string) { c.JSON(status, utils.H{"error": msg}) }
func internalError(c *app.RequestContext)                { fail(c, 500, "internal storage error") }
func bind(c *app.RequestContext, v any) bool {
	if err := json.Unmarshal(c.Request.Body(), v); err != nil {
		fail(c, 400, "invalid JSON body")
		return false
	}
	return true
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

func (s *Server) admin(ctx context.Context, c *app.RequestContext) {
	if !equal(bearer(c), s.Config.AdminToken) {
		fail(c, 401, "invalid admin token")
		c.Abort()
		return
	}
	c.Next(ctx)
}
func (s *Server) routes() {
	h := s.HTTP
	h.Use(func(ctx context.Context, c *app.RequestContext) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Cache-Control", "no-store")
		c.Next(ctx)
	})
	h.GET("/healthz", func(_ context.Context, c *app.RequestContext) { c.JSON(200, utils.H{"ok": true}) })
	admin := h.Group("/api/admin", s.admin)
	admin.GET("/health", func(_ context.Context, c *app.RequestContext) { c.JSON(200, utils.H{"ok": true}) })
	admin.GET("/nodes", func(_ context.Context, c *app.RequestContext) {
		nodes, err := s.Store.nodes(true)
		if err != nil {
			internalError(c)
			return
		}
		c.JSON(200, nodes)
	})
	admin.PATCH("/nodes/:id", s.patchNode)
	admin.DELETE("/nodes/:id", func(_ context.Context, c *app.RequestContext) {
		ok, err := s.Store.deleteNode(c.Param("id"))
		if err != nil {
			internalError(c)
		} else if !ok {
			fail(c, 404, "node not found")
		} else {
			c.Status(204)
		}
	})
	admin.GET("/subscriptions", func(_ context.Context, c *app.RequestContext) {
		subs, err := s.Store.subscriptions()
		if err != nil {
			internalError(c)
			return
		}
		c.JSON(200, subs)
	})
	admin.POST("/subscriptions", s.createSubscription)
	admin.PUT("/subscriptions/:id", s.updateSubscription)
	admin.DELETE("/subscriptions/:id", func(_ context.Context, c *app.RequestContext) {
		ok, err := s.Store.deleteSubscription(c.Param("id"))
		if err != nil {
			internalError(c)
		} else if !ok {
			fail(c, 404, "subscription not found")
		} else {
			c.Status(204)
		}
	})
	admin.POST("/subscriptions/:id/rotate", func(_ context.Context, c *app.RequestContext) {
		token, err := s.Store.rotateSubscription(c.Param("id"))
		if errors.Is(err, sql.ErrNoRows) {
			fail(c, 404, "subscription not found")
		} else if err != nil {
			internalError(c)
		} else {
			c.JSON(200, utils.H{"url": s.subURL(token)})
		}
	})
	admin.GET("/subscriptions/:id/preview", func(_ context.Context, c *app.RequestContext) {
		p, err := s.Store.subscription(c.Param("id"))
		if errors.Is(err, sql.ErrNoRows) {
			fail(c, 404, "subscription not found")
		} else if err != nil {
			internalError(c)
		} else {
			s.writeYAML(c, p)
		}
	})
	h.POST("/api/agent/register", s.register)
	h.POST("/api/agent/heartbeat", s.heartbeat)
	h.GET("/sub/:token", func(_ context.Context, c *app.RequestContext) {
		p, err := s.Store.subscriptionByToken(c.Param("token"))
		if errors.Is(err, sql.ErrNoRows) {
			fail(c, 404, "subscription not found")
		} else if err != nil {
			internalError(c)
		} else {
			s.writeYAML(c, p)
		}
	})
	h.NoRoute(func(_ context.Context, c *app.RequestContext) {
		requestPath := string(c.Path())
		if c.Method() == nil || string(c.Method()) != "GET" || strings.HasPrefix(requestPath, "/api/") || strings.HasPrefix(requestPath, "/sub/") {
			fail(c, 404, "not found")
			return
		}
		name := strings.TrimPrefix(path.Clean(requestPath), "/")
		if name == "." || name == "" {
			name = "index.html"
		}
		raw, err := fs.ReadFile(webui.Files, "dist/"+name)
		if err != nil {
			if path.Ext(name) != "" {
				fail(c, 404, "not found")
				return
			}
			name = "index.html"
			raw, err = fs.ReadFile(webui.Files, "dist/index.html")
		}
		if err != nil {
			fail(c, 404, "frontend not built")
			return
		}
		ct := mime.TypeByExtension(path.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}
		c.Data(200, ct, raw)
	})
}
func (s *Server) register(_ context.Context, c *app.RequestContext) {
	if !equal(bearer(c), s.Config.EnrollToken) {
		fail(c, 401, "invalid enrollment token")
		return
	}
	var r model.RegisterRequest
	if !bind(c, &r) {
		return
	}
	if !validID.MatchString(r.ID) || strings.TrimSpace(r.Name) == "" || len(r.Name) > 120 || len(r.Version) > 80 || len(r.OS) > 40 || len(r.Arch) > 40 {
		fail(c, 400, "invalid node metadata")
		return
	}
	for _, e := range r.Endpoints {
		if !validID.MatchString(e.ID) {
			fail(c, 400, "invalid endpoint ID")
			return
		}
	}
	if err := clash.Validate(r.Endpoints); err != nil {
		fail(c, 400, err.Error())
		return
	}
	var exists int
	if err := s.Store.db.QueryRow(`SELECT count(*) FROM (SELECT id FROM nodes WHERE id=? UNION SELECT id FROM revoked_nodes WHERE id=?)`, r.ID, r.ID).Scan(&exists); err != nil {
		internalError(c)
		return
	}
	if exists > 0 {
		fail(c, 409, "node identity already enrolled")
		return
	}
	token, err := s.Store.register(r)
	if err != nil {
		internalError(c)
		return
	}
	c.JSON(201, utils.H{"token": token})
}
func (s *Server) heartbeat(_ context.Context, c *app.RequestContext) {
	var r model.HeartbeatRequest
	if !bind(c, &r) {
		return
	}
	if !s.Store.nodeAuthorized(r.ID, bearer(c)) {
		fail(c, 401, "invalid node token")
		return
	}
	if len(r.ConfigHash) > 128 || len(r.Version) > 80 || len(r.Cores) > 8 {
		fail(c, 400, "invalid heartbeat metadata")
		return
	}
	if r.Endpoints != nil {
		for _, e := range *r.Endpoints {
			if !validID.MatchString(e.ID) {
				fail(c, 400, "invalid endpoint ID")
				return
			}
		}
		if err := clash.Validate(*r.Endpoints); err != nil {
			fail(c, 400, err.Error())
			return
		}
	}
	if err := s.Store.heartbeat(r); err != nil {
		internalError(c)
		return
	}
	c.JSON(200, utils.H{"ok": true})
}
func (s *Server) patchNode(_ context.Context, c *app.RequestContext) {
	var r struct {
		Name    string `json:"name"`
		Enabled *bool  `json:"enabled"`
	}
	if !bind(c, &r) {
		return
	}
	if r.Enabled == nil || strings.TrimSpace(r.Name) == "" || len(r.Name) > 120 {
		fail(c, 400, "name and enabled required")
		return
	}
	ok, err := s.Store.patchNode(c.Param("id"), r.Name, *r.Enabled)
	if err != nil {
		internalError(c)
		return
	}
	if !ok {
		fail(c, 404, "node not found")
		return
	}
	nodes, err := s.Store.nodes(true)
	if err != nil {
		internalError(c)
		return
	}
	for _, n := range nodes {
		if n.ID == c.Param("id") {
			c.JSON(200, n)
			return
		}
	}
	fail(c, 404, "node not found")
}
func (s *Server) validSubscription(c *app.RequestContext, p *model.Subscription) bool {
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 120 || len(p.NodeIDs) > 1000 {
		fail(c, 400, "invalid subscription name or selection")
		return false
	}
	if p.NodeIDs == nil {
		p.NodeIDs = []string{}
	}
	if p.Protocols == nil {
		p.Protocols = []string{}
	}
	known := map[string]bool{"hysteria2": true, "vless": true, "socks5": true, "http": true, "https": true}
	for _, v := range p.Protocols {
		if !known[v] {
			fail(c, 400, "unsupported protocol")
			return false
		}
	}
	nodes, err := s.Store.nodes(true)
	if err != nil {
		internalError(c)
		return false
	}
	ids := map[string]bool{}
	for _, n := range nodes {
		ids[n.ID] = true
	}
	seen := map[string]bool{}
	for _, id := range p.NodeIDs {
		if !ids[id] || seen[id] {
			fail(c, 400, "node selection contains missing or duplicate IDs")
			return false
		}
		seen[id] = true
	}
	return true
}
func (s *Server) subURL(token string) string { return s.Config.PublicURL + "/sub/" + token }
func (s *Server) createSubscription(_ context.Context, c *app.RequestContext) {
	var p model.Subscription
	if !bind(c, &p) || !s.validSubscription(c, &p) {
		return
	}
	p, token, err := s.Store.createSubscription(p)
	if err != nil {
		internalError(c)
		return
	}
	c.JSON(201, utils.H{"subscription": p, "url": s.subURL(token)})
}
func (s *Server) updateSubscription(_ context.Context, c *app.RequestContext) {
	var p model.Subscription
	if !bind(c, &p) || !s.validSubscription(c, &p) {
		return
	}
	p, err := s.Store.updateSubscription(c.Param("id"), p)
	if errors.Is(err, sql.ErrNoRows) {
		fail(c, 404, "subscription not found")
	} else if err != nil {
		internalError(c)
	} else {
		c.JSON(200, p)
	}
}
func (s *Server) writeYAML(c *app.RequestContext, p model.Subscription) {
	nodes, err := s.Store.nodes(false)
	if err != nil {
		internalError(c)
		return
	}
	ids := map[string]bool{}
	for _, id := range p.NodeIDs {
		ids[id] = true
	}
	selected := []model.Node{}
	for _, n := range nodes {
		if ids[n.ID] {
			selected = append(selected, n)
		}
	}
	raw, err := clash.Render(selected, p.Protocols)
	if err != nil {
		fail(c, 500, "configuration rendering failed")
		return
	}
	c.Header("profile-update-interval", "1")
	c.Header("Content-Disposition", `inline; filename="proxy.yaml"`)
	c.Data(200, "text/yaml; charset=utf-8", raw)
}
