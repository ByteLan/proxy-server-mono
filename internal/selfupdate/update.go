// Package selfupdate implements explicit, operator-triggered binary updates.
// It never runs a downloaded executable, restarts services, or touches proxy
// cores, configuration, credentials, or databases.
package selfupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
)

const DefaultRepository = "ByteLan/proxy-server-mono"

type Options struct {
	Repository     string
	Channel        string
	Version        string
	CurrentVersion string
	CheckOnly      bool
	Target         string
	Token          string

	// APIBase and HTTPClient are injection points for tests. The CLI must not
	// expose them. APIBase accepts api.github.com or a loopback test server.
	APIBase    string
	HTTPClient *http.Client
}

type platform struct{ os, arch, arm string }

// Run checks or updates product, one of proxy-server and proxy-manage-server.
// Token precedence: Options.Token, GITHUB_TOKEN, GH_TOKEN. An empty Target
// resolves the running executable, including symlinks. Only Linux release
// binaries are supported. No service is restarted by this function.
func Run(ctx context.Context, product string, options Options, output io.Writer) error {
	p := platform{os: runtime.GOOS, arch: runtime.GOARCH}
	if p.arch == "arm" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "GOARM" {
					p.arm = strings.Split(setting.Value, ",")[0]
				}
			}
		}
	}
	return run(ctx, product, options, output, p)
}

func run(ctx context.Context, product string, options Options, output io.Writer, p platform) error {
	if output == nil {
		output = io.Discard
	}
	if product != "proxy-server" && product != "proxy-manage-server" {
		return fmt.Errorf("unsupported product")
	}
	targetPlatform, err := p.target()
	if err != nil {
		return err
	}
	if options.Repository == "" {
		options.Repository = DefaultRepository
	}
	if options.Channel == "" {
		options.Channel = "main"
	}
	if options.Channel != "main" && options.Channel != "stable" {
		return fmt.Errorf("channel must be main or stable")
	}
	if options.Token == "" {
		options.Token = os.Getenv("GITHUB_TOKEN")
	}
	if options.Token == "" {
		options.Token = os.Getenv("GH_TOKEN")
	}
	if !validToken(options.Token) {
		return fmt.Errorf("GitHub token contains invalid characters")
	}
	api, err := newGitHub(options)
	if err != nil {
		return err
	}
	release, err := api.release(ctx, options.Channel, options.Version)
	if err != nil {
		return err
	}
	name := product + "_" + targetPlatform
	binary, manifest, err := api.releaseAssets(release, name)
	if err != nil {
		return err
	}
	current := options.CurrentVersion
	if current == "" {
		current = "unknown"
	}
	fmt.Fprintf(output, "Current: %s; release: %s; target: %s\n", current, release.Tag, name)
	if options.CurrentVersion == release.Tag {
		fmt.Fprintln(output, "Already at the selected release; no changes made.")
		return nil
	}
	if options.CheckOnly {
		fmt.Fprintln(output, "Update available. Check only; no assets downloaded or files changed.")
		return nil
	}
	target, err := resolveTarget(options.Target)
	if err != nil {
		return err
	}
	unlock, err := acquireLock(target)
	if err != nil {
		return err
	}
	defer unlock()
	if err := install(ctx, api, target, binary, manifest, p); err != nil {
		return err
	}
	fmt.Fprintf(output, "Installed %s. Previous binary: %s.previous\n", release.Tag, target)
	fmt.Fprintf(output, "The running service was not restarted. Restart it explicitly: systemctl restart %s\n", product)
	return nil
}

func (p platform) target() (string, error) {
	if p.os != "linux" {
		return "", fmt.Errorf("self-update supports Linux only (this platform: %s/%s)", p.os, p.arch)
	}
	switch p.arch {
	case "amd64", "arm64":
		return "linux_" + p.arch, nil
	case "arm":
		if p.arm == "7" {
			return "linux_armv7", nil
		}
	}
	return "", fmt.Errorf("unsupported self-update architecture %s/%s (ARM requires GOARM=7)", p.os, p.arch)
}

func validToken(token string) bool {
	if len(token) > 4096 {
		return false
	}
	for _, c := range token {
		if c <= 32 || c > 126 {
			return false
		}
	}
	return true
}
