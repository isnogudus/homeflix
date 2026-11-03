package jellyfin

import (
	"os"
	"testing"

	"homeflix/internal/auth"
)

func TestMain(m *testing.M) {
	auth.HashParams = auth.Params{MemoryKiB: 1024, Time: 1, Threads: 1}
	os.Exit(m.Run())
}
