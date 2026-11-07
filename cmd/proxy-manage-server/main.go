package main

import (
	"flag"
	"log"
	"os"
	"proxy-mono/internal/manager"
	"runtime/debug"
)

func env(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}
func main() {
	flag.Usage = func() {
		os.Stderr.WriteString("proxy-manage-server: SQLite control plane, embedded UI and dynamic Clash subscriptions. No proxy traffic.\nConfiguration via environment:\n  LISTEN (127.0.0.1:8080)\n  DATA_DIR (./data)\n  PUBLIC_URL (http://127.0.0.1:8080)\n  ADMIN_TOKEN (required, >=24 characters)\n  ENROLL_TOKEN (required, distinct, >=24 characters)\n  MASTER_KEY (optional base64 32 bytes; otherwise DATA_DIR/master.key)\n  TLS_CERT_FILE / TLS_KEY_FILE (optional pair, enables direct HTTPS)\n  GOMEMLIMIT (default 192MiB)\n")
	}
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(192 << 20)
	}
	s, err := manager.New(manager.Config{Listen: env("LISTEN", "127.0.0.1:8080"), DataDir: env("DATA_DIR", "./data"), PublicURL: env("PUBLIC_URL", "http://127.0.0.1:8080"), AdminToken: os.Getenv("ADMIN_TOKEN"), EnrollToken: os.Getenv("ENROLL_TOKEN"), MasterKey: os.Getenv("MASTER_KEY"), TLSCertFile: os.Getenv("TLS_CERT_FILE"), TLSKeyFile: os.Getenv("TLS_KEY_FILE")})
	if err != nil {
		log.Fatal(err)
	}
	defer s.Store.Close()
	log.Printf("proxy manager listening on %s (control plane only)", s.Config.Listen)
	s.HTTP.Spin()
}
