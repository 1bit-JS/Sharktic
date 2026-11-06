package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"

	"github.com/egoist/mygo/ui"
)

const (
	curveMinTemp = 30
	curveMaxTemp = 100
	cpuFanTab    = 0
	gpuFanTab    = 1
)

// curvePoint is a fan level, in percent, from a temperature in degrees on.
type curvePoint struct {
	Temp, Level float64
}

// fanCurve is what the firmware takes: 8 points, the coolest first.
type fanCurve [8]curvePoint

// defaultCurve is where a curve starts, and is safe.
var defaultCurve = fanCurve{{40, 25}, {50, 30}, {60, 40}, {65, 45}, {70, 55}, {75, 70}, {80, 85}, {85, 100}}

// safetyFloors are the fan levels that a curve should reach by these temperatures.
// The chips protect themselves by slowing down, so a curve below them only warns.
var safetyFloors = []curvePoint{{70, 55}, {75, 70}, {80, 85}, {85, 100}}

// bytes returns the curve as ASUS firmware takes it: the temperatures, then the
// levels.
func (c fanCurve) bytes() []byte {
	out := make([]byte, 2*len(c))
	for i, p := range c {
		out[i] = byte(math.Round(p.Temp))
		out[len(c)+i] = byte(math.Round(p.Level))
	}
	return out
}

// levelAt returns the level of the last point at or below a temperature, or 0
// below the first. As the firmware may go in steps between points, this is the
// least it can be.
func (c fanCurve) levelAt(temp float64) float64 {
	level := 0.0
	for _, p := range c {
		if p.Temp <= temp {
			level = p.Level
		}
	}
	return level
}

// problem tells why the firmware should not get this curve, or "" if it can.
func (c fanCurve) problem() string {
	for i, p := range c {
		if math.Round(p.Temp) < curveMinTemp || math.Round(p.Temp) > curveMaxTemp {
			return fmt.Sprintf("%d구간의 온도는 %d~%d°C여야 합니다", i+1, curveMinTemp, curveMaxTemp)
		}
		if math.Round(p.Level) < minFanLevel || math.Round(p.Level) > 100 {
			return fmt.Sprintf("%d구간의 팬 속도는 %d~100%%여야 합니다", i+1, minFanLevel)
		}
	}
	for i := 1; i < len(c); i++ {
		if math.Round(c[i].Temp) <= math.Round(c[i-1].Temp) {
			return fmt.Sprintf("%d구간의 온도는 %d구간보다 높아야 합니다", i+1, i)
		}
		if math.Round(c[i].Level) < math.Round(c[i-1].Level) {
			return fmt.Sprintf("%d구간의 팬 속도는 %d구간보다 낮을 수 없습니다", i+1, i)
		}
	}
	return ""
}

// warning tells what a curve may cost, or "" if it reaches the safety floors.
func (c fanCurve) warning() string {
	for _, f := range safetyFloors {
		if c.levelAt(f.Temp) < f.Level {
			return fmt.Sprintf("주의: %.0f°C에서 팬 속도가 권장 %.0f%%보다 낮아 성능이 떨어지고 더 뜨거워질 수 있습니다", f.Temp, f.Level)
		}
	}
	return ""
}

// ofEitherFan returns what a check of a curve says of the first fan it finds
// something in.
func ofEitherFan(curves [2]fanCurve, check func(fanCurve) string) string {
	if p := check(curves[cpuFanTab]); p != "" {
		return "CPU 팬: " + p
	}
	if p := check(curves[gpuFanTab]); p != "" {
		return "GPU 팬: " + p
	}
	return ""
}

// curvesProblem tells what is wrong with either curve.
func curvesProblem(curves [2]fanCurve) string {
	return ofEitherFan(curves, fanCurve.problem)
}

// configPath is a file of the settings, or "" if there is no place.
func configPath(name string) string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, appName, name)
}

// moveOldSettings copies the settings of the app's old name, the first time it
// starts under the new one. It leaves the old files, for the old version.
func moveOldSettings() {
	dir, err := os.UserConfigDir()
	if err != nil {
		return
	}
	for _, name := range []string{"fan-curves.json", "fan-choice.json"} {
		to := filepath.Join(dir, appName, name)
		if _, err := os.Stat(to); err == nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, oldName, name))
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err == nil {
			err = os.WriteFile(to, data, 0o644)
		}
		if err != nil {
			log.Println("old settings:", err)
		}
	}
}

