package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestHelp(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"init", "--help"},
		{"adopt", "--help"},
		{"add-manager", "--help"},
		{"remove-manager", "--help"},
		{"list-managers", "--help"},
		{"run", "--help"},
	} {
		var output bytes.Buffer
		if err := execute(context.Background(), args, &output); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "config") {
			t.Fatalf("help omitted configuration: %s", output.String())
		}
	}
}
