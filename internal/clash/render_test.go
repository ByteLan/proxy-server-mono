package clash

import (
	"gopkg.in/yaml.v3"
	"proxy-mono/internal/model"
	"strings"
	"testing"
)

func TestRenderProtocolParametersAndReferences(t *testing.T) {
	nodes := []model.Node{{ID: "n1", Name: "same", Enabled: true, Endpoints: []model.Endpoint{{ID: "hy2", Name: "name: test\nquoted", Type: "hysteria2", Server: "2001:db8::1", Port: 443, Password: "secret: \"test\"", Fingerprint: strings.Repeat("a", 64)}, {ID: "vless", Name: "VLESS", Type: "vless", Server: "example.com", Port: 8443, UUID: "test-uuid", PublicKey: "test-key", ShortID: "abc", SNI: "www.microsoft.com"}}}, {ID: "n2", Name: "same", Enabled: true, Endpoints: []model.Endpoint{{ID: "hy2", Name: "name: test\nquoted", Type: "hysteria2", Server: "192.0.2.2", Port: 443, Password: "x"}}}}
	raw, err := Render(nodes, nil)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Proxies []map[string]any `yaml:"proxies"`
		Groups  []struct {
			Name    string   `yaml:"name"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
	}
	if err = yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Proxies) != 3 {
		t.Fatal("missing endpoints")
	}
	known := map[string]bool{"DIRECT": true}
	for _, p := range cfg.Proxies {
		name := p["name"].(string)
		if known[name] {
			t.Fatal("duplicate name")
		}
		known[name] = true
	}
	for _, g := range cfg.Groups {
		known[g.Name] = true
	}
	for _, g := range cfg.Groups {
		for _, p := range g.Proxies {
			if !known[p] {
				t.Fatalf("dangling reference %s", p)
			}
		}
	}
	if cfg.Proxies[0]["fingerprint"] != strings.Repeat("a", 64) {
		t.Fatal("certificate pin missing")
	}
	if cfg.Proxies[1]["flow"] != "xtls-rprx-vision" {
		t.Fatal("VLESS flow missing")
	}
	raw, err = Render(nodes, []string{"vless"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hysteria2") {
		t.Fatal("protocol filter ignored")
	}
}
