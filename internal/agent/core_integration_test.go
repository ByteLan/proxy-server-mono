package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"proxy-mono/internal/clash"
	"proxy-mono/internal/model"
)

// Set MIHOMO_TEST_BIN to a separately verified, pinned core binary. The -t
// switch only validates configuration and never opens a test proxy listener.
func TestGeneratedConfigurationWithMihomo(t *testing.T) {
	binary := os.Getenv("MIHOMO_TEST_BIN")
	if binary == "" {
		t.Skip("optional real-core check: set MIHOMO_TEST_BIN to a verified mihomo binary")
	}
	dir := t.TempDir()
	cfg, err := Init(InitOptions{ConfigPath: filepath.Join(dir, "agent.json"), Server: "127.0.0.1", Name: "local syntax check", Manager: "https://manager.example"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := clash.Render([]model.Node{{ID: cfg.ID, Name: cfg.Name, Enabled: true, Endpoints: cfg.Endpoints}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "client.yaml")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "-t", "-d", dir, "-f", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("mihomo rejected generated local test configuration: %v\n%s", err, output)
	}
}
