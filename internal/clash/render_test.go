package clash

import (
	"gopkg.in/yaml.v3"
	"proxy-mono/internal/model"
	"reflect"
	"strings"
	"testing"
)

type parsedConfig struct {
	Proxies []map[string]any `yaml:"proxies"`
	Groups  []struct {
		Name           string   `yaml:"name"`
		Type           string   `yaml:"type"`
		Proxies        []string `yaml:"proxies"`
		Interval       int      `yaml:"interval"`
		Tolerance      int      `yaml:"tolerance"`
		Lazy           *bool    `yaml:"lazy"`
		Hidden         bool     `yaml:"hidden"`
		MaxFailedTimes int      `yaml:"max-failed-times"`
	} `yaml:"proxy-groups"`
	Rules []string `yaml:"rules"`
}

func parseYAML(t *testing.T, raw []byte) parsedConfig {
	t.Helper()
	var cfg parsedConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{"DIRECT": true, "REJECT": true}
	for _, p := range cfg.Proxies {
		name := p["name"].(string)
		if known[name] {
			t.Fatalf("duplicate outbound name %s", name)
		}
		known[name] = true
	}
	for _, g := range cfg.Groups {
		if known[g.Name] {
			t.Fatalf("duplicate group name %s", g.Name)
		}
		known[g.Name] = true
	}
	for _, g := range cfg.Groups {
		if len(g.Proxies) == 0 {
			t.Fatalf("empty group %s", g.Name)
		}
		for _, p := range g.Proxies {
			if !known[p] {
				t.Fatalf("group %s has dangling reference %s", g.Name, p)
			}
		}
	}
	for _, rule := range cfg.Rules {
		parts := strings.Split(rule, ",")
		target := parts[len(parts)-1]
		if target == "no-resolve" {
			target = parts[len(parts)-2]
		}
		if !known[target] {
			t.Fatalf("rule %q references unknown outbound %q", rule, target)
		}
	}
	return cfg
}

