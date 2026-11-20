package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Turning on advanced fan control installs the service and PawnIO; turning it
// off removes the service. Both run in a copy of Sharktic.exe that Windows
// starts with administrator rights after one UAC prompt, and show no window:
// the app shows the progress and reads the exit code.

// PawnIO 2.2.0's official, signed installer, which its author lets others
// redistribute (https://pawnio.eu). It installs silently with -install -silent.
//
//go:embed pawnio/PawnIO_setup.exe
var pawnIOSetup []byte

const (
	enableFlag  = "--enable-advanced"
	disableFlag = "--disable-advanced"
	// removePawnIOFlag follows disableFlag to remove PawnIO too, which other
	// programs may use.
	removePawnIOFlag = "--remove-pawnio"
	serviceFlag      = "--service"
)

// Exit codes of the elevated copy, which the app tells the user.
const (
	setupOK = iota
	setupNotElevated
	setupCopyFailed
	setupPawnIOFailed
	setupPawnIOTooOld
	setupServiceFailed
	setupRemoveFailed
)

func setupMessage(code int) string {
	switch code {
	case setupOK:
		return ""
	case setupNotElevated:
		return "관리자 권한을 얻지 못했습니다"
	case setupCopyFailed:
		return "Program Files에 서비스 파일을 복사하지 못했습니다"
	case setupPawnIOFailed:
		return "PawnIO 드라이버를 설치하지 못했습니다"
	case setupPawnIOTooOld:
		return "오래된 PawnIO가 설치되어 있습니다. 제거한 뒤 다시 시도해 주세요"
	case setupServiceFailed:
		return "Sharktic 서비스를 등록하거나 시작하지 못했습니다"
	case setupRemoveFailed:
		return "Sharktic 서비스를 제거하지 못했습니다"
	}
	return fmt.Sprintf("알 수 없는 오류 (%d)", code)
}

// serviceDir is where the service's copy of Sharktic.exe lives: a folder only
// administrators can change, as LocalSystem runs what is in it.
func serviceDir() (string, error) {
	pf, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(pf, appName), nil
}

func serviceExe() (string, error) {
	dir, err := serviceDir()
	return filepath.Join(dir, "Sharktic.exe"), err
}

// runSetup runs as the elevated copy and returns its exit code.
func runSetup(args []string) int {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return setupNotElevated
	}
	switch args[0] {
	case enableFlag:
		return enableAdvanced()
	case disableFlag:
		return disableAdvanced(len(args) > 1 && args[1] == removePawnIOFlag)
	}
	return setupNotElevated
}

func enableAdvanced() int {
	m, err := mgr.Connect()
	if err != nil {
		return setupServiceFailed
	}
	defer m.Disconnect()
	// An older service holds its file: stop it before the copy.
	if s, err := m.OpenService(serviceName); err == nil {
		stopService(s)
		s.Close()
	}
	exe, err := installServiceExe()
	if err != nil {
		return setupCopyFailed
	}
	if code := ensurePawnIO(); code != setupOK {
		return code
	}
	if err := installService(m, exe); err != nil {
		return setupServiceFailed
	}
	return setupOK
}

// volumeNameNT asks GetFinalPathNameByHandle for a \Device\… path, as
// GetMappedFileName gives.
const volumeNameNT = 0x2

var procGetMappedFileNameW = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetMappedFileNameW")

// openOwnImage opens the file this process runs from, so that it can be
// copied. The user approved this file in the UAC prompt; but anything the user
// runs may rename it and put another in its place before the copy, which
// would then run as LocalSystem. So the file at the path must be the one
// mapped as this process's image, and it stays open without sharing delete
// or write, which keeps it from being renamed or changed during the copy.
func openOwnImage() (*os.File, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	name, _ := windows.UTF16PtrFromString(self)
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, err
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), volumeNameNT)
	if err != nil || int(n) >= len(buf) {
		windows.CloseHandle(h)
		return nil, errors.New("no path of the opened file")
	}
	opened := windows.UTF16ToString(buf[:n])
	var image windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &image); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	mapped := make([]uint16, windows.MAX_LONG_PATH)
	m, _, _ := procGetMappedFileNameW.Call(uintptr(windows.CurrentProcess()), uintptr(image),
		uintptr(unsafe.Pointer(&mapped[0])), uintptr(len(mapped)))
	if m == 0 || !strings.EqualFold(windows.UTF16ToString(mapped[:m]), opened) {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("%s is not the running image", self)
	}
	return os.NewFile(uintptr(h), self), nil
}

