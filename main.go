package main

import (
	"cmp"
	"fmt"
	"image/color"
	"log"
	"math"
	"slices"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	appName     = "Sharktic"
	sampleEvery = 2 * time.Second

	// restoreAgainAfter is when a restored fan setting is set once more.
	restoreAgainAfter = time.Minute
	gb                = 1 << 30
	mb                = 1 << 20
	topCount          = 3
	panelHeight       = 540
	modesHeight       = 230 // what the fan section adds to the panel
)

// ProcessUse is the use of a running program: of its Count processes, added up.
type ProcessUse struct {
	Name   string
	Count  int
	CPU    float64 // in percent of all CPUs
	Memory uint64  // private working set, in bytes
	id     uintptr // of one of its processes
}

// hiddenPrograms are the kernel's own memory holders, which the Processes tab
// of Task Manager does not list.
var hiddenPrograms = map[string]bool{
	"vmmem":              true,
	"vmmemWSL":           true,
	"Memory Compression": true,
	"Secure System":      true,
}

// visiblePrograms removes the hidden programs from a list.
func visiblePrograms(list []ProcessUse) []ProcessUse {
	return slices.DeleteFunc(list, func(u ProcessUse) bool { return hiddenPrograms[u.Name] })
}

// Snapshot is one reading of the sensors. A Has field is false where a
// sensor gave no value.
type Snapshot struct {
	CPUUsage float64
	HasCPU   bool
	// Temp is the PC's temperature: the hottest ACPI thermal zone.
	Temp    float64
	HasTemp bool

	MemUsed, MemTotal uint64

	GPUUsage            float64
	HasGPU              bool
	GPUTemp             float64
	HasGPUTemp          bool
	VRAMUsed, VRAMTotal uint64 // VRAMTotal is 0 where the size is unknown

	CPUFan, GPUFan       int // in RPM
	HasCPUFan, HasGPUFan bool

	// TopCPU and TopMemory are the programs that use the most.
	TopCPU, TopMemory []ProcessUse
}

// meter is one tray icon, which shows one value of a Snapshot.
type meter struct {
	id, label string
	read      func(Snapshot) (float64, bool)
	color     func(value int, ok bool) color.NRGBA

	tray  *mygo.Tray
	value int
	ok    bool
}

// newMeters returns the icons. The first keeps the ID of the app's first
// version, and with it the place on the taskbar the user gave it.
func newMeters() []*meter {
	return []*meter{
		{
			id: "sharktic", label: "PC", color: tempColor,
			read: func(s Snapshot) (float64, bool) { return s.Temp, s.HasTemp },
		},
		{
			id: "sharktic-cpu", label: "CPU", color: usageColor(color.NRGBA{R: 30, G: 100, B: 200, A: 255}),
			read: func(s Snapshot) (float64, bool) { return s.CPUUsage, s.HasCPU },
		},
		{
			id: "sharktic-gpu", label: "GPU", color: usageColor(color.NRGBA{R: 120, G: 60, B: 190, A: 255}),
			read: func(s Snapshot) (float64, bool) { return s.GPUUsage, s.HasGPU },
		},
		{
			id: "sharktic-ram", label: "RAM", color: usageColor(color.NRGBA{R: 0, G: 125, B: 130, A: 255}),
			read: func(s Snapshot) (float64, bool) { return 100 * ratio(s.MemUsed, s.MemTotal), s.MemTotal > 0 },
		},
	}
}

// All state is read and changed on the main thread.
type app struct {
	meters      []*meter
	panel       *mygo.Window // the open panel, or nil
	curveWindow *mygo.Window // the open window of the fan curves, or nil
	snap        Snapshot

	// fans is the way to the fans of this PC, or nil.
	fans fanBackend
	// modeChoice is the index of the mode last set, or customChoice, or noChoice
	// before the first: the firmware does not tell which one is on.
	modeChoice int
	modeFailed bool

	// curves are what the window is editing, and appliedCurves what was last set
	// or saved.
	curves, appliedCurves [2]fanCurve
	curveTab              int
	curveStatus           string

	// choiceFile keeps the choice for the next start, or is "" to keep nothing.
	choiceFile string
	// setOpenAtLogin starts the app at login, where it can set fans, and openAtLogin
	// is what it was last known to be.
	setOpenAtLogin func(open bool) error
	openAtLogin    bool

	// diagnose reads the report of the diagnosis, which sets nothing.
	diagnose func() diagnosis
}

