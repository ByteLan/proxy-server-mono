package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestInformationalCommandsDoNotStartServer(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "")
	t.Setenv("ENROLL_TOKEN", "")
	for _, args := range [][]string{{"--help"}, {"version"}, {"update", "--help"}} {
		var out bytes.Buffer
		if err := execute(context.Background(), args, &out); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out.String(), "proxy-manage-server") {
			t.Fatalf("command produced no product information: %v", args)
		}
	}
}
