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
	"syscall"

	"proxy-mono/internal/agent"
	"proxy-mono/internal/updatecmd"
	"proxy-mono/internal/version"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "proxy-server:", err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Fprintln(output, "Usage: proxy-server init [options] | run --config FILE | version | update [options]\n\ninit generates a node identity, HY2/REALITY credentials and local core configs.\nrun supervises proxy cores and reports status to the manager.\nUse init --help or run --help for options.\nEnrollment: ENROLL_TOKEN environment variable, or init --enroll-token.\nCore binaries must be installed separately with tested, pinned versions.")
		return nil
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(output, version.String("proxy-server"))
		return nil
	case "update":
		return updatecmd.Run(ctx, "proxy-server", args[1:], output)
	case "init":
		flags := flag.NewFlagSet("init", flag.ContinueOnError)
		flags.SetOutput(output)
		var options agent.InitOptions
		flags.StringVar(&options.ConfigPath, "config", "config.json", "agent JSON config path (must not exist)")
		flags.StringVar(&options.Server, "server", "", "public hostname or IP advertised to clients")
		flags.StringVar(&options.Name, "name", "", "node display name")
		flags.StringVar(&options.Manager, "manager", "", "manager HTTPS URL, including IP/domain and port")
		flags.StringVar(&options.EnrollToken, "enroll-token", "", "initial enrollment key; ENROLL_TOKEN at run time is preferred")
		flags.StringVar(&options.CAFile, "ca-file", "", "additional CA PEM file for manager TLS (system CAs remain enabled)")
		flags.BoolVar(&options.AllowInsecureHTTP, "allow-insecure-http", false, "allow loopback-only HTTP for local development")
		flags.IntVar(&options.HY2Port, "hy2-port", 443, "Hysteria2 UDP listen port")
		flags.IntVar(&options.VLESSPort, "vless-port", 443, "VLESS-REALITY TCP listen port")
		flags.StringVar(&options.HysteriaBin, "hysteria-bin", "hysteria", "pinned Hysteria2 executable path")
		flags.StringVar(&options.XrayBin, "xray-bin", "xray", "pinned Xray executable path compatible with clients")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected positional arguments")
		}
		cfg, err := agent.Init(options)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Initialized node %s (%s). Keep config and state files private.\nRun: proxy-server run --config %s\n", cfg.Name, cfg.ID, options.ConfigPath)
		return nil
	case "run":
		flags := flag.NewFlagSet("run", flag.ContinueOnError)
		flags.SetOutput(output)
		configPath := flags.String("config", "config.json", "agent JSON config path")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected positional arguments")
		}
		runner := agent.Runner{Logf: log.Printf}
		return runner.Run(ctx, *configPath)
	default:
		return fmt.Errorf("unknown command %q (use --help)", args[0])
	}
}