func TestRenderProtocolParametersAndReferences(t *testing.T) {
	nodes := []model.Node{
		{
			ID: "n1", Name: "same", Enabled: true,
			Endpoints: []model.Endpoint{
				{ID: "hy2", Name: "name: test\nquoted", Type: "hysteria2", Server: "2001:db8::1", Port: 443, Password: "secret: \"test\"", Fingerprint: strings.Repeat("a", 64)},
				{ID: "vless", Name: "VLESS", Type: "vless", Server: "example.com", Port: 8443, UUID: "test-uuid", PublicKey: "test-key", ShortID: "abc", SNI: "www.microsoft.com"},
			},
		},
		{
			ID: "n2", Name: "same", Enabled: true,
			Endpoints: []model.Endpoint{
				{ID: "hy2", Name: "name: test\nquoted", Type: "hysteria2", Server: "192.0.2.2", Port: 443, Password: "x"},
			},
		},
	}
	raw, err := Render(nodes, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := parseYAML(t, raw)
	if len(cfg.Proxies) != 3 {
		t.Fatal("missing endpoints")
	}
	if cfg.Proxies[0]["name"] != "same Hysteria2" || cfg.Proxies[1]["name"] != "same VLESS" || cfg.Proxies[2]["name"] != "same Hysteria2 #2" {
		t.Fatalf("unexpected clean proxy names: %v / %v / %v", cfg.Proxies[0]["name"], cfg.Proxies[1]["name"], cfg.Proxies[2]["name"])
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

func TestGlobalDefaultsToRuleProxyAndStrictSiteGroupsIsolateNodes(t *testing.T) {
	nodes := []model.Node{
		{
			ID: "dirty", Name: "SG-Dirty", Enabled: true,
			Endpoints: []model.Endpoint{
				{ID: "hy2", Name: "HY2", Type: "hysteria2", Server: "198.51.100.10", Port: 443, Password: "p1"},
			},
		},
		{
			ID: "clean-a", Name: "US-Clean-A", Enabled: true,
			Endpoints: []model.Endpoint{
				{ID: "hy2", Name: "HY2", Type: "hysteria2", Server: "198.51.100.20", Port: 443, Password: "p2"},
				{ID: "vless", Name: "VLESS", Type: "vless", Server: "198.51.100.20", Port: 8443, UUID: "u2", PublicKey: "k2", SNI: "www.microsoft.com"},
			},
		},
		{
			ID: "clean-b", Name: "JP-Clean-B", Enabled: true,
			Endpoints: []model.Endpoint{
				{ID: "hy2", Name: "HY2", Type: "hysteria2", Server: "198.51.100.30", Port: 443, Password: "p3"},
			},
		},
		{
			ID: "unselected", Name: "Other", Enabled: true,
			Endpoints: []model.Endpoint{
				{ID: "hy2", Name: "HY2", Type: "hysteria2", Server: "203.0.113.99", Port: 443, Password: "secret-never-export"},
			},
		},
	}

	sub := model.Subscription{
		NodeIDs: []string{"dirty"},
		Routes: model.SiteRoutes{
			Claude:  model.RouteGroup{Custom: true, NodeIDs: []string{"clean-a"}},
			ChatGPT: model.RouteGroup{Custom: true, NodeIDs: []string{"clean-b"}},
			Google:  model.RouteGroup{Custom: true, NodeIDs: []string{"clean-a", "clean-b"}},
		},
	}
	raw, err := RenderSubscription(nodes, sub)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "203.0.113.99") || strings.Contains(string(raw), "secret-never-export") {
		t.Fatal("unselected node leaked into subscription YAML")
	}

	cfg := parseYAML(t, raw)
	groupByName := map[string]struct {
		Type           string
		Proxies        []string
		Interval       int
		Tolerance      int
		Lazy           *bool
		Hidden         bool
		MaxFailedTimes int
	}{}
	for _, g := range cfg.Groups {
		groupByName[g.Name] = struct {
			Type           string
			Proxies        []string
			Interval       int
			Tolerance      int
			Lazy           *bool
			Hidden         bool
			MaxFailedTimes int
		}{g.Type, g.Proxies, g.Interval, g.Tolerance, g.Lazy, g.Hidden, g.MaxFailedTimes}
	}

	// GLOBAL and GroupGeneral must stay visible; GroupAuto and strict groups must be hidden.
	if groupByName[GroupGeneral].Hidden {
		t.Fatal("GroupGeneral must remain visible")
	}
	if !groupByName[GroupAuto].Hidden {
		t.Fatal("GroupAuto must be hidden by default")
	}

	// GLOBAL must explicitly default to GroupGeneral ("🌍 代理"), never DIRECT.
	global, ok := groupByName[GroupGlobal]
	if !ok || global.Type != "select" || global.Hidden || global.Proxies[0] != GroupGeneral {
		t.Fatalf("GLOBAL group must default to %s, got %+v", GroupGeneral, global)
	}

	// General group only has dirty node (plus auto & DIRECT).
	dirtyHY2 := "SG-Dirty Hysteria2"
	cleanAHY2 := "US-Clean-A Hysteria2"
	cleanAVLESS := "US-Clean-A VLESS"
	cleanBHY2 := "JP-Clean-B Hysteria2"

	if want := []string{GroupAuto, dirtyHY2, "DIRECT"}; !reflect.DeepEqual(groupByName[GroupGeneral].Proxies, want) {
		t.Fatalf("unexpected general proxies: got %v want %v", groupByName[GroupGeneral].Proxies, want)
	}

	// Strict site groups must be hidden url-test, REJECT-first, tolerance 0, lazy=false, max-failed-times=1, no DIRECT, no dirty node.
	for name, wantProxies := range map[string][]string{
		GroupClaude:  {"REJECT", cleanAHY2, cleanAVLESS},
		GroupChatGPT: {"REJECT", cleanBHY2},
		GroupGoogle:  {"REJECT", cleanAHY2, cleanAVLESS, cleanBHY2},
	} {
		g, exists := groupByName[name]
		if !exists {
			t.Fatalf("missing group %s", name)
		}
		if g.Type != "url-test" || !g.Hidden || g.Interval != 30 || g.Tolerance != 0 || g.Lazy == nil || *g.Lazy != false || g.MaxFailedTimes != 1 {
			t.Fatalf("group %s is not strict hidden url-test: %+v", name, g)
		}
		if !reflect.DeepEqual(g.Proxies, wantProxies) {
			t.Fatalf("group %s proxies: got %v want %v", name, g.Proxies, wantProxies)
		}
	}

	// Disabling clean-a must make Claude strictly [REJECT], never falling back to dirty or DIRECT.
	nodes[1].Enabled = false
	rawDisabled, err := RenderSubscription(nodes, sub)
	if err != nil {
		t.Fatal(err)
	}
	cfgDisabled := parseYAML(t, rawDisabled)
	for _, g := range cfgDisabled.Groups {
		if g.Name == GroupClaude && !reflect.DeepEqual(g.Proxies, []string{"REJECT"}) {
			t.Fatalf("disabled dedicated Claude node leaked to other proxies: %v", g.Proxies)
		}
	}

	// Verify key rules precede CN DIRECT rules and include UDP REJECT guards.
	ruleIndex := func(target string) int {
		for i, r := range cfg.Rules {
			if r == target {
				return i
			}
		}
		return -1
	}
	cnIdx := ruleIndex("GEOSITE,cn,DIRECT")
	if cnIdx < 0 {
		t.Fatal("missing GEOSITE,cn,DIRECT")
	}
	for _, mustPrecedeCN := range []string{
		"DOMAIN-SUFFIX,claude.ai," + GroupClaude,
		"DOMAIN-KEYWORD,claude,REJECT",
		"DOMAIN-SUFFIX,chatgpt.com," + GroupChatGPT,
		"DOMAIN-SUFFIX,openai.com,REJECT",
		"DOMAIN-SUFFIX,antigravity.google," + GroupGoogle,
		"DOMAIN-SUFFIX,google.com," + GroupGoogle,
		"GEOSITE,google,REJECT",
	} {
		idx := ruleIndex(mustPrecedeCN)
		if idx < 0 || idx >= cnIdx {
			t.Fatalf("rule %q missing or placed after CN DIRECT (idx=%d, cnIdx=%d)", mustPrecedeCN, idx, cnIdx)
		}
	}
}
