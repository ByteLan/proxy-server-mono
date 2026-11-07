package clash

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"proxy-mono/internal/model"
	"strings"
)

func Validate(endpoints []model.Endpoint) error {
	if len(endpoints) > 64 {
		return fmt.Errorf("at most 64 endpoints per node")
	}
	ids := map[string]bool{}
	for _, e := range endpoints {
		if e.ID == "" || len(e.ID) > 80 || ids[e.ID] {
			return fmt.Errorf("endpoint IDs must be nonempty and unique")
		}
		ids[e.ID] = true
		if e.Server == "" || len(e.Server) > 253 || strings.ContainsAny(e.Server, " /\\\t\r\n") || e.Port < 1 || e.Port > 65535 {
			return fmt.Errorf("invalid endpoint address or port")
		}
		if len(e.Name) > 120 {
			return fmt.Errorf("endpoint name too long")
		}
		switch e.Type {
		case "hysteria2":
			if e.Password == "" {
				return fmt.Errorf("hysteria2 password required")
			}
		case "vless":
			if e.UUID == "" || e.PublicKey == "" || e.SNI == "" {
				return fmt.Errorf("VLESS UUID, REALITY public key and SNI required")
			}
		case "socks5", "http", "https":
			if e.Username == "" || e.Password == "" {
				return fmt.Errorf("proxy username and password required")
			}
		default:
			return fmt.Errorf("unsupported protocol %q", e.Type)
		}
	}
	return nil
}

// Render builds client configuration; it never opens a proxy listener.
func Render(nodes []model.Node, protocols []string) ([]byte, error) {
	filter := map[string]bool{}
	for _, p := range protocols {
		filter[p] = true
	}
	proxies := []map[string]any{}
	names := []string{}
	for _, node := range nodes {
		if !node.Enabled {
			continue
		}
		for _, e := range node.Endpoints {
			if len(filter) > 0 && !filter[e.Type] {
				continue
			}
			// IDs make names deterministic and collision-free across nodes/protocols.
			name := fmt.Sprintf("%s / %s [%s:%s]", node.Name, e.Name, node.ID, e.ID)
			p := map[string]any{"name": name, "type": e.Type, "server": e.Server, "port": e.Port}
			switch e.Type {
			case "hysteria2":
				p["password"] = e.Password
				p["sni"] = e.SNI
				p["skip-cert-verify"] = e.SkipCertVerify
			case "vless":
				p["uuid"] = e.UUID
				p["network"] = "tcp"
				p["tls"] = true
				p["udp"] = true
				p["flow"] = "xtls-rprx-vision"
				p["servername"] = e.SNI
				p["client-fingerprint"] = "chrome"
				p["reality-opts"] = map[string]any{"public-key": e.PublicKey, "short-id": e.ShortID}
			case "socks5":
				p["username"] = e.Username
				p["password"] = e.Password
				p["udp"] = e.UDP
			case "http", "https":
				p["type"] = "http"
				p["username"] = e.Username
				p["password"] = e.Password
				if e.Type == "https" || e.TLS {
					p["tls"] = true
					p["skip-cert-verify"] = e.SkipCertVerify
					if e.SNI != "" {
						p["sni"] = e.SNI
					}
				}
			default:
				continue
			}
			if e.Fingerprint != "" {
				p["fingerprint"] = e.Fingerprint
			}
			proxies = append(proxies, p)
			names = append(names, name)
		}
	}
	groups := []map[string]any{}
	choices := []string{"DIRECT"}
	if len(names) > 0 {
		choices = append([]string{"自动选择"}, names...)
		groups = append(groups, map[string]any{"name": "自动选择", "type": "url-test", "proxies": names, "url": "https://www.gstatic.com/generate_204", "interval": 300, "tolerance": 50})
	}
	groups = append([]map[string]any{{"name": "代理", "type": "select", "proxies": choices}}, groups...)
	cfg := map[string]any{
		"mixed-port": 7890, "allow-lan": false, "mode": "rule", "log-level": "info", "proxies": proxies, "proxy-groups": groups,
		"dns":   map[string]any{"enable": true, "ipv6": false, "enhanced-mode": "fake-ip", "fake-ip-range": "198.18.0.1/16", "fake-ip-filter": []string{"*.lan", "*.local"}, "default-nameserver": []string{"223.5.5.5", "119.29.29.29"}, "nameserver": []string{"https://dns.alidns.com/dns-query", "https://doh.pub/dns-query"}, "proxy-server-nameserver": []string{"https://dns.alidns.com/dns-query"}},
		"rules": []string{"IP-CIDR,127.0.0.0/8,DIRECT,no-resolve", "IP-CIDR,10.0.0.0/8,DIRECT,no-resolve", "IP-CIDR,172.16.0.0/12,DIRECT,no-resolve", "IP-CIDR,192.168.0.0/16,DIRECT,no-resolve", "MATCH,代理"},
	}
	return yaml.Marshal(cfg)
}
