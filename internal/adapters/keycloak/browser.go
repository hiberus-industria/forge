package keycloak

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// IsWSL reports whether forge runs inside Windows Subsystem for Linux.
func IsWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	b, err := os.ReadFile("/proc/version")
	return err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft")
}

// CanOpenBrowser guesses whether a local browser can complete a loopback
// redirect. Over SSH the browser would run on another machine, so the
// device flow is the right choice there.
func CanOpenBrowser() bool {
	switch runtime.GOOS {
	case "darwin", "windows":
		return os.Getenv("SSH_CONNECTION") == ""
	}
	if IsWSL() {
		return true
	}
	if os.Getenv("SSH_CONNECTION") != "" {
		return false
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// OpenBrowser opens url with the platform launcher.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "darwin":
		cmd = exec.Command("open", url)
	case runtime.GOOS == "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case IsWSL():
		if p, err := exec.LookPath("wslview"); err == nil {
			cmd = exec.Command(p, url)
		} else {
			// rundll32 avoids cmd.exe's '&' escaping problems with query strings.
			cmd = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
		}
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
