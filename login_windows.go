package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"golang.org/x/sys/windows/registry"
)

// A copy of Sharktic other than the installed one, as of a zip or a build
// folder, may have turned on the start at login, which starts that copy: then
// it ran at login even after a newer version was installed, as the app runs
// once. Such a copy, started at login, starts the installed one instead when
// that is no older, and the installed one takes the start at login.

const (
	// identifier is mygo.json's, by which the installer and the start at
	// login know the app.
	identifier = "com.local.sharktic"
	// loginArg is what mygo's start at login passes.
	loginArg     = "--mygo-opened-at-login"
	uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + identifier
)

// installedCopy is the executable and the version of the installed app, if
// it is installed.
func installedCopy() (exe, version string, ok bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, uninstallKey, registry.QUERY_VALUE)
	if err != nil {
		return "", "", false
	}
	defer k.Close()
	dir, _, err := k.GetStringValue("InstallLocation")
	if err != nil || dir == "" {
		return "", "", false
	}
	version, _, _ = k.GetStringValue("DisplayVersion")
	exe = filepath.Join(dir, appName+".exe")
	if _, err := os.Stat(exe); err != nil {
		return "", "", false
	}
	return exe, version, true
}

// ownExecutable is the path of this copy.
func ownExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// startInstalledAtLogin starts the installed copy, if this one is another
// started at login and the installed one is no older, and tells whether it
// did: then this copy quits.
func startInstalledAtLogin() bool {
	if !mygo.App.WasOpenedAtLogin() {
		return false
	}
	exe, version, ok := installedCopy()
	if !ok || samePath(exe, ownExecutable()) || compareVersions(version, mygo.App.Version()) < 0 {
		return false
	}
	cmd := exec.Command(exe, loginArg)
	cmd.Dir = filepath.Dir(exe)
	if err := cmd.Start(); err != nil {
		log.Println("start installed copy:", err)
		return false
	}
	cmd.Process.Release()
	return true
}

// takeLogin points the start at login at this copy, if this is the installed
// one, started at login by another. Task Manager's off stays, in its own key.
func takeLogin() {
	if !mygo.App.WasOpenedAtLogin() {
		return
	}
	self := ownExecutable()
	exe, _, ok := installedCopy()
	if !ok || !samePath(exe, self) {
		return
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	cmd := `"` + self + `" ` + loginArg
	if got, _, err := k.GetStringValue(identifier); err != nil || strings.EqualFold(got, cmd) {
		return
	}
	if err := k.SetStringValue(identifier, cmd); err != nil {
		log.Println("open at login:", err)
	}
}

// compareVersions compares two versions as 0.2.1, by their numbers: -1 if a
// is older, 1 if newer, 0 if the same. A part that is not a number counts as
// 0, as does one that is missing.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		na, nb := versionPart(pa, i), versionPart(pb, i)
		if na != nb {
			if na < nb {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionPart(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(parts[i]))
	return n
}