// noChoice and customChoice are the choices that are not a performance mode: none
// yet, and the curves.
const (
	noChoice     = -1
	customChoice = -2
)

// modes lists the performance modes, if any.
func (a *app) modes() []fanMode {
	if a.fans == nil {
		return nil
	}
	return a.fans.modes()
}

// hasCurves tells whether the fan curves can be set.
func (a *app) hasCurves() bool { return a.fans != nil && a.fans.hasCurves() }

func percent(v float64, ok bool) string {
	if !ok {
		return "--"
	}
	return fmt.Sprintf("%.0f%%", v)
}

func degrees(v float64, ok bool) string {
	if !ok {
		return "--"
	}
	return fmt.Sprintf("%.0f°C", v)
}

func ratio(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total)
}

// memoryLine tells the use of a memory, as "사용 9.1 GB · 여유 6.7 GB", or as
// "사용 9.1 GB" if its size is unknown.
func memoryLine(used, total uint64) string {
	if total == 0 {
		if used == 0 {
			return "--"
		}
		return fmt.Sprintf("사용 %.1f GB", float64(used)/gb)
	}
	return fmt.Sprintf("사용 %.1f GB · 여유 %.1f GB", float64(used)/gb, float64(total-used)/gb)
}

// usageLine tells the use of a processor, as "사용 12% · 여유 88%".
func usageLine(v float64, ok bool) string {
	if !ok {
		return "--"
	}
	return fmt.Sprintf("사용 %.0f%% · 여유 %.0f%%", v, 100-v)
}

// memorySize tells an amount of memory, as "512 MB" or "1.2 GB".
func memorySize(n uint64) string {
	if n >= gb {
		return fmt.Sprintf("%.1f GB", float64(n)/gb)
	}
	return fmt.Sprintf("%d MB", n/mb)
}

// rpm tells the speed of a fan.
func rpm(v int, ok bool) string {
	if !ok {
		return "--"
	}
	return fmt.Sprintf("%d rpm", v)
}

// displayName tells a program, and how many processes it runs if several.
func (u ProcessUse) displayName() string {
	if u.Count > 1 {
		return fmt.Sprintf("%s (%d)", u.Name, u.Count)
	}
	return u.Name
}

// top returns the n programs with the most of a value, the most first.
func top[T cmp.Ordered](list []ProcessUse, n int, value func(ProcessUse) T) []ProcessUse {
	sorted := slices.Clone(list)
	slices.SortFunc(sorted, func(a, b ProcessUse) int { return cmp.Compare(value(b), value(a)) })
	return sorted[:min(n, len(sorted))]
}

// scaleToUsage scales the use of the programs, which counts time alone, so that
// they add up to the total, which counts clock speed as well, as in Task Manager.
func scaleToUsage(list []ProcessUse, usage float64) {
	sum := 0.0
	for _, u := range list {
		sum += u.CPU
	}
	if sum <= 0 {
		return
	}
	for i := range list {
		list[i].CPU *= usage / sum
	}
}

func (s Snapshot) toolTip() string {
	ram := "--"
	if s.MemTotal > 0 {
		ram = fmt.Sprintf("%.1f / %.1f GB", float64(s.MemUsed)/gb, float64(s.MemTotal)/gb)
	}
	return fmt.Sprintf("PC 온도 %s\nCPU %s\nGPU %s · %s\nRAM %s",
		degrees(s.Temp, s.HasTemp), percent(s.CPUUsage, s.HasCPU),
		percent(s.GPUUsage, s.HasGPU), degrees(s.GPUTemp, s.HasGPUTemp), ram)
}

