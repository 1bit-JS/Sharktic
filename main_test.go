package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestViewShowsReading(t *testing.T) {
	a := &app{snap: Snapshot{
		CPUUsage: 12, HasCPU: true, Temp: 67, HasTemp: true,
		MemUsed: 4 * gb, MemTotal: 16 * gb,
		GPUUsage: 30, HasGPU: true, GPUTemp: 43, HasGPUTemp: true,
		VRAMUsed: 1 * gb, VRAMTotal: 4 * gb,
		TopCPU:    []ProcessUse{{Name: "chrome", Count: 23, CPU: 6.25}},
		TopMemory: []ProcessUse{{Name: "Code", Count: 1, Memory: 2 * gb}, {Name: "steam", Count: 1, Memory: 512 * mb}},
	}}
	tt := ui.NewTester(a.view, 300, panelHeight)
	for _, want := range []string{
		"사용 12% · 여유 88%", "PC 온도", "ACPI 열 영역", "67°C", "온도 43°C",
		"전용 메모리 사용 1.0 GB · 여유 3.0 GB", "사용 4.0 GB · 여유 12.0 GB", "25%",
		"chrome (23)", "6.2%", "Code", "2.0 GB", "512 MB",
	} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
}

func TestViewWithoutSensors(t *testing.T) {
	tt := ui.NewTester((&app{}).view, 300, panelHeight)
	if !tt.HasText("온도 --") {
		t.Errorf("texts %q", tt.Texts())
	}
	if tt.HasText("%") {
		t.Errorf("a percentage without a reading in %q", tt.Texts())
	}
}

func TestViewKeepsARealZero(t *testing.T) {
	a := &app{snap: Snapshot{HasCPU: true, HasGPU: true, MemTotal: gb}}
	if tt := ui.NewTester(a.view, 300, panelHeight); !tt.HasText("사용 0% · 여유 100%") {
		t.Errorf("texts %q", tt.Texts())
	}
}

func TestFanRPM(t *testing.T) {
	for _, tc := range []struct {
		raw  uint32
		want int
		ok   bool
	}{
		{0x00010028, 4000, true},
		{0x00010000, 0, true}, // a fan that stands still
		{0x00010079, 0, false},
		{0, 0, false},
		{0xFFFFFFFE, 0, false}, // not supported
		{0x00000028, 0, false},
	} {
		if got, ok := fanRPM(tc.raw); got != tc.want || ok != tc.ok {
			t.Errorf("0x%08X: %v, %v", tc.raw, got, ok)
		}
	}
}

func TestViewWithoutFans(t *testing.T) {
	if tt := ui.NewTester((&app{}).view, 300, panelHeight); tt.HasText("팬") || tt.HasText("균형") {
		t.Errorf("texts %q", tt.Texts())
	}
}

func TestViewShowsFansWithoutModes(t *testing.T) {
	a := &app{snap: Snapshot{CPUFan: 3800, HasCPUFan: true}}
	tt := ui.NewTester(a.view, 300, panelHeight)
	if !tt.HasText("CPU 3800 rpm · GPU --") || tt.HasText("균형") {
		t.Errorf("texts %q", tt.Texts())
	}
}