// curvesPath is the file that keeps the curves.
func curvesPath() string {
	return configPath("fan-curves.json")
}

// choicePath is the file that keeps the fan setting last chosen.
func choicePath() string {
	return configPath("fan-choice.json")
}

// loadCurves reads curves from a file, and gives false if they are missing or
// the firmware should not get them, as a file can be edited by hand.
func loadCurves(path string) ([2]fanCurve, bool) {
	var curves [2]fanCurve
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &curves) != nil || curvesProblem(curves) != "" {
		return curves, false
	}
	return curves, true
}

func saveCurves(path string, curves [2]fanCurve) error {
	data, err := json.MarshalIndent(curves, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// savedChoice is a fan setting to bring back when the app starts, as the firmware
// may not keep it. It holds a mode by its firmware value, not by its place in the
// list.
type savedChoice struct {
	Kind string // choiceMode or choiceCurve
	Mode uint32 `json:",omitempty"`
}

const (
	choiceMode  = "mode"
	choiceCurve = "curve"
)

// saveChoice keeps a choice that was set, where the app has a file for it.
func (a *app) saveChoice(i int) {
	if a.choiceFile == "" {
		return
	}
	var saved savedChoice
	switch i {
	case customChoice:
		saved = savedChoice{Kind: choiceCurve}
	default:
		saved = savedChoice{Kind: choiceMode, Mode: a.modes()[i].value}
	}
	data, err := json.Marshal(saved)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(a.choiceFile), 0o755)
	}
	if err == nil {
		err = os.WriteFile(a.choiceFile, data, 0o644)
	}
	if err != nil {
		log.Println("fan choice:", err)
	}
}

// restoreChoice sets the choice that was kept, and tells whether it did. A file
// can be edited by hand, so that only the modes of the list are ever set. The
// manual level of earlier versions is no longer a choice, so it sets nothing.
func (a *app) restoreChoice() bool {
	data, err := os.ReadFile(a.choiceFile)
	var saved savedChoice
	if err != nil || json.Unmarshal(data, &saved) != nil {
		return false
	}
	choice := noChoice
	switch saved.Kind {
	case choiceCurve:
		if _, ok := loadCurves(curvesPath()); ok && a.hasCurves() {
			choice = customChoice
		}
	case choiceMode:
		for i, m := range a.modes() {
			if m.value == saved.Mode {
				choice = i
			}
		}
	}
	if choice == noChoice {
		return false
	}
	a.chooseMode(choice)
	return !a.modeFailed
}

// applyCurves sets the curves being edited, and keeps them if the firmware took
// them.
func (a *app) applyCurves() {
	previous := a.appliedCurves
	a.appliedCurves = a.curves
	a.chooseMode(customChoice)
	switch {
	case a.modeFailed:
		a.appliedCurves = previous
		a.curveStatus = "적용하지 못했습니다. 이 기기가 사용자 곡선을 지원하지 않을 수 있습니다"
	case saveCurves(curvesPath(), a.curves) != nil:
		a.curveStatus = "적용했지만 저장하지 못했습니다"
	default:
		a.curveStatus = "적용하고 저장했습니다"
	}
}

