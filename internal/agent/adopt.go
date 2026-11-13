package agent

import (
	"crypto/ecdh"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"net"
	"os"
	"path/filepath"
	"proxy-mono/internal/clash"
	"proxy-mono/internal/model"
	"strconv"
	"strings"
)

const (
	DefaultHysteriaConfig = "/etc/hysteria/config.yaml"
	DefaultHysteriaUnit   = "hysteria.service"
	DefaultXrayConfig     = "/etc/xray/config.json"
	DefaultXrayUnit       = "xray.service"
)

// AdoptOptions imports client connection material from proxy cores already
// installed and managed by systemd on the host (companion mode: no core
// binaries are reinstalled and no duplicate child processes are spawned).
type AdoptOptions struct {
	ConfigPath        string
	Server            string
	Name              string
	Manager           string
	CAFile            string
	AllowInsecureHTTP bool
	EnrollToken       string
	HysteriaConfig    string
	HysteriaUnit      string
	XrayConfig        string
	XrayUnit          string
}

func Adopt(options AdoptOptions) (Config, error) {
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
	if _, err = os.Lstat(statePath); err == nil {
		return cfg, errors.New("node state directory already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}

	hyUnit := options.HysteriaUnit
	if hyUnit == "" {
		hyUnit = DefaultHysteriaUnit
	}
	xrayUnit := options.XrayUnit
	if xrayUnit == "" {
		xrayUnit = DefaultXrayUnit
	}

	id, err := randomHex(16)
	if err != nil {
		return cfg, err
	}

	var endpoints []model.Endpoint
	var services []ExternalService

	hyPath, hyExplicit := options.HysteriaConfig, options.HysteriaConfig != ""
	if !hyExplicit {
		hyPath = DefaultHysteriaConfig
	}
	if _, statErr := os.Stat(hyPath); statErr == nil {
		ep, err := importHysteriaEndpoint(hyPath, options.Server, options.Name, id)
		if err != nil {
			return cfg, fmt.Errorf("adopt hysteria config %s: %w", hyPath, err)
		}
		endpoints = append(endpoints, ep)
		services = append(services, ExternalService{Name: "hysteria", Unit: hyUnit})
	} else if hyExplicit || !errors.Is(statErr, os.ErrNotExist) {
		return cfg, fmt.Errorf("read hysteria config %s: %w", hyPath, statErr)
	}

	xrPath, xrExplicit := options.XrayConfig, options.XrayConfig != ""
	if !xrExplicit {
		xrPath = DefaultXrayConfig
	}
	if _, statErr := os.Stat(xrPath); statErr == nil {
		eps, err := importXrayEndpoints(xrPath, options.Server, options.Name, id)
		if err != nil {
			return cfg, fmt.Errorf("adopt xray config %s: %w", xrPath, err)
		}
		if len(eps) > 0 {
			endpoints = append(endpoints, eps...)
			services = append(services, ExternalService{Name: "xray", Unit: xrayUnit})
		}
	} else if xrExplicit || !errors.Is(statErr, os.ErrNotExist) {
		return cfg, fmt.Errorf("read xray config %s: %w", xrPath, statErr)
	}

	if len(endpoints) == 0 {
		return cfg, errors.New("no supported Hysteria2 or Xray endpoints found to adopt")
	}
	if err := clash.Validate(endpoints); err != nil {
		return cfg, fmt.Errorf("validate adopted endpoints: %w", err)
	}

	cfg = Config{
		ID:                "node_" + id,
		Name:              options.Name,
		Manager:           strings.TrimRight(options.Manager, "/"),
		CAFile:            options.CAFile,
		AllowInsecureHTTP: options.AllowInsecureHTTP,
		EnrollToken:       options.EnrollToken,
		TokenFile:         filepath.Join(statePath, "agent.token"),
		Endpoints:         endpoints,
		Commands:          []Command{},
		Services:          services,
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return Config{}, err
	}
	if err := os.Mkdir(statePath, 0700); err != nil {
		return Config{}, fmt.Errorf("create node state directory: %w", err)
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

func importHysteriaEndpoint(configPath, serverHost, nodeName, suffix string) (model.Endpoint, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return model.Endpoint{}, err
	}
	var hy struct {
		Listen string `yaml:"listen"`
		TLS    struct {
			Cert string `yaml:"cert"`
		} `yaml:"tls"`
		Auth struct {
			Type     string `yaml:"type"`
			Password string `yaml:"password"`
		} `yaml:"auth"`
	}
	if err := yaml.Unmarshal(raw, &hy); err != nil {
		return model.Endpoint{}, err
	}
	if (hy.Auth.Type != "" && hy.Auth.Type != "password") || hy.Auth.Password == "" {
		return model.Endpoint{}, errors.New("only password-authenticated Hysteria2 server configs can be adopted")
	}
	port, err := parseListenPort(hy.Listen, 443)
	if err != nil {
		return model.Endpoint{}, err
	}
	sni := serverHost
	fingerprint := ""
	skipVerify := true
	if hy.TLS.Cert != "" {
		certPath := hy.TLS.Cert
		if !filepath.IsAbs(certPath) {
			certPath = filepath.Join(filepath.Dir(configPath), certPath)
		}
		certSNI, certFP, certSkip, err := inspectCertFile(certPath, serverHost)
		if err != nil {
			return model.Endpoint{}, err
		}
		sni, fingerprint, skipVerify = certSNI, certFP, certSkip
	}
	return model.Endpoint{
		ID:             "hy2_" + suffix,
		Name:           nodeName + " HY2",
		Type:           "hysteria2",
		Server:         serverHost,
		Port:           port,
		Password:       hy.Auth.Password,
		SNI:            sni,
		Fingerprint:    fingerprint,
		SkipCertVerify: skipVerify,
		TLS:            true,
		UDP:            true,
	}, nil
}

func importXrayEndpoints(configPath, serverHost, nodeName, suffix string) ([]model.Endpoint, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var xr struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Port     any    `json:"port"`
			Protocol string `json:"protocol"`
			Settings struct {
				Clients []struct {
					ID string `json:"id"`
				} `json:"clients"`
				Accounts []struct {
					User string `json:"user"`
					Pass string `json:"pass"`
				} `json:"accounts"`
				UDP bool `json:"udp"`
			} `json:"settings"`
			StreamSettings struct {
				Network  string `json:"network"`
				Security string `json:"security"`
				Reality  struct {
					ServerNames []string `json:"serverNames"`
					PrivateKey  string   `json:"privateKey"`
					ShortIDs    []string `json:"shortIds"`
				} `json:"realitySettings"`
				TLS struct {
					Certificates []struct {
						CertificateFile string `json:"certificateFile"`
					} `json:"certificates"`
				} `json:"tlsSettings"`
			} `json:"streamSettings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(stripJSONComments(raw), &xr); err != nil {
		return nil, err
	}

	typeCounts := map[string]int{}
	makeID := func(prefix string, port int) string {
		typeCounts[prefix]++
		if typeCounts[prefix] == 1 {
			return prefix + "_" + suffix
		}
		return fmt.Sprintf("%s_%d_%s", prefix, port, suffix)
	}
	makeName := func(label string, count int, port int) string {
		if count == 1 {
			return nodeName + " " + label
		}
		return fmt.Sprintf("%s %s (%d)", nodeName, label, port)
	}

	var out []model.Endpoint
	for _, ib := range xr.Inbounds {
		port, err := parseJSONPort(ib.Port)
		if err != nil {
			continue
		}
		switch strings.ToLower(ib.Protocol) {
		case "vless":
			if !strings.EqualFold(ib.StreamSettings.Security, "reality") {
				continue
			}
			if ib.StreamSettings.Network != "" && !strings.EqualFold(ib.StreamSettings.Network, "tcp") {
				continue
			}
			if len(ib.Settings.Clients) == 0 || ib.Settings.Clients[0].ID == "" || len(ib.StreamSettings.Reality.ServerNames) == 0 || ib.StreamSettings.Reality.ServerNames[0] == "" {
				continue
			}
			pubKey, err := deriveRealityPublicKey(ib.StreamSettings.Reality.PrivateKey)
			if err != nil {
				return nil, fmt.Errorf("derive REALITY public key on port %d: %w", port, err)
			}
			shortID := ""
			if len(ib.StreamSettings.Reality.ShortIDs) > 0 {
				shortID = ib.StreamSettings.Reality.ShortIDs[0]
			}
			epID := makeID("vless", port)
			out = append(out, model.Endpoint{
				ID:        epID,
				Name:      makeName("REALITY", typeCounts["vless"], port),
				Type:      "vless",
				Server:    serverHost,
				Port:      port,
				UUID:      ib.Settings.Clients[0].ID,
				SNI:       ib.StreamSettings.Reality.ServerNames[0],
				PublicKey: pubKey,
				ShortID:   shortID,
				TLS:       true,
				UDP:       true,
			})
		case "socks":
			if len(ib.Settings.Accounts) == 0 || ib.Settings.Accounts[0].User == "" || ib.Settings.Accounts[0].Pass == "" {
				continue
			}
			epID := makeID("socks5", port)
			out = append(out, model.Endpoint{
				ID:       epID,
				Name:     makeName("SOCKS5", typeCounts["socks5"], port),
				Type:     "socks5",
				Server:   serverHost,
				Port:     port,
				Username: ib.Settings.Accounts[0].User,
				Password: ib.Settings.Accounts[0].Pass,
				UDP:      ib.Settings.UDP,
			})
		case "http":
			if len(ib.Settings.Accounts) == 0 || ib.Settings.Accounts[0].User == "" || ib.Settings.Accounts[0].Pass == "" {
				continue
			}
			if strings.EqualFold(ib.StreamSettings.Security, "tls") {
				sni, fp, skip := "", "", true
				if len(ib.StreamSettings.TLS.Certificates) > 0 && ib.StreamSettings.TLS.Certificates[0].CertificateFile != "" {
					certPath := ib.StreamSettings.TLS.Certificates[0].CertificateFile
					if !filepath.IsAbs(certPath) {
						certPath = filepath.Join(filepath.Dir(configPath), certPath)
					}
					if certSNI, certFP, certSkip, err := inspectCertFile(certPath, serverHost); err == nil {
						sni, fp, skip = certSNI, certFP, certSkip
					}
				}
				epID := makeID("https", port)
				out = append(out, model.Endpoint{
					ID:             epID,
					Name:           makeName("HTTPS", typeCounts["https"], port),
					Type:           "https",
					Server:         serverHost,
					Port:           port,
					Username:       ib.Settings.Accounts[0].User,
					Password:       ib.Settings.Accounts[0].Pass,
					TLS:            true,
					SNI:            sni,
					Fingerprint:    fp,
					SkipCertVerify: skip,
				})
			} else {
				epID := makeID("http", port)
				out = append(out, model.Endpoint{
					ID:       epID,
					Name:     makeName("HTTP", typeCounts["http"], port),
					Type:     "http",
					Server:   serverHost,
					Port:     port,
					Username: ib.Settings.Accounts[0].User,
					Password: ib.Settings.Accounts[0].Pass,
				})
			}
		}
	}
	return out, nil
}

func parseListenPort(listen string, defaultPort int) (int, error) {
	s := strings.TrimSpace(listen)
	if s == "" {
		return defaultPort, nil
	}
	// Strip optional Hysteria2 port-hopping suffix (e.g. ":24443,50000-55000").
	s, _, _ = strings.Cut(s, ",")
	_, portStr, err := net.SplitHostPort(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("invalid listen address %q: %w", listen, err)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid listen port %q", portStr)
	}
	return p, nil
}

func parseJSONPort(v any) (int, error) {
	switch val := v.(type) {
	case float64:
		p := int(val)
		if float64(p) != val || p < 1 || p > 65535 {
			return 0, errors.New("port out of range")
		}
		return p, nil
	case string:
		p, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || p < 1 || p > 65535 {
			return 0, errors.New("invalid string port")
		}
		return p, nil
	default:
		return 0, errors.New("unsupported port type")
	}
}

func inspectCertFile(path, serverHost string) (sni string, fingerprint string, skipVerify bool, err error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return "", "", false, err
	}
	for len(pemBytes) > 0 {
		var block *pem.Block
		block, pemBytes = pem.Decode(pemBytes)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", "", false, err
		}
		sum := sha256.Sum256(cert.Raw)
		fp := hex.EncodeToString(sum[:])
		if cert.VerifyHostname(serverHost) == nil {
			return serverHost, fp, false, nil
		}
		for _, dns := range cert.DNSNames {
			candidate := dns
			if strings.HasPrefix(candidate, "*.") {
				candidate = "www." + strings.TrimPrefix(candidate, "*.")
			}
			if cert.VerifyHostname(candidate) == nil {
				return candidate, fp, false, nil
			}
		}
		for _, ip := range cert.IPAddresses {
			candidate := ip.String()
			if cert.VerifyHostname(candidate) == nil {
				return candidate, fp, false, nil
			}
		}
		if cert.Subject.CommonName != "" {
			return cert.Subject.CommonName, fp, true, nil
		}
		return serverHost, fp, true, nil
	}
	return "", "", false, fmt.Errorf("no PEM certificate found in %s", path)
}

func deriveRealityPublicKey(encoded string) (string, error) {
	s := strings.TrimSpace(encoded)
	if s == "" {
		return "", errors.New("missing REALITY private key")
	}
	var raw []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		raw, err = enc.DecodeString(s)
		if err == nil && len(raw) == 32 {
			break
		}
	}
	if len(raw) != 32 {
		return "", errors.New("invalid X25519 private key encoding")
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

// stripJSONComments removes // and /* */ comments outside JSON string literals
// so hand-edited Xray JSONC files parse cleanly.
func stripJSONComments(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inString := false
	escaped := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(src) {
			if src[i+1] == '/' {
				for i += 2; i < len(src) && src[i] != '\n'; i++ {
				}
				if i < len(src) {
					out = append(out, '\n')
				}
				continue
			}
			if src[i+1] == '*' {
				for i += 2; i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/'); i++ {
				}
				i++
				continue
			}
		}
		out = append(out, c)
	}
	return out
}
