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
	if err := run([]string{"serve", "-config", "/nonexistent/homeflix.conf"}, cio); err == nil {
		t.Error("serve with missing config: want error")
	}
}

func TestServeCheckOnly(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.conf")
	os.WriteFile(good, []byte("unused = 1\nlisten on 127.0.0.1 port 18096\nlibrary Filme { path /m }\n"), 0o600)
	cio, out, errOut := testIO("")
	if err := run([]string{"serve", "-n", "-config", good}, cio); err != nil || out.String() != "configuration OK\n" {
		t.Errorf("valid config: %v, %q", err, out.String())
	}
	if !strings.Contains(errOut.String(), "warning: ") || !strings.Contains(errOut.String(), "macro unused is not used") {
		t.Errorf("unused macro not warned about: %q", errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "data")); err == nil {
		t.Error("-n created the data directory")
	}

	bad := filepath.Join(dir, "bad.conf")
	os.WriteFile(bad, []byte("listen on nowhere\nlog level loud\n"), 0o600)
	cio, _, _ = testIO("")
	err := run([]string{"serve", "-n", "-config", bad}, cio)
	if err == nil || !strings.Contains(err.Error(), "bad.conf:1:") || !strings.Contains(err.Error(), "bad.conf:2:") {
		t.Errorf("invalid config: want both errors with lines, got %v", err)
	}
}

func TestUserCommands(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "homeflix.conf")
	if err := os.WriteFile(cfg, []byte("data directory \"data\"\n"), 0o600); err != nil {
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

func TestBackupCommand(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "homeflix.conf")
	os.WriteFile(cfg, []byte("data directory \"data\"\n"), 0o600)
	cio, out, _ := testIO("pw\n")
	if err := run([]string{"user", "add", "anna", "-config", cfg}, cio); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "backup.db")
	cio, out, _ = testIO("")
	if err := run([]string{"backup", dst, "-config", cfg}, cio); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "database written to "+dst) || !strings.Contains(out.String(), "metadata") {
		t.Errorf("output %q", out.String())
	}
	if fi, err := os.Stat(dst); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("backup %v, %v", fi, err)
	}
	if err := run([]string{"backup", "-config", cfg}, cio); err == nil {
		t.Error("backup without file name: want error")
	}
}
