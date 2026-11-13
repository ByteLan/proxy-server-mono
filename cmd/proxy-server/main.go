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
		fmt.Fprintln(output, "Usage: proxy-server init [options] | adopt [options] | add-manager [options] | remove-manager [options] | list-managers [options] | run --config FILE | version | update [options]\n\ninit generates a node identity, HY2/REALITY credentials and local core configs.\nadopt imports existing host Hysteria2/Xray configs without reinstalling or spawning duplicate cores.\nadd-manager / remove-manager / list-managers manage multiple control-plane manager servers for one node.\nrun supervises proxy cores (or probes adopted systemd units) and reports status to all configured managers.\nUse <subcommand> --help for options.\nEnrollment: ENROLL_TOKEN environment variable, or --enroll-token.\nCore binaries must be installed separately with tested, pinned versions.")
		return nil
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(output, version.String("proxy-server"))
		return nil
	case "update":
		return updatecmd.Run(ctx, "proxy-server", args[1:], output)
	case "add-manager":
		flags := flag.NewFlagSet("add-manager", flag.ContinueOnError)
		flags.SetOutput(output)
		var options agent.AddManagerOptions
		flags.StringVar(&options.ConfigPath, "config", "/etc/proxy-server/config.json", "existing agent JSON config path")
		flags.StringVar(&options.Manager, "manager", "", "additional manager HTTPS URL, including IP/domain and port")
		flags.StringVar(&options.EnrollToken, "enroll-token", "", "enrollment key for this manager (ENROLL_TOKEN environment variable is preferred)")
		flags.StringVar(&options.CAFile, "ca-file", "", "additional CA PEM file for this manager's TLS certificate")
		flags.BoolVar(&options.AllowInsecureHTTP, "allow-insecure-http", false, "allow loopback-only HTTP for local development")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected positional arguments")
		}
		cfg, err := agent.AddManager(options)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Added manager %s to node %s (%s). Total managers: %d.\nRestart proxy-server to begin reporting to the new manager.\n", options.Manager, cfg.Name, cfg.ID, len(cfg.EffectiveManagers()))
		return nil
	case "remove-manager":
		flags := flag.NewFlagSet("remove-manager", flag.ContinueOnError)
		flags.SetOutput(output)
		configPath := flags.String("config", "/etc/proxy-server/config.json", "existing agent JSON config path")
		managerURL := flags.String("manager", "", "manager URL to remove")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected positional arguments")
		}
		cfg, err := agent.RemoveManager(*configPath, *managerURL)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Removed manager %s from node %s (%s). Remaining managers: %d.\nRestart proxy-server to apply.\n", *managerURL, cfg.Name, cfg.ID, len(cfg.EffectiveManagers()))
		return nil
	case "list-managers":
		flags := flag.NewFlagSet("list-managers", flag.ContinueOnError)
		flags.SetOutput(output)
		configPath := flags.String("config", "/etc/proxy-server/config.json", "existing agent JSON config path")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected positional arguments")
		}
		items, err := agent.ListManagers(*configPath)
		if err != nil {
			return err
		}
		for i, m := range items {
			state := "pending-enrollment"
			if m.Enrolled {
				state = "enrolled"
			}
			if m.CAFile != "" {
				fmt.Fprintf(output, "%d. %s [%s] (ca: %s)\n", i+1, m.URL, state, m.CAFile)
			} else {
				fmt.Fprintf(output, "%d. %s [%s]\n", i+1, m.URL, state)
			}
		}
		return nil
	case "adopt":
		flags := flag.NewFlagSet("adopt", flag.ContinueOnError)
		flags.SetOutput(output)
		var options agent.AdoptOptions
		flags.StringVar(&options.ConfigPath, "config", "config.json", "agent JSON config path (must not exist)")
		flags.StringVar(&options.Server, "server", "", "public hostname or IP advertised to clients")
		flags.StringVar(&options.Name, "name", "", "node display name")
		flags.StringVar(&options.Manager, "manager", "", "manager HTTPS URL, including IP/domain and port")
		flags.StringVar(&options.EnrollToken, "enroll-token", "", "initial enrollment key; ENROLL_TOKEN at run time is preferred")
		flags.StringVar(&options.CAFile, "ca-file", "", "additional CA PEM file for manager TLS (system CAs remain enabled)")
		flags.BoolVar(&options.AllowInsecureHTTP, "allow-insecure-http", false, "allow loopback-only HTTP for local development")
		flags.StringVar(&options.HysteriaConfig, "hysteria-config", "", "existing Hysteria2 server YAML path (defaults to /etc/hysteria/config.yaml if present)")
		flags.StringVar(&options.HysteriaUnit, "hysteria-unit", agent.DefaultHysteriaUnit, "systemd unit managing the existing Hysteria2 service")
		flags.StringVar(&options.XrayConfig, "xray-config", "", "existing Xray server JSON path (defaults to /etc/xray/config.json if present)")
		flags.StringVar(&options.XrayUnit, "xray-unit", agent.DefaultXrayUnit, "systemd unit managing the existing Xray service")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected positional arguments")
		}
		cfg, err := agent.Adopt(options)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Adopted existing node %s (%s) with %d endpoint(s). Existing core services remain untouched.\nRun: proxy-server run --config %s\n", cfg.Name, cfg.ID, len(cfg.Endpoints), options.ConfigPath)
		return nil
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
		flags.IntVar(&options.SOCKS5Port, "socks5-port", 1080, "SOCKS5 TCP/UDP listen port (0 disables)")
		flags.IntVar(&options.HTTPPort, "http-port", 8080, "HTTP proxy TCP listen port (0 disables)")
		flags.IntVar(&options.HTTPSPort, "https-port", 8443, "HTTPS proxy TLS listen port (0 disables)")
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
