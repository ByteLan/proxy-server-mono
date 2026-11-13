package clash

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"proxy-mono/internal/model"
	"strings"
)

const (
	GroupGeneral = "🌍 代理"
	GroupAuto    = "⚡ 自动"
	GroupClaude  = "🔒 Claude 禁止直连"
	GroupChatGPT = "🤖 ChatGPT 禁止直连"
	GroupGoogle  = "🔍 Google 禁止直连"
	GroupGlobal  = "GLOBAL"

	probeURL = "https://www.gstatic.com/generate_204"
)

var fakeIPFilter = []string{
	// 通用保留域
	"*.lan", "*.localdomain", "*.example", "*.invalid", "*.localhost", "*.test", "*.local", "*.home.arpa",
	// NTP
	"time.*.com", "time.*.gov", "time.*.edu.cn", "time.*.apple.com",
	"time1.*.com", "time2.*.com", "time3.*.com", "time4.*.com",
	"ntp.*.com", "ntp1.*.com", "ntp2.*.com", "*.time.edu.cn", "*.ntp.org.cn",
	"+.pool.ntp.org", "time1.cloud.tencent.com", "ntp.aliyun.com",
	// Windows / Android / iOS 网络连通性探测
	"+.msftconnecttest.com", "+.msftncsi.com",
	"+.connectivitycheck.gstatic.com", "+.connectivitycheck.android.com", "+.clients3.google.com",
	"+.captive.apple.com", "+.gsp-ssl.ls.apple.com", "+.gsp-ssl.ls-apple.com.akadns.net",
	// Apple 系统服务
	"+.push.apple.com", "mesu.apple.com", "swscan.apple.com", "swquery.apple.com",
	"swdownload.apple.com", "swcdn.apple.com", "swdist.apple.com",
	// 国内音乐 / 登录 / 推送 SDK
	"music.163.com", "*.music.163.com", "*.126.net",
	"y.qq.com", "*.y.qq.com", "streamoc.music.tc.qq.com", "mobileoc.music.tc.qq.com",
	"isure.stream.qqmusic.qq.com", "dl.stream.qqmusic.qq.com", "aqqmusic.tc.qq.com", "amobile.music.tc.qq.com",
	"*.xiami.com", "*.music.migu.cn", "music.migu.cn", "musicapi.taihe.com", "music.taihe.com",
	"songsearch.kugou.com", "trackercdn.kugou.com", "*.kuwo.cn",
	"localhost.ptlogin2.qq.com", "localhost.sec.qq.com", "+.dingtalk.com", "+.yunos.com",
	"*.189.cn", "*.10010.com", "*.10086.cn",
	// STUN / P2P / 游戏平台 / 路由器
	"stun.*.*", "stun.*.*.*", "+.stun.*.*", "+.stun.*.*.*", "+.stun.*.*.*.*",
	"stun.l.google.com", "lens.l.google.com",
	"+.srv.nintendo.net", "+.stun.playstation.net", "xbox.*.microsoft.com", "xnotify.xboxlive.com",
	"+.battlenet.com.cn", "+.wotgame.cn", "+.wggames.cn", "+.wowsgame.cn", "+.wargaming.net",
	"+.ipv6.microsoft.com", "heartbeat.belkin.com", "*.linksys.com", "*.linksyssmartwifi.com",
	"*.router.asus.com", "proxy.golang.org", "*.mcdn.bilivideo.cn", "+.media.dssott.com", "+.pvp.net",
}

