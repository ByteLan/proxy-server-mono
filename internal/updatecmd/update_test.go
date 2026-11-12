package updatecmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateTokenFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("example-private-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	token, err := readTokenFile(p)
	if err != nil || token != "example-private-token" {
		t.Fatal("failed to read private token")
	}
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = readTokenFile(p); err == nil {
		t.Fatal("accepted world-readable token")
	}
	if err := os.Chmod(p, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("secret\nsecond-line"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readTokenFile(p); err == nil {
		t.Fatal("accepted multiple token lines")
	}
}
func TestUpdateHelpDoesNotFetch(t *testing.T) {
	var out bytes.Buffer
	if err := Run(context.Background(), "proxy-server", []string{"--help"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "token-file") {
		t.Fatal("missing private repository instructions")
	}
}
