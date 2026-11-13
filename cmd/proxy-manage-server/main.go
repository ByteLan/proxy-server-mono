package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"proxy-mono/internal/manager"
	"proxy-mono/internal/updatecmd"
	"proxy-mono/internal/version"
	"runtime/debug"
	"syscall"
)

func env(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "proxy-manage-server:", err)
		os.Exit(1)
	}
}
func execute(ctx context.Context, args []string, output io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version":
			fmt.Fprintln(output, version.String("proxy-manage-server"))
			return nil
		case "update":
			return updatecmd.Run(ctx, "proxy-manage-server", args[1:], output)
		}
	}
	flags := flag.NewFlagSet("proxy-manage-server", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		fmt.Fprint(output, "Usage: proxy-manage-server [version | update [options]]\nSQLite control plane, embedded UI and dynamic Clash subscriptions. No proxy traffic.\nConfiguration via environment:\n  LISTEN (127.0.0.1:8080)\n  DATA_DIR (./data)\n  PUBLIC_URL (http://127.0.0.1:8080)\n  ADMIN_TOKEN (required, >=24 characters)\n  ENROLL_TOKEN (required, distinct, >=24 characters)\n  MASTER_KEY (optional base64 32 bytes; otherwise DATA_DIR/master.key)\n  TLS_CERT_FILE / TLS_KEY_FILE (optional pair, enables direct HTTPS)\n  HTTP_SUB_LISTEN / HTTP_SUB_PUBLIC_URL (optional pair, exposes read-only /sub/:token over HTTP for IP-only Clash clients)\n  GOMEMLIMIT (default 192MiB)\n")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 0 {
		return errors.New("unexpected command (use --help)")
	}
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(192 << 20)
	}
	s, err := manager.New(manager.Config{
		Listen:           env("LISTEN", "127.0.0.1:8080"),
		DataDir:          env("DATA_DIR", "./data"),
		PublicURL:        env("PUBLIC_URL", "http://127.0.0.1:8080"),
		AdminToken:       os.Getenv("ADMIN_TOKEN"),
		EnrollToken:      os.Getenv("ENROLL_TOKEN"),
		MasterKey:        os.Getenv("MASTER_KEY"),
		TLSCertFile:      os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:       os.Getenv("TLS_KEY_FILE"),
		HTTPSubListen:    os.Getenv("HTTP_SUB_LISTEN"),
		HTTPSubPublicURL: os.Getenv("HTTP_SUB_PUBLIC_URL"),
	})
	if err != nil {
		return err
	}
	defer s.Store.Close()
	if s.SubHTTP != nil {
		log.Printf("proxy manager HTTP subscription listener on %s (/sub/* only)", s.Config.HTTPSubListen)
		go func() {
			<-ctx.Done()
			_ = s.SubHTTP.Shutdown(context.Background())
		}()
		go func() {
			if runErr := s.SubHTTP.Run(); runErr != nil {
				log.Printf("HTTP subscription listener stopped: %v", runErr)
			}
		}()
	}
	log.Printf("proxy manager listening on %s (control plane only)", s.Config.Listen)
	s.HTTP.Spin()
	return nil
}
