// Package agent enrolls and supervises proxy nodes. Server private keys never
// enter the control-plane request types.
package agent

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"proxy-mono/internal/model"
)

const Version = "0.1.0"

type Command struct {
	Name string   `json:"name"`
	Path string   `json:"path"`
	Args []string `json:"args"`
}

type Config struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	Manager           string           `json:"manager_url"`
	CAFile            string           `json:"ca_file,omitempty"`
	AllowInsecureHTTP bool             `json:"allow_insecure_http,omitempty"`
	EnrollToken       string           `json:"enroll_token,omitempty"`
	TokenFile         string           `json:"token_file"`
	Endpoints         []model.Endpoint `json:"endpoints"`
	Commands          []Command        `json:"commands"`
}

// InitOptions describes a new node. Core binaries are installed separately and
// deliberately never downloaded from an unpinned "latest" release by the agent.
type InitOptions struct {
	ConfigPath        string
	Server            string
	Name              string
	Manager           string
	CAFile            string
	AllowInsecureHTTP bool
	EnrollToken       string
	HY2Port           int
	VLESSPort         int
	HysteriaBin       string
	XrayBin           string
}

func LoadConfig(path string) (Config, error) {
	var cfg Config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read agent config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("decode agent config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.ID == "" || c.Name == "" {
		return errors.New("config id and name are required")
	}
	u, err := url.Parse(c.Manager)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("manager must be an http(s) origin without credentials, path, query or fragment")
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if !c.AllowInsecureHTTP || host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("manager requires HTTPS; HTTP is only allowed for loopback development with allow_insecure_http")
		}
	}
	if !filepath.IsAbs(c.TokenFile) {
		return errors.New("token_file must be an absolute path")
	}
	seen := make(map[string]bool)
	for _, endpoint := range c.Endpoints {
		if endpoint.ID == "" || seen[endpoint.ID] {
			return errors.New("endpoint ids must be nonempty and unique")
		}
		seen[endpoint.ID] = true
		if err := validateHost(endpoint.Server); err != nil {
			return err
		}
		if endpoint.Port < 1 || endpoint.Port > 65535 {
			return errors.New("endpoint port must be between 1 and 65535")
		}
		if endpoint.Type != "hysteria2" && endpoint.Type != "vless" && endpoint.Type != "socks5" && endpoint.Type != "http" && endpoint.Type != "https" {
			return errors.New("unsupported endpoint type")
		}
	}
	seen = make(map[string]bool)
	for _, command := range c.Commands {
		if command.Name == "" || command.Path == "" || seen[command.Name] {
			return errors.New("core commands require a unique name and executable path")
		}
		seen[command.Name] = true
	}
	return nil
}

func validateHost(host string) error {
	if host == "" || strings.ContainsAny(host, "/\\\r\n\t @?#") || strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return errors.New("server must be a public hostname or unbracketed IP, without scheme or port")
	}
	return nil
}