// curveView is the window of the fan curves.
func (a *app) curveView(c *ui.Context) {
	t, s := c.Theme(), a.snap
	note := func(text string) { ui.Text(c, text).FontSize(12).TextColor(t.TextMuted) }
	problem, warning := curvesProblem(a.curves), ofEitherFan(a.curves, fanCurve.warning)

	ui.Column(c).Fill().Background(t.Surface).Padding(16).Gap(12).Children(func() {
		note(fmt.Sprintf("지금: CPU 팬 %s · GPU 팬 %s · PC %s · GPU %s",
			rpm(s.CPUFan, s.HasCPUFan), rpm(s.GPUFan, s.HasGPUFan),
			degrees(s.Temp, s.HasTemp), degrees(s.GPUTemp, s.HasGPUTemp)))
		ui.Tabs(c.Key("fan"), &a.curveTab, "CPU 팬", "GPU 팬")

		curve := &a.curves[a.curveTab]
		ui.Row(c).Grow(1).Gap(16).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Width(360).Gap(4).Children(func() {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, "구간").FontSize(12).TextColor(t.TextMuted).Width(28)
					ui.Text(c, "온도 (°C)").FontSize(12).TextColor(t.TextMuted).Width(160)
					ui.Text(c, "팬 속도 (%)").FontSize(12).TextColor(t.TextMuted).Width(160)
				})
				for i := range curve {
					p := &curve[i]
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, fmt.Sprint(i+1)).FontSize(12).Width(28)
						ui.NumberInput(c.Key(fmt.Sprint("temp ", a.curveTab, i)), &p.Temp, curveMinTemp, curveMaxTemp, 1).
							Width(160).Label(fmt.Sprintf("%d구간 온도", i+1))
						ui.NumberInput(c.Key(fmt.Sprint("level ", a.curveTab, i)), &p.Level, minFanLevel, 100, 1).
							Width(160).Label(fmt.Sprintf("%d구간 팬 속도", i+1))
					})
				}
			})
			ui.Column(c).Grow(1).Gap(4).Children(func() {
				ui.Column(c).Grow(1).Draw(func(p *ui.Painter, r ui.Rect) { drawCurve(p, r, t, *curve) })
				note("실선: 이 곡선 · 점선: 권장 하한")
			})
		})

		switch {
		case problem != "":
			ui.Text(c, problem).FontSize(12).Bold()
		case warning != "":
			ui.Text(c, warning).FontSize(12).Bold()
		default:
			note("곡선의 온도는 펌웨어가 읽는 센서 기준이라, 위 PC 온도와 다를 수 있습니다")
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Button(c.Key("default"), "기본값").OnClick(func() { *curve = defaultCurve; a.curveStatus = "" })
			other, label := gpuFanTab, "GPU 팬에 복사"
			if a.curveTab == gpuFanTab {
				other, label = cpuFanTab, "CPU 팬에 복사"
			}
			ui.Button(c.Key("copy"), label).OnClick(func() { a.curves[other] = *curve; a.curveStatus = "" })
			ui.Button(c.Key("undo"), "되돌리기").Disabled(a.curves == a.appliedCurves).
				OnClick(func() { a.curves = a.appliedCurves; a.curveStatus = "" })
			ui.Text(c, a.curveStatus).FontSize(12).TextColor(t.TextMuted).Grow(1)
			ui.Button(c.Key("apply"), "적용").Disabled(problem != "").OnClick(a.applyCurves)
		})
	})
}

// drawCurve draws a curve over the safety floors, with the temperature along and
// the fan level up.
func drawCurve(p *ui.Painter, r ui.Rect, t *ui.Theme, curve fanCurve) {
	const left, right, top, bottom = 32, 8, 8, 22
	w, h := r.W-left-right, r.H-top-bottom
	x := func(temp float64) float32 {
		return r.X + left + w*float32((temp-curveMinTemp)/(curveMaxTemp-curveMinTemp))
	}
	y := func(level float64) float32 { return r.Y + top + h*float32(1-level/100) }

	for level := 0.0; level <= 100; level += 25 {
		p.Line(x(curveMinTemp), y(level), x(curveMaxTemp), y(level), 1, t.Border)
		p.Text(r.X, y(level)-7, fmt.Sprintf("%.0f%%", level), 11, t.TextMuted)
	}
	for temp := float64(curveMinTemp); temp <= curveMaxTemp; temp += 10 {
		p.Line(x(temp), y(0), x(temp), y(100), 1, t.Border)
		p.Text(x(temp)-8, y(0)+6, fmt.Sprintf("%.0f°", temp), 11, t.TextMuted)
	}

	// The floors go up in steps at their temperatures, from the lowest level.
	floor := float64(minFanLevel)
	floors := (&ui.Path{}).MoveTo(x(curveMinTemp), y(floor))
	for _, f := range safetyFloors {
		floors.LineTo(x(f.Temp), y(floor)).LineTo(x(f.Temp), y(f.Level))
		floor = f.Level
	}
	floors.LineTo(x(curveMaxTemp), y(floor))
	p.StrokePath(floors.Dash(0, 4, 4), 1.5, t.TextMuted)

	line := make([][2]float32, len(curve))
	for i, point := range curve {
		line[i] = [2]float32{x(point.Temp), y(point.Level)}
	}
	p.StrokePolyline(line, 2, t.Accent)
	const dot = 10
	for _, point := range curve {
		mark := ui.Rect{X: x(point.Temp) - dot/2, Y: y(point.Level) - dot/2, W: dot, H: dot}
		p.Fill(mark, t.Accent, dot/2)
		p.Stroke(mark, t.Surface, dot/2, 2)
	}
}
