// +build freebsd

package main

import (
	"github.com/opencontainers/runc/libcontainer/system"
	"os"
)

func shouldHonorXDGRuntimeDir() bool {
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		return false
	}
	if os.Geteuid() != 0 {
		return true
	}
	if !system.RunningInUserNS() {
		// euid == 0 , in the initial ns (i.e. the real root)
		// in this case, we should use /run/runc and ignore
		// $XDG_RUNTIME_DIR (e.g. /run/user/0) for backward
		// compatibility.
		return false
	}
	// euid = 0, in a userns.
	u, ok := os.LookupEnv("USER")
	return !ok || u != "root"
}
