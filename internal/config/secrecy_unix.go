//go:build unix

package config

import (
	"fmt"
	"os"
	"syscall"
)

// checkSecrecy applies the rules of check_file_secrecy() in OpenBSD
// daemons: the file must be owned by root or the current user, and must
// not be group writable or accessible by others.
func checkSecrecy(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if uid := int(st.Uid); uid != 0 && uid != os.Getuid() {
			return fmt.Errorf("%s: owner not root or current user", path)
		}
	}
	if fi.Mode().Perm()&0o037 != 0 {
		return fmt.Errorf("%s: group writable or world read/writable", path)
	}
	return nil
}
