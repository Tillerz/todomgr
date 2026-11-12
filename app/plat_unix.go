//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
)

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// bootID changes with every start of the system (Linux, also WSL); "" where it is not known.
func bootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

func openBrowser(url string) {
	if b, err := os.ReadFile("/proc/version"); err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft") {
		// WSL: hand the URL to Windows' default browser
		if exec.Command("explorer.exe", url).Start() == nil {
			return
		}
		if exec.Command("cmd.exe", "/c", "start", "", url).Start() == nil {
			return
		}
	}
	if runtime.GOOS == "darwin" {
		_ = exec.Command("open", url).Start()
		return
	}
	_ = exec.Command("xdg-open", url).Start()
}
