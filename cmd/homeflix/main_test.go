package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunCommands(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"version"}, &out, &errOut); err != nil || !strings.HasPrefix(out.String(), "homeflix ") {
		t.Errorf("version: err %v, out %q", err, out.String())
	}
	if err := run(nil, &out, &errOut); err == nil {
		t.Error("no command: want error")
	}
	if err := run([]string{"frobnicate"}, &out, &errOut); err == nil {
		t.Error("unknown command: want error")
	}
	if err := run([]string{"serve", "-config", "/nonexistent/homeflix.toml"}, &out, &errOut); err == nil {
		t.Error("serve with missing config: want error")
	}
}
