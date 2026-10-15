// Package service installs the forge daemon as a per-user background
// service: a systemd --user unit on Linux/WSL2 (when systemd runs) and a
// launchd LaunchAgent on macOS.
package service

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/hiberus-industria/forge/internal/config"
)

const (
	unitName = "forge.service"
	label    = "com.hiberus.forge"
)

// Manager names the service manager in use.
type Manager string

const (
	Systemd Manager = "systemd"
	Launchd Manager = "launchd"
	None    Manager = "none"
)

// ErrUnsupported means no supported service manager is available.
var ErrUnsupported = errors.New("no user service manager available")

// Fallback explains how to run the daemon without a service manager
// (e.g. WSL2 without systemd).
const Fallback = `Sin systemd de usuario disponible. Opciones:
  • WSL2: activa systemd en /etc/wsl.conf ([boot] systemd=true), ejecuta 'wsl --shutdown' y vuelve a 'forge daemon install'.
  • O añade a tu ~/.bashrc / ~/.zshrc:
      pgrep -f "forge daemon run" >/dev/null || (nohup forge daemon run >>"$HOME/.local/state/forge/daemon.log" 2>&1 &)`

// Status describes the installed service.
type Status struct {
	Manager   Manager
	Path      string // unit/plist path
	Installed bool
	Running   bool
	Detail    string
}

// Detect returns the service manager available on this machine.
func Detect() Manager {
	switch runtime.GOOS {
	case "darwin":
		return Launchd
	case "linux":
		if _, err := os.Stat("/run/systemd/system"); err != nil {
			return None
		}
		if _, err := exec.LookPath("systemctl"); err != nil {
			return None
		}
		return Systemd
	}
	return None
}

// Install writes and starts the service running `<exe> daemon run`.
func Install(exe string) (Status, error) {
	switch Detect() {
	case Systemd:
		p := unitPath()
		if err := writeFile(p, renderUnit(exe)); err != nil {
			return Status{}, err
		}
		if err := run("systemctl", "--user", "daemon-reload"); err != nil {
			return Status{}, err
		}
		if err := run("systemctl", "--user", "enable", "--now", unitName); err != nil {
			return Status{}, err
		}
		// Restart picks up a new binary on reinstall.
		_ = run("systemctl", "--user", "restart", unitName)
		return Get()
	case Launchd:
		p := plistPath()
		if err := os.MkdirAll(config.StateDir(), 0o755); err != nil {
			return Status{}, err
		}
		if err := writeFile(p, renderPlist(exe, config.LogPath())); err != nil {
			return Status{}, err
		}
		_ = run("launchctl", "bootout", domainTarget()+"/"+label)
		if err := run("launchctl", "bootstrap", domainTarget(), p); err != nil {
			return Status{}, err
		}
		return Get()
	}
	return Status{Manager: None}, ErrUnsupported
}

// Uninstall stops and removes the service.
func Uninstall() error {
	switch Detect() {
	case Systemd:
		_ = run("systemctl", "--user", "disable", "--now", unitName)
		if err := os.Remove(unitPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return run("systemctl", "--user", "daemon-reload")
	case Launchd:
		_ = run("launchctl", "bootout", domainTarget()+"/"+label)
		if err := os.Remove(plistPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return ErrUnsupported
}

// Get reports the service status.
func Get() (Status, error) {
	switch m := Detect(); m {
	case Systemd:
		s := Status{Manager: m, Path: unitPath()}
		s.Installed = exists(s.Path)
		out, _ := exec.Command("systemctl", "--user", "is-active", unitName).Output()
		s.Detail = strings.TrimSpace(string(out))
		s.Running = s.Detail == "active"
		return s, nil
	case Launchd:
		s := Status{Manager: m, Path: plistPath()}
		s.Installed = exists(s.Path)
		out, err := exec.Command("launchctl", "print", domainTarget()+"/"+label).Output()
		s.Running = err == nil && bytes.Contains(out, []byte("state = running"))
		if s.Running {
			s.Detail = "running"
		} else {
			s.Detail = "stopped"
		}
		return s, nil
	}
	return Status{Manager: None, Detail: "not supported"}, nil
}

// LogsCommand returns the command that follows the daemon logs.
func LogsCommand(follow bool) *exec.Cmd {
	if Detect() == Systemd {
		args := []string{"--user", "-u", unitName, "-n", "200", "--no-pager"}
		if follow {
			args = append(args, "-f")
		}
		return exec.Command("journalctl", args...)
	}
	args := []string{"-n", "200"}
	if follow {
		args = append(args, "-f")
	}
	return exec.Command("tail", append(args, config.LogPath())...)
}

func unitPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		h, _ := os.UserHomeDir()
		base = filepath.Join(h, ".config")
	}
	return filepath.Join(base, "systemd", "user", unitName)
}

func plistPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library", "LaunchAgents", label+".plist")
}

func domainTarget() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

var unitTmpl = template.Must(template.New("unit").Parse(`[Unit]
Description=forge daemon (AI spend capture)
Documentation=https://github.com/hiberus-industria/forge
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{.}} daemon run
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`))

var plistTmpl = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + label + `</string>
  <key>ProgramArguments</key>
  <array><string>{{.Exe}}</string><string>daemon</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>{{.Log}}</string>
  <key>StandardErrorPath</key><string>{{.Log}}</string>
</dict>
</plist>
`))

func renderUnit(exe string) string {
	var b bytes.Buffer
	_ = unitTmpl.Execute(&b, exe)
	return b.String()
}

func renderPlist(exe, log string) string {
	var b bytes.Buffer
	_ = plistTmpl.Execute(&b, struct{ Exe, Log string }{exe, log})
	return b.String()
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}
