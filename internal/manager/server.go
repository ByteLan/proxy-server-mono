package manager

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/network/standard"
	"io/fs"
	"mime"
	"net/url"
	"os"
	"path"
	"proxy-mono/internal/clash"
	"proxy-mono/internal/model"
	"proxy-mono/internal/version"
	"proxy-mono/internal/webui"
	"regexp"
	"strings"
	"time"
)

type Config struct {
	Listen, DataDir, AdminToken, EnrollToken, PublicURL, MasterKey string
	TLSCertFile, TLSKeyFile, TLSCAFile                             string
	HTTPSubListen, HTTPSubPublicURL                                string
}
type Server struct {
	Store       *Store
	Config      Config
	HTTP        *server.Hertz
	SubHTTP     *server.Hertz
	publicCAPEM []byte
}

func validateOriginURL(raw, name string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("%s must be an http(s) origin, e.g. https://proxy.example.com", name)
	}
	return strings.TrimRight(raw, "/"), nil
}

func loadPublicCAPEM(file string) ([]byte, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read TLS_CA_FILE: %w", err)
	}
	var out bytes.Buffer
	rest := raw
	count := 0
	for len(bytes.TrimSpace(rest)) > 0 {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			return nil, fmt.Errorf("TLS_CA_FILE contains invalid PEM data")
		}
		if strings.Contains(blk.Type, "PRIVATE") {
			return nil, fmt.Errorf("TLS_CA_FILE must not contain private keys")
		}
		if blk.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("TLS_CA_FILE must contain only CERTIFICATE blocks")
		}
		if _, err := x509.ParseCertificate(blk.Bytes); err != nil {
			return nil, fmt.Errorf("parse TLS_CA_FILE certificate: %w", err)
		}
		_ = pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: blk.Bytes})
		count++
	}
	if count == 0 {
		return nil, fmt.Errorf("TLS_CA_FILE contains no certificates")
	}
	return out.Bytes(), nil
}

