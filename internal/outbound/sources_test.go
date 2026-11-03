package outbound

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// networkUse matches the ways Go code can open connections or send HTTP
// requests on its own.
var networkUse = regexp.MustCompile(`\bhttp\.(Get|Head|Post|PostForm|DefaultClient|DefaultTransport)\b|` +
	`\bhttp\.Client\{|\bhttp\.Transport\{|\bnet\.(Dial|DialTimeout|DialTCP|DialUDP)\b|\bnet\.Dialer\{|` +
	`\bnet\.ListenPacket\b|\bnet\.ResolveIPAddr\b|\bnet\.Lookup[A-Za-z]*\b`)

// TestNoOtherNetworkClients checks that this package is the only code that
// can reach other hosts: everything else must go through a Client, which
// refuses hosts nobody configured.
func TestNoOtherNetworkClients(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "testdata" || d.Name() == "build") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filepath.Dir(path) == "." || filepath.Base(filepath.Dir(path)) == "outbound" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if m := networkUse.FindString(line); m != "" {
				t.Errorf("%s:%d uses %s; outbound connections must go through outbound.Client", path, i+1, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