// installServiceExe copies the running Sharktic.exe to serviceDir.
func installServiceExe() (string, error) {
	in, err := openOwnImage()
	if err != nil {
		return "", err
	}
	defer in.Close()
	dst, err := serviceExe()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if strings.EqualFold(in.Name(), dst) {
		return dst, nil
	}
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

// ensurePawnIO installs PawnIO if it is missing. The installer is written to
// serviceDir, where no user can swap it before it runs.
func ensurePawnIO() int {
	if pawnIOReady() {
		return setupOK
	}
	if v, _, ok := pawnIOInstalled(); ok && !versionAtLeast(v, [3]int{2, 1, 0}) {
		// The installer upgrades 2.1.0 only; older ones must be removed first.
		return setupPawnIOTooOld
	}
	dir, err := serviceDir()
	if err != nil {
		return setupPawnIOFailed
	}
	setup := filepath.Join(dir, "PawnIO_setup.exe")
	if err := os.WriteFile(setup, pawnIOSetup, 0o755); err != nil {
		return setupPawnIOFailed
	}
	defer os.Remove(setup)
	cmd := exec.Command(setup, "-install", "-silent")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if cmd.Run() != nil || !pawnIOReady() {
		return setupPawnIOFailed
	}
	return setupOK
}

// installService registers the service, or updates it, and starts it.
func installService(m *mgr.Mgr, exe string) error {
	config := mgr.Config{
		DisplayName:  serviceDisplayName,
		Description:  "Sharktic의 고급 팬 제어: CPU 온도와 팬처럼 관리자 권한이 필요한 하드웨어를 읽습니다.",
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}
	s, err := m.OpenService(serviceName)
	if err == nil {
		config.BinaryPathName = syscall.EscapeArg(exe) + " " + serviceFlag
		config.ServiceType = windows.SERVICE_WIN32_OWN_PROCESS
		err = s.UpdateConfig(config)
	} else {
		s, err = m.CreateService(serviceName, exe, config, serviceFlag)
	}
	if err != nil {
		return err
	}
	defer s.Close()
	// Restart it if it dies, a few times.
	s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.NoAction},
	}, 24*60*60)
	if err := s.Start(); err != nil {
		return err
	}
	return waitService(s, svc.Running)
}

func disableAdvanced(removePawnIO bool) int {
	m, err := mgr.Connect()
	if err != nil {
		return setupRemoveFailed
	}
	defer m.Disconnect()
	if s, err := m.OpenService(serviceName); err == nil {
		stopService(s)
		err = s.Delete()
		s.Close()
		if err != nil {
			return setupRemoveFailed
		}
	}
	if dir, err := serviceDir(); err == nil {
		// The stopped service's process may take a moment to let go of its file.
		for range 10 {
			if os.RemoveAll(dir) == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	if removePawnIO {
		if _, dir, ok := pawnIOInstalled(); ok {
			cmd := exec.Command(filepath.Join(dir, "uninstall.exe"), "-uninstall", "-silent")
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			if cmd.Run() != nil {
				return setupRemoveFailed
			}
		}
	}
	return setupOK
}

func stopService(s *mgr.Service) {
	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		s.Control(svc.Stop)
		waitService(s, svc.Stopped)
	}
}

func waitService(s *mgr.Service, want svc.State) error {
	for range 50 {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("the service did not change state")
}

// runElevated starts this program again with administrator rights, after a
// UAC prompt, and waits for its exit code. It is called off the main thread.
func runElevated(args string) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(self)
	params, _ := windows.UTF16PtrFromString(args)
	info := shellExecuteInfo{
		mask: seeMaskNoCloseProcess | seeMaskNoAsync,
		verb: verb, file: file, params: params,
		show: windows.SW_HIDE,
	}
	info.size = uint32(unsafe.Sizeof(info))
	if r, _, err := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return 0, errCancelled
		}
		return 0, err
	}
	defer windows.CloseHandle(info.process)
	if _, err := windows.WaitForSingleObject(info.process, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return 0, err
	}
	return int(code), nil
}

var errCancelled = errors.New("UAC was cancelled")

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

const (
	seeMaskNoCloseProcess = 0x40
	seeMaskNoAsync        = 0x100
)

// shellExecuteInfo is SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	size      uint32
	mask      uint32
	hwnd      uintptr
	verb      *uint16
	file      *uint16
	params    *uint16
	dir       *uint16
	show      int32
	instApp   uintptr
	idList    uintptr
	class     *uint16
	keyClass  uintptr
	hotKey    uint32
	iconOrMon uintptr
	process   windows.Handle
}