func TestChoosingAMode(t *testing.T) {
	var set []uint32
	var fail error
	a := &app{modeChoice: -1, fans: &fakeFans{setModeFn: func(mode uint32) error {
		set = append(set, mode)
		return fail
	}}}
	tt := ui.NewTester(a.view, 300, panelHeight+modesHeight)
	if !tt.HasText("현재 모드는 읽을 수 없어") || tt.HasText("수동") || len(set) != 0 {
		t.Fatalf("before a choice: set %v, texts %q", set, tt.Texts())
	}
	if err := tt.Click("터보"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(set) != 1 || set[0] != 1 || a.modeChoice != 2 || tt.HasText("현재 모드는 읽을 수 없어") {
		t.Errorf("turbo: set %v, choice %d, texts %q", set, a.modeChoice, tt.Texts())
	}

	fail = errors.New("refused")
	if err := tt.Click("조용함"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(set) != 2 || set[1] != 2 || a.modeChoice != 2 || !tt.HasText("적용하지 못했습니다") {
		t.Errorf("refused: set %v, choice %d, texts %q", set, a.modeChoice, tt.Texts())
	}

	fail = nil
	if err := tt.Click("터보"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(set) != 3 || set[2] != 1 || a.modeChoice != 2 || tt.HasText("적용하지 못했습니다") {
		t.Errorf("turbo again: set %v, choice %d, texts %q", set, a.modeChoice, tt.Texts())
	}
}

func TestCurveProblems(t *testing.T) {
	if p := defaultCurve.problem(); p != "" {
		t.Errorf("default: %s", p)
	}
	for name, c := range map[string]fanCurve{
		"same temperature": {{40, 25}, {40, 30}, {60, 40}, {65, 45}, {70, 55}, {75, 70}, {80, 85}, {85, 100}},
		"falling fan":      {{40, 50}, {50, 30}, {60, 40}, {65, 45}, {70, 55}, {75, 70}, {80, 85}, {85, 100}},
		"fan stopped":      {{40, 0}, {50, 0}, {60, 0}, {65, 0}, {70, 0}, {75, 0}, {80, 0}, {85, 0}},
		"fan over 100":     {{40, 25}, {50, 30}, {60, 40}, {65, 45}, {70, 55}, {75, 70}, {80, 85}, {85, 200}},
		"too cold":         {{20, 25}, {50, 30}, {60, 40}, {65, 45}, {70, 55}, {75, 70}, {80, 85}, {85, 100}},
		"too hot":          {{40, 25}, {50, 30}, {60, 40}, {65, 45}, {70, 55}, {75, 70}, {80, 85}, {120, 100}},
	} {
		if c.problem() == "" {
			t.Errorf("%s: no problem", name)
		}
	}
}

func TestCurveWarnings(t *testing.T) {
	if w := defaultCurve.warning(); w != "" {
		t.Errorf("default: %s", w)
	}
	for name, c := range map[string]fanCurve{
		"low at 70":     {{40, 25}, {50, 30}, {60, 40}, {65, 45}, {70, 50}, {75, 70}, {80, 85}, {85, 100}},
		"no 100 by 85":  {{40, 25}, {50, 30}, {60, 40}, {65, 45}, {70, 55}, {75, 70}, {80, 85}, {90, 100}},
		"all low":       {{40, 20}, {50, 20}, {60, 20}, {65, 20}, {70, 20}, {75, 20}, {80, 20}, {85, 20}},
		"first too hot": {{71, 55}, {72, 70}, {73, 85}, {74, 100}, {75, 100}, {76, 100}, {77, 100}, {78, 100}},
		// Like the BIOS's curve: 80% at 80°C, 100% only at 100°C.
		"like the BIOS": {{30, 30}, {35, 30}, {40, 50}, {50, 50}, {60, 60}, {70, 70}, {80, 80}, {100, 100}},
	} {
		if c.warning() == "" {
			t.Errorf("%s: no warning", name)
		}
		if p := c.problem(); p != "" {
			t.Errorf("%s: cannot be applied: %s", name, p)
		}
	}
}

func TestCurveBytes(t *testing.T) {
	want := []byte{40, 50, 60, 65, 70, 75, 80, 85, 25, 30, 40, 45, 55, 70, 85, 100}
	if got := defaultCurve.bytes(); !bytes.Equal(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestCurvesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "fan-curves.json")
	if _, ok := loadCurves(path); ok {
		t.Error("loaded a missing file")
	}
	curves := [2]fanCurve{defaultCurve, defaultCurve}
	curves[gpuFanTab][0].Level = 21
	if err := saveCurves(path, curves); err != nil {
		t.Fatal(err)
	}
	if got, ok := loadCurves(path); !ok || got != curves {
		t.Errorf("loaded %v, %v", got, ok)
	}
	curves[cpuFanTab][4].Level = 50 // below a floor, which only warns
	if err := saveCurves(path, curves); err != nil {
		t.Fatal(err)
	}
	if got, ok := loadCurves(path); !ok || got != curves {
		t.Errorf("loaded a curve below a floor as %v, %v", got, ok)
	}
	for name, content := range map[string]string{
		"broken": "{",
		"unsafe": `[[{"Temp":40,"Level":0},{"Temp":50,"Level":0},{"Temp":60,"Level":0},{"Temp":65,"Level":0},{"Temp":70,"Level":0},{"Temp":75,"Level":0},{"Temp":80,"Level":0},{"Temp":85,"Level":0}],[]]`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := loadCurves(path); ok {
			t.Errorf("loaded a file that is %s", name)
		}
	}
}

func TestRestoringAChoice(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	var modes []uint32
	var curves int
	newApp := func() *app {
		return &app{modeChoice: -1, choiceFile: choicePath(), fans: &fakeFans{
			setModeFn:   func(m uint32) error { modes = append(modes, m); return nil },
			setCurvesFn: func(c, g fanCurve) error { curves++; return nil },
		}}
	}
	reset := func() { modes, curves = nil, 0 }

	// Nothing was chosen: nothing reaches the firmware.
	if a := newApp(); a.restoreChoice() || a.modeChoice != -1 || modes != nil {
		t.Errorf("restored without a file: choice %d, modes %v", a.modeChoice, modes)
	}

	// Each choice comes back, a mode by its value.
	for i, m := range asusModes {
		newApp().chooseMode(i)
		reset()
		a := newApp()
		if !a.restoreChoice() || a.modeChoice != i || len(modes) != 1 || modes[0] != m.value {
			t.Errorf("%s: choice %d, modes %v", m.label, a.modeChoice, modes)
		}
	}
	// A curve needs its file.
	newApp().chooseMode(customChoice)
	reset()
	if a := newApp(); a.restoreChoice() || curves != 0 {
		t.Errorf("restored a curve without its file: %d", curves)
	}
	if err := saveCurves(curvesPath(), [2]fanCurve{defaultCurve, defaultCurve}); err != nil {
		t.Fatal(err)
	}
	if a := newApp(); !a.restoreChoice() || a.modeChoice != customChoice || curves != 1 {
		t.Errorf("curve: choice %d, set %d", a.modeChoice, curves)
	}

	// A failure is no choice, and does not replace the one that is kept.
	failing := newApp()
	failing.fans.(*fakeFans).setModeFn = func(uint32) error { return errors.New("refused") }
	failing.chooseMode(0)
	reset()
	if a := newApp(); !a.restoreChoice() || a.modeChoice != customChoice {
		t.Errorf("after a failure: choice %d", a.modeChoice)
	}

	// A file edited by hand sets nothing else: 3 is full speed. The manual level of
	// earlier versions is gone, and leaves the firmware as it is.
	for _, content := range []string{"{", `{"Kind":"mode","Mode":3}`, `{"Kind":"manual","Level":50}`, `{"Kind":"other"}`} {
		if err := os.WriteFile(choicePath(), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		reset()
		if a := newApp(); a.restoreChoice() || modes != nil || curves != 0 {
			t.Errorf("%s: modes %v, curves %d", content, modes, curves)
		}
	}
}

func TestAppWithoutAFileKeepsNoChoice(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	a := &app{fans: modesAndCurves()}
	a.chooseMode(0)
	if _, err := os.Stat(choicePath()); err == nil {
		t.Error("kept a choice")
	}
}

func TestOpenAtLoginCheckbox(t *testing.T) {
	const label = "컴퓨터를 켤 때 이 팬 설정 적용"
	var fail error
	var set []bool
	a := &app{modeChoice: -1, fans: modesAndCurves(),
		setOpenAtLogin: func(open bool) error { set = append(set, open); return fail },
	}
	tt := ui.NewTester(a.view, 300, panelHeight+modesHeight)
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.openAtLogin || len(set) != 1 || !set[0] {
		t.Errorf("on: %v, set %v", a.openAtLogin, set)
	}
	fail = errors.New("refused")
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.openAtLogin || len(set) != 2 || set[1] {
		t.Errorf("refused: %v, set %v", a.openAtLogin, set)
	}
}

// Typing while the button is down, as a touchpad's late release lets it, must
// keep what is typed. It needs mygo's fix of #1159.
func TestTypingWhileThePressIsHeld(t *testing.T) {
	a := &app{modeChoice: -1,
		curves: [2]fanCurve{defaultCurve, defaultCurve}, appliedCurves: [2]fanCurve{defaultCurve, defaultCurve},
	}
	tt := ui.NewTester(a.curveView, 860, 600)
	input, _ := tt.Find("8구간 온도")
	x, y := input.X+20, input.Y+input.H/2
	tt.Press(x, y)
	tt.Release(x, y)
	tt.Press(x, y)
	tt.Frame()
	tt.TypeKey(0, ui.Key9, "9")
	tt.Frame()
	tt.TypeKey(0, ui.Key0, "0")
	tt.Frame()
	tt.Release(x, y)
	tt.Frame()
	if got := a.curves[cpuFanTab][7].Temp; got != 90 {
		t.Errorf("temperature %v", got)
	}
}

func TestCurveWindow(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir()) // where the curves get saved
	var cpu, gpu []byte
	var fail error
	a := &app{modeChoice: -1,
		fans: &fakeFans{
			setModeFn:   func(uint32) error { return nil },
			setCurvesFn: func(c, g fanCurve) error { cpu, gpu = c.bytes(), g.bytes(); return fail },
		},
		curves: [2]fanCurve{defaultCurve, defaultCurve}, appliedCurves: [2]fanCurve{defaultCurve, defaultCurve},
	}
	tt := ui.NewTester(a.curveView, 860, 600)
	for i := 1; i <= 8; i++ {
		if _, ok := tt.Find(fmt.Sprintf("%d구간 온도", i)); !ok {
			t.Errorf("no input of section %d", i)
		}
		if _, ok := tt.Find(fmt.Sprintf("%d구간 팬 속도", i)); !ok {
			t.Errorf("no level of section %d", i)
		}
	}

	// Editing does not reach the firmware.
	input, _ := tt.Find("1구간 팬 속도") // with its buttons: the text is on the left
	tt.ClickAt(input.X+20, input.Y+input.H/2)
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("28")
	tt.Frame()
	if a.curves[cpuFanTab][0].Level != 28 || cpu != nil {
		t.Fatalf("level %v, cpu %v", a.curves[cpuFanTab][0].Level, cpu)
	}

	// A curve the firmware should not get cannot be applied, and says why.
	a.curves[cpuFanTab][1].Temp = 40
	tt.Frame()
	if !tt.HasText("CPU 팬: 2구간의 온도는") {
		t.Errorf("texts %q", tt.Texts())
	}
	if err := tt.Click("적용"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if cpu != nil || a.modeChoice != -1 {
		t.Errorf("applied a wrong curve: %v, choice %d", cpu, a.modeChoice)
	}
	a.curves[cpuFanTab][1].Temp = 50

	// A curve below a floor warns, and can be applied.
	a.curves[cpuFanTab][4].Level = 50
	tt.Frame()
	if !tt.HasText("CPU 팬: 주의: 70°C에서") {
		t.Errorf("texts %q", tt.Texts())
	}
	if err := tt.Click("적용"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if cpu == nil || cpu[8+4] != 50 || a.modeChoice != customChoice {
		t.Errorf("did not apply a curve below a floor: %v, choice %d", cpu, a.modeChoice)
	}
	cpu, gpu, a.modeChoice = nil, nil, -1
	a.curves[cpuFanTab][4].Level = 55
	tt.Frame()

	if err := tt.Click("적용"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if cpu == nil || cpu[8] != 28 || gpu[8] != 25 || a.modeChoice != customChoice || a.appliedCurves != a.curves || !tt.HasText("적용하고 저장했습니다") {
		t.Errorf("applied: cpu %v, gpu %v, choice %d, texts %q", cpu, gpu, a.modeChoice, tt.Texts())
	}
	if got, ok := loadCurves(curvesPath()); !ok || got != a.curves {
		t.Errorf("saved %v, %v", got, ok)
	}

	// Refused curves are not kept as applied.
	before := a.appliedCurves
	fail = errors.New("refused")
	a.curves[cpuFanTab][0].Level = 21
	a.applyCurves()
	tt.Frame()
	if a.appliedCurves != before || a.modeChoice != -1 || !tt.HasText("적용하지 못했습니다") {
		t.Errorf("refused: choice %d, texts %q", a.modeChoice, tt.Texts())
	}

	// Going back, copying, and the default.
	if err := tt.Click("되돌리기"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.curves != before {
		t.Errorf("undo: %v", a.curves[cpuFanTab])
	}
	if err := tt.Click("GPU 팬에 복사"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.curves[gpuFanTab][0].Level != 28 {
		t.Errorf("copy: %v", a.curves[gpuFanTab][0])
	}
	if err := tt.Click("기본값"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.curves[cpuFanTab] != defaultCurve {
		t.Errorf("default: %v", a.curves[cpuFanTab])
	}
}

func TestPanelShowsAppliedCurve(t *testing.T) {
	a := &app{modeChoice: customChoice, fans: modesAndCurves()}
	tt := ui.NewTester(a.view, 300, panelHeight+modesHeight)
	if !tt.HasText("고급 설정") || !tt.HasText("곡선 사용 중") || tt.HasText("실제 속도는") {
		t.Errorf("texts %q", tt.Texts())
	}
}

// The button opens a window, so it must not look pressed while its curve is on.
func TestAdvancedButtonNeverLooksPressed(t *testing.T) {
	render := func(choice int) (*image.RGBA, ui.Rect) {
		a := &app{modeChoice: choice, fans: modesAndCurves()}
		tt := ui.NewTester(a.view, 300, panelHeight+modesHeight)
		r, ok := tt.Find("고급 설정 (온도별 팬 곡선)")
		if !ok {
			t.Fatalf("no button in %q", tt.Texts())
		}
		return tt.Image(), r
	}
	on, r := render(customChoice)
	off, _ := render(0)
	for y := int(r.Y); y < int(r.Y+r.H); y++ {
		for x := int(r.X); x < int(r.X+r.W); x++ {
			if on.RGBAAt(x, y) != off.RGBAAt(x, y) {
				t.Fatalf("differs at %d,%d: %v and %v", x, y, on.RGBAAt(x, y), off.RGBAAt(x, y))
			}
		}
	}
}

func TestModesLeaveOutFullSpeed(t *testing.T) {
	for _, m := range asusModes {
		if m.value > 2 {
			t.Errorf("mode %+v", m)
		}
	}
}

func TestToolTipWithoutMemory(t *testing.T) {
	if tip := (Snapshot{}).toolTip(); !strings.Contains(tip, "RAM --") {
		t.Errorf("tip %q", tip)
	}
}

func TestMemoryLineOfUnknownSize(t *testing.T) {
	if got := memoryLine(2*gb, 0); got != "사용 2.0 GB" {
		t.Errorf("got %q", got)
	}
	if got := memoryLine(0, 0); got != "--" {
		t.Errorf("got %q", got)
	}
}

func TestCounterItemValid(t *testing.T) {
	for _, tc := range []struct {
		item counterItem
		want bool
	}{
		{counterItem{status: pdhCStatusValid, value: 5}, true},
		{counterItem{status: pdhCStatusNewData, value: 0}, true},
		{counterItem{status: 0xC0000BC6, value: 5}, false}, // PDH_CSTATUS_INVALID_DATA
		{counterItem{value: -1}, false},
		{counterItem{value: math.NaN()}, false},
		{counterItem{value: math.Inf(1)}, false},
	} {
		if got := tc.item.valid(); got != tc.want {
			t.Errorf("%+v: %v", tc.item, got)
		}
	}
}

func TestHottestZone(t *testing.T) {
	const invalid = 0xC0000BC6
	got, ok := hottestZone([]counterItem{
		{value: 3000},                  // 26.85°C
		{value: 3512},                  // 78.05°C
		{status: invalid, value: 4000}, // not to be used
		{value: 0},                     // no sensor
	})
	if !ok || math.Abs(got-78.05) > 0.001 {
		t.Errorf("got %v, %v", got, ok)
	}
	if _, ok := hottestZone([]counterItem{{value: 0}, {status: invalid, value: 4000}}); ok {
		t.Error("a temperature without a valid zone")
	}
	if _, ok := hottestZone(nil); ok {
		t.Error("a temperature without zones")
	}
}

func TestBusiestEngine(t *testing.T) {
	got, ok := busiestEngine([]counterItem{
		// Two processes on one engine add up.
		{name: "pid_1_luid_0x0_0xA_phys_0_eng_0_engtype_3D", value: 30},
		{name: "pid_2_luid_0x0_0xA_phys_0_eng_0_engtype_3D", value: 25},
		{name: "pid_1_luid_0x0_0xA_phys_0_eng_1_engtype_Copy", value: 40},
		// The same engine of another adapter does not.
		{name: "pid_3_luid_0x0_0xB_phys_0_eng_0_engtype_3D", value: 50},
		{name: "pid_4_luid_0x0_0xB_phys_0_eng_0_engtype_3D", status: 0xC0000BC6, value: 90},
		{name: "not an engine", value: 99},
	})
	if !ok || got != 55 {
		t.Errorf("got %v, %v", got, ok)
	}
	if got, _ := busiestEngine([]counterItem{
		{name: "pid_1_luid_0x0_0xA_phys_0_eng_0_engtype_3D", value: 70},
		{name: "pid_2_luid_0x0_0xA_phys_0_eng_0_engtype_3D", value: 70},
	}); got != 100 {
		t.Errorf("not capped: %v", got)
	}
	if _, ok := busiestEngine(nil); ok {
		t.Error("a use without engines")
	}
	if got, ok := busiestEngine([]counterItem{{name: "pid_1_luid_0x0_0xA_phys_0_eng_0_engtype_3D"}}); !ok || got != 0 {
		t.Errorf("an idle engine: %v, %v", got, ok)
	}
}

func TestDescribeRetriesWithoutAPath(t *testing.T) {
	var s processSensor
	list := []ProcessUse{{Name: "gone", id: 0xFFFFFFF0}}
	s.describe(list)
	if _, cached := s.descriptions["gone"]; cached || list[0].Name != "gone" {
		t.Errorf("descriptions %v, list %+v", s.descriptions, list)
	}
}

func TestTrayIconIsPNG(t *testing.T) {
	for _, m := range newMeters() {
		for _, tc := range []struct {
			value int
			ok    bool
		}{{0, false}, {7, true}, {67, true}, {120, true}, {-5, true}} {
			img, err := png.Decode(bytes.NewReader(trayIcon(m.label, tc.value, tc.ok, m.color(tc.value, tc.ok))))
			if err != nil {
				t.Fatal(err)
			}
			if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 32 {
				t.Errorf("size %v", b)
			}
		}
	}
}

func TestUsageColorTurnsRedWhenNearlyFull(t *testing.T) {
	base := color.NRGBA{B: 200, A: 255}
	c := usageColor(base)
	if c(89, true) != base || c(90, true) != redColor || c(0, false) != grayColor {
		t.Errorf("colors %v %v %v", c(89, true), c(90, true), c(0, false))
	}
}

func TestTopAndScale(t *testing.T) {
	list := []ProcessUse{{Name: "a", CPU: 1, Memory: 30}, {Name: "b", CPU: 3, Memory: 10}, {Name: "c", CPU: 0, Memory: 20}}
	scaleToUsage(list, 8)
	got := top(list, 2, func(u ProcessUse) float64 { return u.CPU })
	if len(got) != 2 || got[0].Name != "b" || got[0].CPU != 6 || got[1].Name != "a" || got[1].CPU != 2 {
		t.Errorf("by CPU %+v", got)
	}
	if m := top(list, 1, func(u ProcessUse) uint64 { return u.Memory }); m[0].Name != "a" {
		t.Errorf("by memory %+v", m)
	}
	if n := top(list, 9, func(u ProcessUse) uint64 { return u.Memory }); len(n) != 3 {
		t.Errorf("all %d", len(n))
	}
}

func TestVisibleProgramsDropsHidden(t *testing.T) {
	got := visiblePrograms([]ProcessUse{{Name: "vmmem"}, {Name: "chrome"}, {Name: "Memory Compression"}, {Name: "System"}})
	if len(got) != 2 || got[0].Name != "chrome" || got[1].Name != "System" {
		t.Errorf("%+v", got)
	}
}

func TestFileDescription(t *testing.T) {
	if d := fileDescription(`C:\Windows\explorer.exe`); d == "" {
		t.Error("no description of explorer")
	}
	for _, path := range []string{"", `C:\no\such\file.exe`} {
		if d := fileDescription(path); d != "" {
			t.Errorf("%q has description %q", path, d)
		}
	}
}

// The tests of real sensors are left out by go test -short: what they find
// depends on the machine.
func TestSamplerReadsThisMachine(t *testing.T) {
	if testing.Short() {
		t.Skip("reads the real sensors")
	}
	s := NewSampler()
	time.Sleep(time.Second) // the CPU counter measures between two reads
	got := s.Read()
	t.Logf("%+v", got)
	if !got.HasCPU || got.CPUUsage < 0 || got.CPUUsage > 100 {
		t.Errorf("CPU %v, %v", got.CPUUsage, got.HasCPU)
	}
	if !got.HasTemp {
		t.Log("no ACPI temperature, which is normal on many machines")
	}
	if got.MemTotal == 0 || got.MemUsed > got.MemTotal {
		t.Errorf("memory %d of %d", got.MemUsed, got.MemTotal)
	}
	if got.HasTemp && (got.Temp < 1 || got.Temp > 120) {
		t.Errorf("temperature %v", got.Temp)
	}
	if len(got.TopCPU) != topCount || len(got.TopMemory) != topCount {
		t.Fatalf("top %+v %+v", got.TopCPU, got.TopMemory)
	}
	if got.VRAMTotal > 0 && got.VRAMUsed > got.VRAMTotal {
		t.Errorf("video memory %d of %d", got.VRAMUsed, got.VRAMTotal)
	}
	// All of them use less than the machine has, and in order.
	for _, u := range got.TopCPU {
		if u.Name == "" || u.CPU < 0 || u.CPU > got.CPUUsage+0.01 || math.IsNaN(u.CPU) {
			t.Errorf("CPU of %+v, total %v", u, got.CPUUsage)
		}
	}
	for _, u := range got.TopMemory {
		if u.Memory == 0 || u.Memory > got.MemTotal || hiddenPrograms[u.Name] {
			t.Errorf("memory of %+v, total %d", u, got.MemTotal)
		}
	}
}

func TestSamplerRecoversFromLostGPU(t *testing.T) {
	if testing.Short() {
		t.Skip("reads the real sensors")
	}
	s := NewSampler()
	var first Snapshot
	s.readGPU(&first)
	if s.gpu == nil {
		t.Skip("no NVML")
	}
	// As a driver restart does. NVML counts its users: other tests have it too.
	for range 8 {
		s.gpu.shutdown.Call()
	}

	var lost Snapshot
	s.readGPU(&lost)
	if s.gpu != nil {
		t.Fatal("still uses the lost GPU")
	}
	if lost.HasGPUTemp {
		t.Errorf("a temperature from the counters: %+v", lost)
	}

	s.gpuRetryAt = time.Time{} // the time to retry has come
	var back Snapshot
	s.readGPU(&back)
	if s.gpu == nil || !back.HasGPU || !back.HasGPUTemp || back.VRAMTotal == 0 {
		t.Errorf("not recovered: %+v", back)
	}
}

// It only reads: a test must not change the modes of the machine.
func TestFansReadThisMachine(t *testing.T) {
	if testing.Short() {
		t.Skip("reads the real sensors")
	}
	fans := newASUSControl()
	if fans == nil {
		t.Skip("no ASUS control")
	}
	var got Snapshot
	fans.read(&got)
	t.Logf("CPU %d (%v), GPU %d (%v), modes %v", got.CPUFan, got.HasCPUFan, got.GPUFan, got.HasGPUFan, fans.modes())
	if got.CPUFan < 0 || got.CPUFan > atkFanMax*100 || got.GPUFan < 0 || got.GPUFan > atkFanMax*100 {
		t.Errorf("fans %+v", got)
	}
}

func TestGPUCountersReadThisMachine(t *testing.T) {
	if testing.Short() {
		t.Skip("reads the real sensors")
	}
	s := newGPUCounters()
	if s == nil {
		t.Skip("no GPU counters")
	}
	s.query.collect() // the baseline of a rate
	time.Sleep(time.Second)
	var got Snapshot
	s.read(&got)
	t.Logf("use %v (%v), dedicated memory %d", got.GPUUsage, got.HasGPU, got.VRAMUsed)
	if !got.HasGPU || got.GPUUsage < 0 || got.GPUUsage > 100 {
		t.Errorf("GPU %v, %v", got.GPUUsage, got.HasGPU)
	}
}

func TestMovingOldSettings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	write := func(path, content string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) string {
		data, _ := os.ReadFile(path)
		return string(data)
	}
	old := filepath.Join(dir, oldName)
	write(filepath.Join(old, "fan-curves.json"), "old curves")
	write(filepath.Join(old, "fan-choice.json"), "old choice")
	// A setting of the new name is newer, and stays.
	write(choicePath(), "new choice")

	moveOldSettings()
	if got := read(curvesPath()); got != "old curves" {
		t.Errorf("curves %q", got)
	}
	if got := read(choicePath()); got != "new choice" {
		t.Errorf("choice %q", got)
	}
	if got := read(filepath.Join(old, "fan-choice.json")); got != "old choice" {
		t.Errorf("old file %q", got)
	}
	if !strings.HasSuffix(filepath.Dir(curvesPath()), appName) {
		t.Errorf("path %s", curvesPath())
	}
}

func TestDiagnosisReadsEachDeviceOnce(t *testing.T) {
	seen := map[uint32]int{}
	lines := asusDiagnosisLines(func(device uint32) (uint32, bool) {
		seen[device]++
		return 0, true
	})
	if len(lines) != len(asusDiagDevices) || len(seen) != len(asusDiagDevices) {
		t.Fatalf("lines %d, devices %v", len(lines), seen)
	}
	for _, d := range asusDiagDevices {
		if seen[d.id] != 1 {
			t.Errorf("0x%08X read %d times", d.id, seen[d.id])
		}
	}
}

func TestDiagnosisText(t *testing.T) {
	d := diagnosis{
		App: "Sharktic 0.1.0", Windows: "Windows 10 Pro 22H2 (19045.1)",
		Manufacturer: "ASUSTeK COMPUTER INC.", Model: "ROG Strix G512LI_G512LI", Board: "G512LI",
		BIOS: "G512LI.311", BIOSDate: "04/28/2021", FanBackend: "asus", Modes: true, Curves: true,
		Lines: asusDiagnosisLines(func(device uint32) (uint32, bool) {
			switch device {
			case atkCPUFan:
				return 0x00010023, true
			case atkPerformanceMode:
				return 0x00010003, true
			case 0x00110019:
				return 0, false
			}
			return 0xFFFFFFFE, true
		}),
	}
	text := d.text()
	for _, want := range []string{
		"팬 설정은 바꾸지 않았습니다",
		"model:        ROG Strix G512LI_G512LI\n",
		"family:       -\n",
		"bios:         G512LI.311 04/28/2021\n",
		"fan backend:  asus\n",
		"0x00110013  00010023  CPU 팬 속도 · 있음 (3500 rpm)\n",
		"0x00120075  00010003  성능 모드 · 있음\n",
		"0x00110024  FFFFFFFE  CPU 팬 곡선 · 없음\n",
		"0x00110019  --------  성능 모드 (Vivobook 방식) · 응답 없음\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}

	// Without the driver there is nothing to read.
	if text := (diagnosis{Model: "Other"}).text(); strings.Contains(text, "0x0011") || !strings.Contains(text, "fan backend:  none\n") {
		t.Errorf("without the driver:\n%s", text)
	}
}

// TestDiagnosisOfThisPC reads this PC, and only reads.
func TestDiagnosisOfThisPC(t *testing.T) {
	if testing.Short() {
		t.Skip("reads this PC")
	}
	fans := detectFans()
	d := diagnose(fans, "test")
	text := d.text()
	t.Log("\n" + text)
	if d.Model == "" || d.BIOS == "" || !strings.Contains(d.Windows, "Windows") {
		t.Errorf("no PC in\n%s", text)
	}
	if fans != nil && len(d.Lines) == 0 {
		t.Error("the backend read nothing")
	}
}

// fakeFirmware records the sets that reach the firmware, and answers each with
// One, as a firmware that takes them.
type fakeFirmware struct{ sets []string }

func (f *fakeFirmware) control(modes, curves bool) *asusControl {
	return &asusControl{hasModes: modes, curveDevices: curves, fake: func(method, device uint32, params []byte) (uint32, bool) {
		if method == atkSet {
			s := fmt.Sprintf("%08X", device)
			if device == atkPerformanceMode {
				s += fmt.Sprintf("=%d", binary.LittleEndian.Uint32(params))
			}
			f.sets = append(f.sets, s)
		}
		return 1, true
	}}
}

func TestCurvesWithoutModesNeverSetAMode(t *testing.T) {
	f := &fakeFirmware{}
	a := f.control(false, true)
	curve := defaultCurve
	for range 2 {
		if err := a.setCurves(curve, curve); err != nil {
			t.Fatal(err)
		}
	}
	a.leaveCurves()
	want := []string{"00110024", "00110025", "00110024", "00110025"}
	if !slices.Equal(f.sets, want) {
		t.Errorf("sets %v, want %v", f.sets, want)
	}
	if !a.curvesOn {
		t.Error("curves not on")
	}
}

func TestCurvesWithModesStartFromBalanced(t *testing.T) {
	f := &fakeFirmware{}
	a := f.control(true, true)
	curve := defaultCurve
	a.setCurves(curve, curve)
	a.setCurves(curve, curve)
	a.leaveCurves()
	want := []string{"00120075=0", "00110024", "00110025", "00110024", "00110025", "00120075=0"}
	if !slices.Equal(f.sets, want) {
		t.Errorf("sets %v, want %v", f.sets, want)
	}
}

func TestPanelWithCurvesOnly(t *testing.T) {
	a := &app{modeChoice: -1, fans: &fakeFans{setCurvesFn: func(c, g fanCurve) error { return nil }, noLeaving: true}}
	tt := ui.NewTester(a.view, 300, panelHeight+modesHeight)
	for _, want := range []string{"조용함", "균형", "최대", "고급 설정 (온도별 팬 곡선)", "앱이 만든 곡선을 프리셋으로", "현재 곡선은 읽을 수 없어"} {
		if !tt.HasText(want) {
			t.Errorf("before: no %q in %q", want, tt.Texts())
		}
	}
	if tt.HasText("터보") || tt.HasText("현재 모드는 읽을 수 없어") {
		t.Errorf("before: texts %q", tt.Texts())
	}
	a.modeChoice = customChoice
	tt.Frame()
	if !tt.HasText("고급 설정의 곡선 사용 중. 이 기기는") {
		t.Errorf("curve on: texts %q", tt.Texts())
	}
	a.modeChoice = 0
	tt.Frame()
	if !tt.HasText("앱이 만든 곡선 사용 중. 이 기기는") || tt.HasText("현재 곡선은 읽을 수 없어") {
		t.Errorf("preset on: texts %q", tt.Texts())
	}
}

// Where the firmware has curves and no modes, the presets set curves of ours, the
// same for both fans, and never a mode.
func TestChoosingAPreset(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	var set [][2]fanCurve
	var fail error
	newApp := func() *app {
		return &app{modeChoice: -1, choiceFile: choicePath(), fans: &fakeFans{setCurvesFn: func(c, g fanCurve) error {
			set = append(set, [2]fanCurve{c, g})
			return fail
		}}}
	}
	a := newApp()
	tt := ui.NewTester(a.view, 300, panelHeight+modesHeight)
	if err := tt.Click("최대"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(set) != 1 || set[0][0] != curvePresets[2].curve || set[0][1] != curvePresets[2].curve || a.modeChoice != 2 {
		t.Fatalf("max: set %v, choice %d", set, a.modeChoice)
	}

	// It comes back by its label.
	set = nil
	if b := newApp(); !b.restoreChoice() || b.modeChoice != 2 || len(set) != 1 || set[0][0] != curvePresets[2].curve {
		t.Errorf("restored: choice %d, set %v", b.modeChoice, set)
	}

	// A refusal leaves no choice, as the curves on are unknown.
	fail = errors.New("refused")
	if err := tt.Click("조용함"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.modeChoice != noChoice || !tt.HasText("적용하지 못했습니다") {
		t.Errorf("refused: choice %d, texts %q", a.modeChoice, tt.Texts())
	}

	// A preset kept where there are modes sets nothing.
	set = nil
	modes := 0
	withModes := &app{modeChoice: -1, choiceFile: choicePath(), fans: &fakeFans{
		setModeFn:   func(uint32) error { modes++; return nil },
		setCurvesFn: func(c, g fanCurve) error { set = append(set, [2]fanCurve{c, g}); return nil },
	}}
	if err := os.WriteFile(choicePath(), []byte(`{"Kind":"preset","Preset":"최대"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if withModes.restoreChoice() || modes != 0 || set != nil {
		t.Errorf("preset with modes: modes %d, set %v", modes, set)
	}
}

func TestPresetsNeitherFailNorWarn(t *testing.T) {
	for _, p := range curvePresets {
		if s := p.curve.problem(); s != "" {
			t.Errorf("%s: %s", p.label, s)
		}
		if s := p.curve.warning(); s != "" {
			t.Errorf("%s: %s", p.label, s)
		}
	}
}

func TestPanelWithModesOnly(t *testing.T) {
	a := &app{modeChoice: -1, fans: &fakeFans{setModeFn: func(uint32) error { return nil }}}
	tt := ui.NewTester(a.view, 300, panelHeight+modesHeight)
	if !tt.HasText("균형") || tt.HasText("고급 설정") || tt.HasText("최대") || tt.HasText("프리셋") {
		t.Errorf("texts %q", tt.Texts())
	}
}

func TestRestoringWhatTheFirmwareLacks(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	if err := saveCurves(curvesPath(), [2]fanCurve{defaultCurve, defaultCurve}); err != nil {
		t.Fatal(err)
	}
	var modes, curves int
	onlyModes := &fakeFans{setModeFn: func(uint32) error { modes++; return nil }}
	onlyCurves := &fakeFans{setCurvesFn: func(c, g fanCurve) error { curves++; return nil }}

	(&app{choiceFile: choicePath(), fans: onlyModes}).chooseMode(1)
	if a := (&app{modeChoice: -1, choiceFile: choicePath(), fans: onlyCurves}); a.restoreChoice() || curves != 0 {
		t.Errorf("restored a mode without modes: choice %d", a.modeChoice)
	}
	(&app{choiceFile: choicePath(), fans: onlyCurves}).chooseMode(customChoice)
	modes, curves = 0, 0
	if a := (&app{modeChoice: -1, choiceFile: choicePath(), fans: onlyModes}); a.restoreChoice() || modes != 0 {
		t.Errorf("restored a curve without curves: choice %d", a.modeChoice)
	}
}

// fakeFans is a backend for the tests of the app, with the modes of ASUS if it has
// setModeFn, and curves if it has setCurvesFn.
type fakeFans struct {
	setModeFn   func(value uint32) error
	setCurvesFn func(cpu, gpu fanCurve) error
	noLeaving   bool
}

func modesAndCurves() *fakeFans {
	return &fakeFans{
		setModeFn:   func(uint32) error { return nil },
		setCurvesFn: func(c, g fanCurve) error { return nil },
	}
}

func (f *fakeFans) name() string                      { return "fake" }
func (f *fakeFans) read(*Snapshot)                    {}
func (f *fakeFans) hasCurves() bool                   { return f.setCurvesFn != nil }
func (f *fakeFans) forgetCurves()                     {}
func (f *fakeFans) leaveCurves()                      {}
func (f *fakeFans) canLeaveCurves() bool              { return !f.noLeaving }
func (f *fakeFans) diagnosisLines() []string          { return []string{"fake line"} }
func (f *fakeFans) setMode(v uint32) error            { return f.setModeFn(v) }
func (f *fakeFans) setCurves(cpu, gpu fanCurve) error { return f.setCurvesFn(cpu, gpu) }
func (f *fakeFans) modes() []fanMode {
	if f.setModeFn == nil {
		return nil
	}
	return asusModes
}

// The firmware must never get a mode that is not listed, whoever asks: 3 is not
// handled on the G512LI's firmware.
func TestASUSRefusesUnlistedModes(t *testing.T) {
	f := &fakeFirmware{}
	a := f.control(true, true)
	for _, mode := range []uint32{3, 4, 0xFFFFFFFF} {
		if err := a.setMode(mode); err == nil {
			t.Errorf("mode %d: no error", mode)
		}
	}
	if len(f.sets) != 0 {
		t.Errorf("reached the firmware: %v", f.sets)
	}
	if err := a.setMode(2); err != nil || !slices.Equal(f.sets, []string{"00120075=2"}) {
		t.Errorf("listed mode: %v, sets %v", err, f.sets)
	}
}

// A PC without a backend shows no fan section and sets nothing.
func TestPanelWithoutABackend(t *testing.T) {
	a := &app{modeChoice: -1}
	tt := ui.NewTester(a.view, 300, panelHeight)
	if tt.HasText("균형") || tt.HasText("고급 설정") {
		t.Errorf("texts %q", tt.Texts())
	}
	if a.hasFanControl() || a.modes() != nil || a.hasCurves() {
		t.Error("has fan control")
	}
	if text := diagnose(nil, "test").text(); !strings.Contains(text, "fan backend:  none\n") {
		t.Errorf("diagnosis:\n%s", text)
	}
}

// The backend tells the report what it has.
func TestDiagnosisAsksTheBackend(t *testing.T) {
	d := diagnose(&fakeFans{setCurvesFn: func(c, g fanCurve) error { return nil }}, "test")
	if d.FanBackend != "fake" || d.Modes || !d.Curves || !strings.Contains(d.text(), "fake line\n") {
		t.Errorf("%+v", d)
	}
}