func New(c Config) (*Server, error) {
	if len(c.AdminToken) < 24 || len(c.EnrollToken) < 24 {
		return nil, fmt.Errorf("ADMIN_TOKEN and ENROLL_TOKEN must be at least 24 characters")
	}
	if c.AdminToken == c.EnrollToken {
		return nil, fmt.Errorf("admin and enrollment tokens must be different")
	}
	pubURL, err := validateOriginURL(c.PublicURL, "PUBLIC_URL")
	if err != nil {
		return nil, err
	}
	c.PublicURL = pubURL
	if (c.HTTPSubListen == "") != (c.HTTPSubPublicURL == "") {
		return nil, fmt.Errorf("HTTP_SUB_LISTEN and HTTP_SUB_PUBLIC_URL must be configured together")
	}
	if c.HTTPSubPublicURL != "" {
		httpSubURL, err := validateOriginURL(c.HTTPSubPublicURL, "HTTP_SUB_PUBLIC_URL")
		if err != nil {
			return nil, err
		}
		if c.HTTPSubListen == c.Listen {
			return nil, fmt.Errorf("HTTP_SUB_LISTEN must differ from LISTEN")
		}
		c.HTTPSubPublicURL = httpSubURL
	}
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
	var publicCAPEM []byte
	if c.TLSCAFile != "" {
		caBytes, caErr := loadPublicCAPEM(c.TLSCAFile)
		if caErr != nil {
			store.Close()
			return nil, caErr
		}
		publicCAPEM = caBytes
	}
	var subH *server.Hertz
	if c.HTTPSubListen != "" {
		subH = server.New(server.WithHostPorts(c.HTTPSubListen), server.WithTransport(standard.NewTransporter), server.WithMaxRequestBodySize(4<<10), server.WithReadTimeout(10*time.Second), server.WithWriteTimeout(15*time.Second), server.WithIdleTimeout(60*time.Second))
	}
	s := &Server{Store: store, Config: c, HTTP: h, SubHTTP: subH, publicCAPEM: publicCAPEM}
	s.routes()
	if s.SubHTTP != nil {
		s.subOnlyRoutes()
	}
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
	h.GET("/api/public/meta", func(_ context.Context, c *app.RequestContext) {
		c.JSON(200, utils.H{"ca_cert_available": len(s.publicCAPEM) > 0})
	})
	h.GET("/ca.crt", func(_ context.Context, c *app.RequestContext) {
		if len(s.publicCAPEM) == 0 {
			fail(c, 404, "CA certificate not configured")
			return
		}
		c.Header("Content-Disposition", `attachment; filename="proxy-console-ca.crt"`)
		c.Data(200, "application/x-x509-ca-cert", s.publicCAPEM)
	})
	admin := h.Group("/api/admin", s.admin)
	admin.GET("/health", func(_ context.Context, c *app.RequestContext) { c.JSON(200, utils.H{"ok": true}) })
	admin.GET("/onboarding", func(_ context.Context, c *app.RequestContext) {
		c.JSON(200, utils.H{
			"public_url":          s.Config.PublicURL,
			"http_sub_public_url": s.Config.HTTPSubPublicURL,
			"enroll_token":        s.Config.EnrollToken,
			"ca_cert_available":   len(s.publicCAPEM) > 0,
			"ca_cert_pem":         string(s.publicCAPEM),
			"version":             version.Version,
			"default_repo":        "ByteLan/proxy-server-mono",
		})
	})
	admin.GET("/nodes", func(_ context.Context, c *app.RequestContext) {
		nodes, err := s.Store.nodes(true)
		if err != nil {
			internalError(c)
			return
		}
		c.JSON(200, nodes)
	})
	admin.PATCH("/nodes/:id", s.patchNode)
	admin.POST("/nodes/:id/block", func(_ context.Context, c *app.RequestContext) {
		blocked, ok, err := s.Store.blockNode(c.Param("id"))
		if err != nil {
			internalError(c)
		} else if !ok {
			fail(c, 404, "node not found")
		} else {
			c.JSON(200, blocked)
		}
	})
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
	admin.GET("/blocked-nodes", func(_ context.Context, c *app.RequestContext) {
		blocked, err := s.Store.blockedNodes()
		if err != nil {
			internalError(c)
			return
		}
		c.JSON(200, blocked)
	})
	admin.DELETE("/blocked-nodes/:id", func(_ context.Context, c *app.RequestContext) {
		ok, err := s.Store.unblockNode(c.Param("id"))
		if err != nil {
			internalError(c)
		} else if !ok {
			fail(c, 404, "blocked node not found")
		} else {
			c.Status(204)
		}
	})
	admin.GET("/subscriptions", func(_ context.Context, c *app.RequestContext) {
		subs, tokens, err := s.Store.subscriptions()
		if err != nil {
			internalError(c)
			return
		}
		for i := range subs {
			s.populateSubURLs(&subs[i], tokens[i])
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
		p, token, err := s.Store.rotateSubscription(c.Param("id"))
		if errors.Is(err, sql.ErrNoRows) {
			fail(c, 404, "subscription not found")
		} else if err != nil {
			internalError(c)
		} else {
			s.populateSubURLs(&p, token)
			resp := utils.H{"subscription": p, "url": p.URL}
			if p.HTTPURL != "" {
				resp["http_url"] = p.HTTPURL
			}
			c.JSON(200, resp)
		}
	})
	admin.GET("/subscriptions/:id/preview", func(_ context.Context, c *app.RequestContext) {
		p, _, err := s.Store.subscription(c.Param("id"))
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
	h.GET("/sub/:token", s.serveSubscriptionByToken)
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
	blocked, enrolled, err := s.Store.nodeEnrollConflict(r.ID)
	if err != nil {
		internalError(c)
		return
	}
	if blocked {
		fail(c, 409, "node identity is blocked")
		return
	}
	if enrolled {
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
	if !validID.MatchString(r.ID) {
		fail(c, 401, "invalid node token")
		return
	}
	auth, err := s.Store.checkNodeAuth(r.ID, bearer(c))
	if err != nil {
		internalError(c)
		return
	}
	switch auth {
	case nodeAuthDenied:
		fail(c, 401, "invalid node token")
		return
	case nodeAuthDeleted:
		fail(c, 410, "node deleted; re-enrollment required")
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
	checkNodeSet := func(list []string, label string) bool {
		if len(list) > 1000 {
			fail(c, 400, "invalid "+label+" selection")
			return false
		}
		seen := map[string]bool{}
		for _, id := range list {
			if !ids[id] || seen[id] {
				fail(c, 400, label+" selection contains missing or duplicate IDs")
				return false
			}
			seen[id] = true
		}
		return true
	}
	if !checkNodeSet(p.NodeIDs, "node") {
		return false
	}
	for _, item := range []struct {
		group *model.RouteGroup
		label string
	}{
		{&p.Routes.Claude, "claude route"},
		{&p.Routes.ChatGPT, "chatgpt route"},
		{&p.Routes.Google, "google route"},
	} {
		if !item.group.Custom {
			item.group.NodeIDs = []string{}
			continue
		}
		if item.group.NodeIDs == nil {
			item.group.NodeIDs = []string{}
		}
		if !checkNodeSet(item.group.NodeIDs, item.label) {
			return false
		}
	}
	return true
}
func (s *Server) serveSubscriptionByToken(_ context.Context, c *app.RequestContext) {
	p, err := s.Store.subscriptionByToken(c.Param("token"))
	if errors.Is(err, sql.ErrNoRows) {
		fail(c, 404, "subscription not found")
	} else if err != nil {
		internalError(c)
	} else {
		s.writeYAML(c, p)
	}
}
func (s *Server) subOnlyRoutes() {
	h := s.SubHTTP
	h.Use(func(ctx context.Context, c *app.RequestContext) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Cache-Control", "no-store")
		c.Next(ctx)
	})
	h.GET("/healthz", func(_ context.Context, c *app.RequestContext) { c.JSON(200, utils.H{"ok": true}) })
	h.GET("/sub/:token", s.serveSubscriptionByToken)
	h.NoRoute(func(_ context.Context, c *app.RequestContext) { fail(c, 404, "not found") })
}
func (s *Server) subURL(token string) string { return s.Config.PublicURL + "/sub/" + token }
func (s *Server) httpSubURL(token string) string {
	if s.Config.HTTPSubPublicURL == "" {
		return ""
	}
	return s.Config.HTTPSubPublicURL + "/sub/" + token
}
func (s *Server) populateSubURLs(p *model.Subscription, token string) {
	if token == "" {
		return
	}
	p.URL = s.subURL(token)
	p.HTTPURL = s.httpSubURL(token)
}
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
	s.populateSubURLs(&p, token)
	resp := utils.H{"subscription": p, "url": p.URL}
	if p.HTTPURL != "" {
		resp["http_url"] = p.HTTPURL
	}
	c.JSON(201, resp)
}
func (s *Server) updateSubscription(_ context.Context, c *app.RequestContext) {
	var p model.Subscription
	if !bind(c, &p) || !s.validSubscription(c, &p) {
		return
	}
	p, token, err := s.Store.updateSubscription(c.Param("id"), p)
	if errors.Is(err, sql.ErrNoRows) {
		fail(c, 404, "subscription not found")
	} else if err != nil {
		internalError(c)
	} else {
		s.populateSubURLs(&p, token)
		c.JSON(200, p)
	}
}
func encodeRFC5987(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') ||
			ch == '!' || ch == '#' || ch == '$' || ch == '&' || ch == '+' || ch == '-' ||
			ch == '.' || ch == '^' || ch == '_' || ch == '`' || ch == '|' || ch == '~' {
			b.WriteByte(ch)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[ch>>4])
			b.WriteByte(hex[ch&0x0f])
		}
	}
	return b.String()
}
func (s *Server) writeYAML(c *app.RequestContext, p model.Subscription) {
	nodes, err := s.Store.nodes(false)
	if err != nil {
		internalError(c)
		return
	}
	raw, err := clash.RenderSubscription(nodes, p)
	if err != nil {
		fail(c, 500, "configuration rendering failed")
		return
	}
	title := strings.TrimSpace(p.Name)
	if title == "" {
		title = "proxy"
	}
	c.Header("profile-update-interval", "1")
	c.Header("profile-title", "base64:"+base64.StdEncoding.EncodeToString([]byte(title)))
	c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+encodeRFC5987(title))
	c.Data(200, "text/yaml; charset=utf-8", raw)
}