// chooseMode sets a choice, by its index. The one already chosen can be set
// again, as a key of the laptop may have changed it.
func (a *app) chooseMode(i int) {
	var err error
	switch i {
	case customChoice:
		err = a.fans.setCurves(a.appliedCurves[cpuFanTab], a.appliedCurves[gpuFanTab])
	default:
		err = a.fans.setMode(a.modes()[i].value)
	}
	if err != nil {
		log.Println("fan control:", err)
		a.modeFailed = true
		if i == customChoice {
			a.modeChoice = noChoice // it went back to a mode
		}
		return
	}
	a.modeChoice, a.modeFailed = i, false
	a.saveChoice(i)
}

// hasFanControl tells whether the firmware has modes or curves to set.
func (a *app) hasFanControl() bool { return len(a.modes()) > 0 || a.hasCurves() }

// fanSection shows the fans, and the performance modes and the curves where
// there are any.
func (a *app) fanSection(c *ui.Context) {
	s, t := a.snap, c.Theme()
	if !a.hasFanControl() && !s.HasCPUFan && !s.HasGPUFan {
		return
	}
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, "팬").Bold()
		ui.Text(c, "CPU "+rpm(s.CPUFan, s.HasCPUFan)+" · GPU "+rpm(s.GPUFan, s.HasGPUFan)).FontSize(12).TextColor(t.TextMuted)
		if !a.hasFanControl() {
			return
		}
		// Buttons, as a segmented control always has one chosen.
		button := func(i int, label string) {
			// Little padding, as they share the width of the panel.
			b := ui.Button(c.Key(label), label).Grow(1).Padding(4).OnClick(func() { a.chooseMode(i) })
			if i == a.modeChoice {
				b.Background(t.SurfacePressed).Border(1, t.Accent)
			}
		}
		if len(a.modes()) > 0 {
			ui.Row(c).Gap(4).Children(func() {
				for i, m := range a.modes() {
					button(i, m.label)
				}
			})
		}
		if a.hasCurves() {
			// Never marked as chosen, as it opens a window: the note below tells if its curve is on.
			ui.Button(c.Key("advanced"), "고급 설정 (온도별 팬 곡선)").OnClick(a.openCurveWindow)
		}
		note := func(text string) { ui.Text(c, text).FontSize(12).TextColor(t.TextMuted) }
		switch {
		case a.modeChoice == customChoice && !a.fans.canLeaveCurves():
			note("고급 설정의 곡선 사용 중. 이 기기는 앱에서 펌웨어 곡선으로 되돌릴 수 없어, 앱을 종료해도 곡선이 유지됩니다")
		case a.modeChoice == customChoice:
			note("고급 설정의 곡선 사용 중")
		default:
			note("실제 속도는 온도에 따라 펌웨어가 조절합니다")
		}
		switch {
		case a.modeFailed:
			note("적용하지 못했습니다")
		case a.modeChoice == noChoice && len(a.modes()) > 0:
			note("현재 모드는 읽을 수 없어 고른 뒤에 표시됩니다")
		}
		if a.setOpenAtLogin != nil && ui.Checkbox(c, &a.openAtLogin, "컴퓨터를 켤 때 이 팬 설정 적용").Changed() {
			if err := a.setOpenAtLogin(a.openAtLogin); err != nil {
				log.Println("open at login:", err)
				a.openAtLogin = !a.openAtLogin
			}
		}
	})
}