// Init generates client credentials and node-local server configs. Existing
// configuration is never overwritten, including on a repeated installation.
func Init(options InitOptions) (Config, error) {
	var cfg Config
	if err := validateHost(options.Server); err != nil {
		return cfg, err
	}
	if options.ConfigPath == "" {
		return cfg, errors.New("config path is required")
	}
	if options.Name == "" {
		return cfg, errors.New("node name is required")
	}
	if options.HY2Port == 0 {
		options.HY2Port = 443
	}
	if options.VLESSPort == 0 {
		options.VLESSPort = 443
	}
	if options.HY2Port < 1 || options.HY2Port > 65535 || options.VLESSPort < 1 || options.VLESSPort > 65535 {
		return cfg, errors.New("ports must be between 1 and 65535")
	}
	if options.HysteriaBin == "" {
		options.HysteriaBin = "hysteria"
	}
	if options.XrayBin == "" {
		options.XrayBin = "xray"
	}
	configPath, err := filepath.Abs(options.ConfigPath)
	if err != nil {
		return cfg, err
	}
	if _, err = os.Lstat(configPath); err == nil {
		return cfg, errors.New("config already exists; use run to preserve its identity")
	} else if !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	statePath := strings.TrimSuffix(configPath, filepath.Ext(configPath)) + ".d"
	id, err := randomHex(16)
	if err != nil {
		return cfg, err
	}
	password, err := randomHex(32)
	if err != nil {
		return cfg, err
	}
	uuid, err := newUUID()
	if err != nil {
		return cfg, err
	}
	shortID, err := randomHex(8)
	if err != nil {
		return cfg, err
	}
	realityKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return cfg, err
	}
	cert, privateKey, fingerprint, err := selfSigned(options.Server)
	if err != nil {
		return cfg, err
	}
	cfg = Config{
		ID: "node_" + id, Name: options.Name, Manager: strings.TrimRight(options.Manager, "/"),
		EnrollToken: options.EnrollToken, CAFile: options.CAFile, AllowInsecureHTTP: options.AllowInsecureHTTP, TokenFile: filepath.Join(statePath, "agent.token"),
		Endpoints: []model.Endpoint{
			{ID: "hy2_" + id, Name: options.Name + " HY2", Type: "hysteria2", Server: options.Server, Port: options.HY2Port, Password: password, SNI: options.Server, Fingerprint: fingerprint, TLS: true, UDP: true},
			{ID: "vless_" + id, Name: options.Name + " REALITY", Type: "vless", Server: options.Server, Port: options.VLESSPort, UUID: uuid, SNI: "www.microsoft.com", PublicKey: base64.RawURLEncoding.EncodeToString(realityKey.PublicKey().Bytes()), ShortID: shortID, TLS: true, UDP: true},
		},
		Commands: []Command{
			{Name: "hysteria", Path: options.HysteriaBin, Args: []string{"server", "-c", filepath.Join(statePath, "hysteria.yaml")}},
			{Name: "xray", Path: options.XrayBin, Args: []string{"run", "-c", filepath.Join(statePath, "xray.json")}},
		},
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return Config{}, err
	}
	// Refuse an existing state directory: it may hold credentials from a prior
	// initialization whose final config write did not finish.
	if err := os.Mkdir(statePath, 0700); err != nil {
		return Config{}, fmt.Errorf("create node state directory: %w", err)
	}
	hysteria := fmt.Sprintf("listen: \":%d\"\ntls:\n  cert: %s\n  key: %s\nauth:\n  type: password\n  password: %s\n", options.HY2Port, quote(filepath.Join(statePath, "server.crt")), quote(filepath.Join(statePath, "server.key")), quote(password))
	xray := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": "vless-reality", "listen": "0.0.0.0", "port": options.VLESSPort, "protocol": "vless",
			"settings": map[string]any{"clients": []any{map[string]any{"id": uuid, "flow": "xtls-rprx-vision"}}, "decryption": "none"},
			"streamSettings": map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{
				"show": false, "dest": "www.microsoft.com:443", "xver": 0, "serverNames": []string{"www.microsoft.com"},
				"privateKey": base64.RawURLEncoding.EncodeToString(realityKey.Bytes()), "shortIds": []string{shortID},
			}},
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
	}
	xrayJSON, err := json.MarshalIndent(xray, "", "  ")
	if err != nil {
		return Config{}, err
	}
	for _, item := range []struct {
		path string
		data []byte
	}{
		{filepath.Join(statePath, "server.crt"), cert}, {filepath.Join(statePath, "server.key"), privateKey},
		{filepath.Join(statePath, "hysteria.yaml"), []byte(hysteria)}, {filepath.Join(statePath, "xray.json"), xrayJSON},
	} {
		if err := atomicWrite(item.path, item.data); err != nil {
			return Config{}, err
		}
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return Config{}, err
	}
	if err := atomicWrite(configPath, append(b, '\n')); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func selfSigned(host string) ([]byte, []byte, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, "", err
	}
	now := time.Now()
	template := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: host}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(2, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, "", err
	}
	fingerprint := sha256.Sum256(der)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), hex.EncodeToString(fingerprint[:]), nil
}

func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".proxy-tmp-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