var (
	claudeMatchers = []string{
		"DOMAIN-SUFFIX,claude.ai",
		"DOMAIN-SUFFIX,claude.com",
		"DOMAIN-SUFFIX,claudeusercontent.com",
		"DOMAIN-SUFFIX,anthropic.com",
		"DOMAIN-SUFFIX,anthropic.services",
		"GEOSITE,anthropic",
		"DOMAIN-KEYWORD,anthropic",
		"DOMAIN-KEYWORD,claude",
	}
	chatgptMatchers = []string{
		"DOMAIN-SUFFIX,chatgpt.com",
		"DOMAIN-SUFFIX,openai.com",
		"DOMAIN-SUFFIX,oaistatic.com",
		"DOMAIN-SUFFIX,oaiusercontent.com",
		"DOMAIN-SUFFIX,sora.com",
		"DOMAIN,openaiapi-site.azureedge.net",
		"GEOSITE,openai",
		"DOMAIN-KEYWORD,openai",
		"DOMAIN-KEYWORD,chatgpt",
	}
	googleMatchers = []string{
		"DOMAIN-SUFFIX,antigravity.google",
		"DOMAIN-KEYWORD,antigravity",
		"DOMAIN-SUFFIX,deepmind.com",
		"DOMAIN-SUFFIX,google",
		"DOMAIN-SUFFIX,goog",
		"DOMAIN-SUFFIX,google.com",
		"DOMAIN-SUFFIX,google.com.hk",
		"DOMAIN-SUFFIX,google.co.jp",
		"DOMAIN-SUFFIX,google.co.kr",
		"DOMAIN-SUFFIX,googleapis.com",
		"DOMAIN-SUFFIX,googlevideo.com",
		"DOMAIN-SUFFIX,googleusercontent.com",
		"DOMAIN-SUFFIX,gstatic.com",
		"DOMAIN-SUFFIX,ggpht.com",
		"DOMAIN-SUFFIX,google-analytics.com",
		"DOMAIN-SUFFIX,googleadservices.com",
		"DOMAIN-SUFFIX,googlesyndication.com",
		"DOMAIN-SUFFIX,googletagmanager.com",
		"DOMAIN-SUFFIX,youtube.com",
		"DOMAIN-SUFFIX,ytimg.com",
		"DOMAIN-SUFFIX,youtu.be",
		"DOMAIN-SUFFIX,gmail.com",
		"DOMAIN-SUFFIX,1e100.net",
		"DOMAIN-SUFFIX,gvt1.com",
		"DOMAIN-SUFFIX,gvt2.com",
		"GEOSITE,google",
		"GEOSITE,youtube",
	}
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

// Render builds client configuration using all provided enabled nodes as the general pool.
func Render(nodes []model.Node, protocols []string) ([]byte, error) {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	return RenderSubscription(nodes, model.Subscription{NodeIDs: ids, Protocols: protocols})
}

// RenderSubscription builds mihomo / Clash Meta configuration for a subscription's general and dedicated site selections; it never opens a proxy listener.
func RenderSubscription(nodes []model.Node, sub model.Subscription) ([]byte, error) {
	filter := map[string]bool{}
	for _, p := range sub.Protocols {
		filter[p] = true
	}
	needed := map[string]bool{}
	addNeeded := func(ids []string) {
		for _, id := range ids {
			needed[id] = true
		}
	}
	addNeeded(sub.NodeIDs)
	if sub.Routes.Claude.Custom {
		addNeeded(sub.Routes.Claude.NodeIDs)
	}
	if sub.Routes.ChatGPT.Custom {
		addNeeded(sub.Routes.ChatGPT.NodeIDs)
	}
	if sub.Routes.Google.Custom {
		addNeeded(sub.Routes.Google.NodeIDs)
	}

	proxies := []map[string]any{}
	allNames := []string{}
	byNode := map[string][]string{}
	for _, node := range nodes {
		if !node.Enabled || !needed[node.ID] {
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
			allNames = append(allNames, name)
			byNode[node.ID] = append(byNode[node.ID], name)
		}
	}

	namesFor := func(ids []string) []string {
		set := map[string]bool{}
		for _, id := range ids {
			set[id] = true
		}
		out := []string{}
		for _, node := range nodes {
			if set[node.ID] {
				out = append(out, byNode[node.ID]...)
			}
		}
		return out
	}
	generalNames := namesFor(sub.NodeIDs)
	routeNames := func(rg model.RouteGroup) []string {
		if rg.Custom {
			return namesFor(rg.NodeIDs)
		}
		return generalNames
	}
	claudeNames := routeNames(sub.Routes.Claude)
	chatgptNames := routeNames(sub.Routes.ChatGPT)
	googleNames := routeNames(sub.Routes.Google)

	generalChoices := []string{"DIRECT"}
	if len(generalNames) > 0 {
		generalChoices = make([]string, 0, len(generalNames)+2)
		generalChoices = append(generalChoices, GroupAuto)
		generalChoices = append(generalChoices, generalNames...)
		generalChoices = append(generalChoices, "DIRECT")
	}

	globalChoices := []string{GroupGeneral}
	if len(generalNames) > 0 {
		globalChoices = append(globalChoices, GroupAuto)
	}
	globalChoices = append(globalChoices, GroupClaude, GroupChatGPT, GroupGoogle)
	globalChoices = append(globalChoices, allNames...)
	globalChoices = append(globalChoices, "DIRECT", "REJECT")

	groups := []map[string]any{
		{"name": GroupGeneral, "type": "select", "proxies": generalChoices},
	}
	if len(generalNames) > 0 {
		groups = append(groups, map[string]any{
			"name":      GroupAuto,
			"type":      "url-test",
			"proxies":   generalNames,
			"url":       probeURL,
			"interval":  300,
			"tolerance": 50,
		})
	}
	groups = append(groups,
		strictAutoGroup(GroupClaude, claudeNames),
		strictAutoGroup(GroupChatGPT, chatgptNames),
		strictAutoGroup(GroupGoogle, googleNames),
		map[string]any{"name": GroupGlobal, "type": "select", "proxies": globalChoices},
	)

	domesticDoH := []string{"https://223.5.5.5/dns-query", "https://120.53.53.53/dns-query"}
	cfg := map[string]any{
		"mixed-port":        7890,
		"allow-lan":         false,
		"mode":              "rule",
		"log-level":         "info",
		"unified-delay":     true,
		"tcp-concurrent":    true,
		"find-process-mode": "strict",
		"geodata-mode":      true,
		"geox-url": map[string]any{
			"geoip":   "https://cdn.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/geoip.dat",
			"geosite": "https://cdn.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/geosite.dat",
			"mmdb":    "https://cdn.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/country.mmdb",
		},
		"dns": map[string]any{
			"enable":                  true,
			"ipv6":                    false,
			"prefer-h3":               true,
			"enhanced-mode":           "fake-ip",
			"fake-ip-range":           "28.0.0.1/8",
			"use-hosts":               true,
			"use-system-hosts":        true,
			"fake-ip-filter":          fakeIPFilter,
			"default-nameserver":      []string{"223.5.5.5", "119.29.29.29"},
			"nameserver":              []string{"https://223.5.5.5/dns-query", "https://120.53.53.53/dns-query", "https://doh.pub/dns-query", "https://dns.alidns.com/dns-query"},
			"nameserver-policy":       map[string]any{"geosite:cn,private": domesticDoH},
			"proxy-server-nameserver": domesticDoH,
			"direct-nameserver":       domesticDoH,
		},
		"proxies":      proxies,
		"proxy-groups": groups,
		"rules":        buildRules(),
	}
	return yaml.Marshal(cfg)
}

// strictAutoGroup keeps REJECT at index 0: when any member succeeds URL probing,
// mihomo selects the lowest-latency endpoint across the chosen nodes/protocols;
// when all members fail (or none are available), mihomo falls back to index 0 (REJECT)
// and never leaks traffic via DIRECT or unselected nodes.
func strictAutoGroup(name string, memberNames []string) map[string]any {
	proxies := make([]string, 0, 1+len(memberNames))
	proxies = append(proxies, "REJECT")
	proxies = append(proxies, memberNames...)
	return map[string]any{
		"name":             name,
		"type":             "url-test",
		"proxies":          proxies,
		"url":              probeURL,
		"interval":         30,
		"timeout":          5000,
		"tolerance":        0,
		"lazy":             false,
		"max-failed-times": 1,
	}
}

func appendStrictSiteRules(dst []string, matchers []string, group string) []string {
	for _, m := range matchers {
		dst = append(dst, m+","+group)
	}
	// Non-UDP proxies (e.g. HTTP/HTTPS) skip UDP/QUIC and continue matching later rules;
	// mirror each matcher with REJECT immediately below so UDP never leaks to DIRECT.
	for _, m := range matchers {
		dst = append(dst, m+",REJECT")
	}
	return dst
}

func buildRules() []string {
	rules := []string{
		"GEOSITE,private,DIRECT",
		"IP-CIDR,127.0.0.0/8,DIRECT,no-resolve",
		"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
		"IP-CIDR,172.16.0.0/12,DIRECT,no-resolve",
		"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
		"GEOIP,private,DIRECT,no-resolve",
	}
	rules = appendStrictSiteRules(rules, claudeMatchers, GroupClaude)
	rules = appendStrictSiteRules(rules, chatgptMatchers, GroupChatGPT)
	rules = appendStrictSiteRules(rules, googleMatchers, GroupGoogle)
	rules = append(rules,
		// 国内直连
		"DOMAIN-SUFFIX,cn,DIRECT",
		"GEOSITE,cn,DIRECT",
		"GEOIP,cn,DIRECT,no-resolve",
		// 登录 / 支付 / 验证码 / IP 检测 / 其他 AI
		"DOMAIN-SUFFIX,auth0.com,"+GroupGeneral,
		"DOMAIN-SUFFIX,clerk.com,"+GroupGeneral,
		"DOMAIN-SUFFIX,stripe.com,"+GroupGeneral,
		"DOMAIN-SUFFIX,stripe.network,"+GroupGeneral,
		"DOMAIN-SUFFIX,recaptcha.net,"+GroupGeneral,
		"DOMAIN-SUFFIX,hcaptcha.com,"+GroupGeneral,
		"DOMAIN-SUFFIX,turnstile.com,"+GroupGeneral,
		"DOMAIN,challenges.cloudflare.com,"+GroupGeneral,
		"DOMAIN-SUFFIX,whatismyipaddress.com,"+GroupGeneral,
		"DOMAIN-SUFFIX,whatismyip.com,"+GroupGeneral,
		"DOMAIN-SUFFIX,ipinfo.io,"+GroupGeneral,
		"DOMAIN-SUFFIX,ipapi.co,"+GroupGeneral,
		"DOMAIN-SUFFIX,copilot.microsoft.com,"+GroupGeneral,
		"DOMAIN-SUFFIX,perplexity.ai,"+GroupGeneral,
		// 常用海外 GeoSite
		"GEOSITE,github,"+GroupGeneral,
		"GEOSITE,twitter,"+GroupGeneral,
		"GEOSITE,facebook,"+GroupGeneral,
		"GEOSITE,telegram,"+GroupGeneral,
		"GEOSITE,netflix,"+GroupGeneral,
		"GEOSITE,spotify,"+GroupGeneral,
		"GEOSITE,geolocation-!cn,"+GroupGeneral,
		// 兜底
		"MATCH,"+GroupGeneral,
	)
	return rules
}