func (a *app) view(c *ui.Context) {
	t := c.Theme()
	s := a.snap
	// A value that is not there has no bar, which would look like a real 0%.
	section := func(title string, use float64, ok bool, lines ...string) {
		ui.Column(c).Gap(4).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, title).Bold().Grow(1)
				ui.Text(c, percent(use, ok)).Bold()
			})
			if ok {
				ui.Progress(c, use/100)
			}
			for _, line := range lines {
				ui.Text(c, line).FontSize(12).TextColor(t.TextMuted)
			}
		})
	}
	ranking := func(title string, list []ProcessUse, value func(ProcessUse) string) {
		ui.Column(c).Gap(2).Children(func() {
			ui.Text(c, title).FontSize(12).Bold()
			if len(list) == 0 {
				ui.Text(c, "--").FontSize(12).TextColor(t.TextMuted)
			}
			for _, u := range list {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, u.displayName()).FontSize(12).TextColor(t.TextMuted).Grow(1).MaxLines(1).Ellipsis("…")
					ui.Text(c, value(u)).FontSize(12)
				})
			}
		})
	}
	c.Root().Background(ui.Transparent)
	ui.Column(c).Fill().Background(t.Surface).Padding(16).Gap(12).Children(func() {
		// Scrolls with large text or long lists, so that the quit button stays.
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Gap(12).Children(func() {
				ui.Column(c).Gap(4).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "PC 온도").Bold().Grow(1)
						ui.Text(c, degrees(s.Temp, s.HasTemp)).Bold()
					})
					ui.Text(c, "ACPI 열 영역 · 부품별 센서가 아님").FontSize(12).TextColor(t.TextMuted)
				})
				section("CPU", s.CPUUsage, s.HasCPU,
					usageLine(s.CPUUsage, s.HasCPU))
				section("GPU", s.GPUUsage, s.HasGPU,
					usageLine(s.GPUUsage, s.HasGPU),
					"온도 "+degrees(s.GPUTemp, s.HasGPUTemp),
					"전용 메모리 "+memoryLine(s.VRAMUsed, s.VRAMTotal))
				section("메모리", 100*ratio(s.MemUsed, s.MemTotal), s.MemTotal > 0,
					memoryLine(s.MemUsed, s.MemTotal))
				a.fanSection(c)
				ui.Divider(c)
				ranking("CPU 점유 상위", s.TopCPU, func(u ProcessUse) string { return fmt.Sprintf("%.1f%%", u.CPU) })
				ranking("메모리 점유 상위", s.TopMemory, func(u ProcessUse) string { return memorySize(u.Memory) })
			})
		})
		ui.Row(c).Children(func() {
			ui.Spacer(c)
			ui.Button(c.Key("quit"), "종료").OnClick(mygo.App.Quit)
		})
	})
}

// apply shows a new reading on the icons, their tooltips, and the open panel.
func (a *app) apply(s Snapshot) {
	a.snap = s
	tip := s.toolTip()
	for _, m := range a.meters {
		v, ok := m.read(s)
		value := int(math.Round(v))
		if value != m.value || ok != m.ok {
			m.value, m.ok = value, ok
			if err := m.tray.SetIcon(trayIcon(m.label, value, ok, m.color(value, ok)), false); err != nil {
				log.Println("tray icon:", err)
			}
		}
		m.tray.SetToolTip(tip)
	}
	if a.panel != nil {
		a.panel.Update(func() {})
	}
	if a.curveWindow != nil {
		a.curveWindow.Update(func() {})
	}
}

// openCurveWindow opens the window of the fan curves, or brings it up.
func (a *app) openCurveWindow() {
	if a.panel != nil {
		a.panel.Close()
	}
	if a.curveWindow != nil {
		a.curveWindow.Focus()
		return
	}
	a.curveWindow = mygo.NewWindow(mygo.WindowOptions{
		Title:     "고급 팬 설정",
		Width:     860,
		Height:    600,
		MinWidth:  760,
		MinHeight: 560,
		Content:   ui.View(a.curveView),
	})
	a.curveWindow.OnClosed(func() { a.curveWindow = nil })
}

// toggle opens the panel next to an icon, or closes it.
func (a *app) toggle(tray *mygo.Tray) {
	if a.panel != nil {
		a.panel.Close()
		return
	}
	height := panelHeight
	if a.hasFanControl() {
		height += modesHeight
	}
	if a.setOpenAtLogin != nil {
		// Task Manager can turn it off too.
		a.openAtLogin = mygo.App.OpenAtLogin()
	}
	a.panel = mygo.NewFlyout(mygo.FlyoutOptions{
		Tray:      tray,
		Placement: mygo.PlacementBottom,
		Gap:       4,
		Width:     300,
		Height:    height,
		Focusable: true,
		Shadow:    true,
		Content:   ui.View(a.view),
	})
	a.panel.OnClosed(func() { a.panel = nil })
}

