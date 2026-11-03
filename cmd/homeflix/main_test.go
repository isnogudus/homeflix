package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testIO(stdin string) (cliIO, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return cliIO{stdin: strings.NewReader(stdin), stdout: &out, stderr: &errOut}, &out, &errOut
}

func TestRunCommands(t *testing.T) {
	cio, out, _ := testIO("")
	if err := run([]string{"version"}, cio); err != nil || !strings.HasPrefix(out.String(), "homeflix ") {
		t.Errorf("version: err %v, out %q", err, out.String())
	}
	if err := run(nil, cio); err == nil {
		t.Error("no command: want error")
	}
	if err := run([]string{"frobnicate"}, cio); err == nil {
		t.Error("unknown command: want error")
	}
	if err := run([]string{"serve", "-config", "/nonexistent/homeflix.toml"}, cio); err == nil {
		t.Error("serve with missing config: want error")
	}
}

func TestUserCommands(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "homeflix.toml")
	if err := os.WriteFile(cfg, []byte("data_dir = \"data\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	user := func(stdin string, args ...string) (string, error) {
		cio, out, _ := testIO(stdin)
		err := run(append([]string{"user"}, append(args, "-config", cfg)...), cio)
		return out.String(), err
	}

	if out, err := user("secret\n", "add", "anna", "-admin"); err != nil || !strings.Contains(out, `administrator "anna"`) {
		t.Fatalf("add: %q, %v", out, err)
	}
	if _, err := user("other\n", "add", "Anna"); err == nil {
		t.Error("add duplicate: want error")
	}
	if _, err := user("\n", "add", "bert"); err == nil {
		t.Error("add with empty password: want error")
	}
	if _, err := user("pw\n", "add", "bert"); err != nil {
		t.Fatal(err)
	}
	if _, err := user("newpw\n", "passwd", "BERT"); err != nil {
		t.Errorf("passwd: %v", err)
	}
	out, err := user("", "list")
	if err != nil || !strings.Contains(out, "anna") || !strings.Contains(out, "admin") || !strings.Contains(out, "bert") {
		t.Errorf("list: %q, %v", out, err)
	}
	if _, err := user("", "delete", "bert"); err != nil {
		t.Errorf("delete: %v", err)
	}
	if _, err := user("", "delete", "bert"); err == nil {
		t.Error("delete twice: want error")
	}
	if _, err := user("", "add"); err == nil {
		t.Error("add without name: want error")
	}
	if _, err := user("", "rename", "x"); err == nil {
		t.Error("unknown subcommand: want error")
	}
}
