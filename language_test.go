package main

import (
	"os"
	"testing"

	"github.com/egoist/mygo/ui"
)

// inKorean shows the app in Korean for the rest of a test.
func inKorean(t *testing.T) {
	setAppLanguage(korean)
	t.Cleanup(func() { setAppLanguage(english) })
}

func TestLanguageIsKept(t *testing.T) {
	path := t.TempDir() + `\Sharktic\language`
	if l := loadLanguage(path); l != english {
		t.Errorf("none chosen: %v", l)
	}
	saveLanguage(path, korean)
	if l := loadLanguage(path); l != korean {
		t.Errorf("korean kept: %v", l)
	}
	saveLanguage(path, english)
	if l := loadLanguage(path); l != english {
		t.Errorf("english kept: %v", l)
	}
	if err := os.WriteFile(path, []byte("fr\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if l := loadLanguage(path); l != english {
		t.Errorf("unknown: %v", l)
	}
}

func TestViewInKorean(t *testing.T) {
	inKorean(t)
	a := &app{modeChoice: -1, fans: modesAndCurves(), snap: Snapshot{CPUUsage: 12, HasCPU: true}}
	tt := ui.NewTester(a.view, 300, panelHeight+modesHeight)
	for _, want := range []string{"PC 온도", "사용 12% · 여유 88%", "팬", "균형", "터보", "고급 설정 (온도별 팬 곡선)", "종료"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if tt.HasText("Balanced") || tt.HasText("Quit") {
		t.Errorf("english in %q", tt.Texts())
	}
}

func TestPresetOfOldVersionComesBack(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	var set []fanCurve
	a := &app{modeChoice: -1, choiceFile: choicePath(), fans: &fakeFans{setCurvesFn: func(c, g fanCurve) error {
		set = append(set, c)
		return nil
	}}}
	// Earlier versions kept a preset by its Korean label.
	if err := os.MkdirAll(configPath(""), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(choicePath(), []byte(`{"Kind":"preset","Preset":"조용함"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !a.restoreChoice() || a.modeChoice != 0 || len(set) != 1 || set[0] != curvePresets[0].curve {
		t.Errorf("choice %d, set %v", a.modeChoice, set)
	}
}

func TestServiceTextInKorean(t *testing.T) {
	if got := fanLabel("Fan 2"); got != "Fan 2" {
		t.Errorf("english fan: %q", got)
	}
	if got := superIONote("no chip"); got != "no chip" {
		t.Errorf("english note: %q", got)
	}
	inKorean(t)
	for in, want := range map[string]string{"Fan 2": "팬 2", "CPU": "CPU", "SYS_FAN1": "SYS_FAN1"} {
		if got := fanLabel(in); got != want {
			t.Errorf("fanLabel(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"no chip":                      "칩 없음",
		"unsupported chip [0x1234@2E]": "지원하지 않는 칩 [0x1234@2E]",
		"error: access denied":         "오류: access denied",
		"Nuvoton NCT6798D":             "Nuvoton NCT6798D",
	} {
		if got := superIONote(in); got != want {
			t.Errorf("superIONote(%q) = %q, want %q", in, got, want)
		}
	}
}