// addTray makes the icon of a meter, without a reading yet.
func (a *app) addTray(m *meter) {
	var err error
	m.tray, err = mygo.NewTray(mygo.TrayOptions{
		ID:      m.id,
		Icon:    trayIcon(m.label, 0, false, m.color(0, false)),
		ToolTip: appName,
	})
	if err != nil {
		log.Fatal("tray: ", err)
	}
	// Asking where it is adds it now, as NewTray alone adds the icons later, in
	// no particular order.
	m.tray.Bounds()
	openAtLogin := &mygo.MenuItem{
		Label: "로그인 시 자동 실행",
		Type:  mygo.MenuItemCheckbox,
		Click: func(item *mygo.MenuItem, _ *mygo.Window) {
			if err := mygo.App.SetOpenAtLogin(item.IsChecked()); err != nil {
				log.Println("open at login:", err)
			}
		},
	}
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "자세히 보기", Click: func(*mygo.MenuItem, *mygo.Window) { a.toggle(m.tray) }},
		openAtLogin,
		{Label: "진단 정보 복사", Click: func(*mygo.MenuItem, *mygo.Window) { a.copyDiagnosis() }},
		mygo.Separator(),
		{Label: "종료", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	})
	m.tray.OnClick(func() { a.toggle(m.tray) })
	m.tray.OnRightClick(func() {
		if a.panel != nil {
			a.panel.Close()
		}
		// Task Manager can turn it off too, and another icon's menu can change it.
		openAtLogin.SetChecked(mygo.App.OpenAtLogin())
		// The menu for this popup only, so that clicks keep toggling the panel.
		m.tray.SetMenu(menu)
		defer m.tray.SetMenu(nil)
		m.tray.PopUpMenu()
	})
}

// copyDiagnosis puts the report of the diagnosis on the clipboard, for the user
// to paste in a message, and tells so.
func (a *app) copyDiagnosis() {
	mygo.Clipboard.WriteText(a.diagnose().text())
	n := mygo.NewNotification(mygo.NotificationOptions{
		ID:     "diagnosis",
		Title:  "진단 정보를 복사했습니다",
		Body:   "메시지나 이슈에 붙여 넣어 보내 주세요. 모델명, BIOS 버전, 팬 관련 펌웨어 값만 들어 있습니다.",
		Silent: true,
	})
	if err := n.Show(); err != nil {
		log.Println("notification:", err)
	}
}

func (a *app) ready() {
	clearDeadTrayIcons()
	moveOldSettings()
	if takeOldLogin() {
		if err := mygo.App.SetOpenAtLogin(true); err != nil {
			log.Println("open at login:", err)
		}
	}
	fans := detectFans()
	a.fans = fans
	a.diagnose = func() diagnosis { return diagnose(fans, appName+" "+mygo.App.Version()) }
	if a.hasFanControl() {
		a.modeChoice = noChoice
		a.appliedCurves = [2]fanCurve{defaultCurve, defaultCurve}
		if curves, ok := loadCurves(curvesPath()); ok {
			a.appliedCurves = curves
		}
		a.curves = a.appliedCurves
		a.choiceFile = choicePath()
		a.setOpenAtLogin = mygo.App.SetOpenAtLogin
		if a.restoreChoice() {
			// The vendor's services start around the same time, and may set their own mode.
			time.AfterFunc(restoreAgainAfter, func() {
				mygo.RunOnMain(func() {
					if a.modeChoice != noChoice {
						fans.forgetCurves()
						a.chooseMode(a.modeChoice)
					}
				})
			})
		}
		// The firmware may have gone back to its own curves during the sleep.
		mygo.Power.OnResume(func() {
			mygo.RunOnMain(func() {
				if a.modeChoice == customChoice {
					fans.forgetCurves()
					a.chooseMode(a.modeChoice)
				}
			})
		})
	}
	a.meters = newMeters()
	for _, m := range a.meters {
		a.addTray(m)
	}
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		if fans != nil {
			fans.leaveCurves()
		}
		for _, m := range a.meters {
			m.tray.Destroy()
		}
	})

	go func() {
		sampler := NewSampler()
		sampler.fans = fans
		for range time.Tick(sampleEvery) {
			s := sampler.Read()
			mygo.RunOnMain(func() { a.apply(s) })
		}
	}()
}

func main() {
	a := &app{}
	mygo.App.SetName(appName)
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	// The panel is the app's only window: keep running as it closes.
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.WhenReady(a.ready)
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
