package main

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// The advanced fan control window explains what turning it on does, turns it
// on and off, and shows the progress. It is opt-in: nothing of it is
// installed with the app.

// serviceRetryNow asks the sampler to connect to the service at once, as
// after it has been turned on.
var serviceRetryNow atomic.Bool

type advancedStep int

const (
	advancedIdle advancedStep = iota
	advancedWorking
	advancedDone
	advancedFailed
)

// advancedWork is what the elevated copy is asked to do.
type advancedWork int

const (
	turnOn advancedWork = iota
	turnOff
	// updateService installs this version's service in place of an older
	// one, which turning on does too.
	updateService
)

// advancedState is the advanced window's, on the main thread.
type advancedState struct {
	window       *mygo.Window
	step         advancedStep
	work         advancedWork // what the work in progress does
	message      string       // of the last result
	installed    bool         // the service is registered
	removePawnIO bool
}

// advancedPromos are shown, one at a time, while the setup runs.
var advancedPromos = []msg{
	{"Sharktic is a light app that shows temperatures, usage and fans at a glance in the tray.",
		"Sharktic은 트레이 아이콘으로 온도, 사용량, 팬을 한눈에 보여주는 가벼운 앱이에요."},
	{"Click an icon to see the programs that use the most CPU and memory.",
		"아이콘을 누르면 CPU와 메모리를 가장 많이 쓰는 프로그램을 바로 볼 수 있어요."},
	{"On supported laptops, you can draw your own fan curves by temperature in Advanced.",
		"지원하는 노트북에서는 고급 설정에서 온도별 팬 곡선을 직접 그릴 수 있어요."},
	{"If something is wrong, tell us with 'Copy diagnostic info' in the tray menu. It helps a lot with support for more devices.",
		"문제가 있으면 트레이 메뉴의 '진단 정보 복사'로 알려 주세요. 다른 기기 지원에 큰 도움이 돼요."},
}

const promoEvery = 4 * time.Second

func (a *app) openAdvancedWindow() {
	if a.panel != nil {
		a.panel.Close()
	}
	if a.advanced.window != nil {
		a.advanced.window.Focus()
		return
	}
	if a.advanced.step != advancedWorking {
		a.advanced.step = advancedIdle
		a.advanced.installed, _ = serviceStatus()
	}
	a.advanced.window = mygo.NewWindow(mygo.WindowOptions{
		Title:     tr("Advanced fan control", "고급 팬 제어"),
		Width:     560,
		Height:    620,
		MinWidth:  480,
		MinHeight: 520,
		Content:   ui.View(a.advancedView),
	})
	a.advanced.window.OnClosed(func() { a.advanced.window = nil })
}

// runAdvancedSetup turns advanced fan control on or off, or updates its
// service, in an elevated copy of the app, and shows the result when it ends.
func (a *app) runAdvancedSetup(work advancedWork) {
	args := enableFlag
	if work == turnOff {
		args = disableFlag
		if a.advanced.removePawnIO {
			args += " " + removePawnIOFlag
		}
	}
	a.advanced.step, a.advanced.work, a.advanced.message = advancedWorking, work, ""
	go func() {
		code, err := runElevated(args)
		mygo.RunOnMain(func() {
			st := &a.advanced
			st.installed, _ = serviceStatus()
			switch {
			case errors.Is(err, errCancelled):
				st.step, st.message = advancedIdle, tr("The administrator prompt was cancelled; nothing was changed.", "관리자 확인이 취소되어 아무것도 바꾸지 않았어요.")
			case err != nil:
				st.step, st.message = advancedFailed, tr("Could not run as administrator: ", "관리자 권한으로 실행하지 못했습니다: ")+err.Error()
			case code != setupOK:
				st.step, st.message = advancedFailed, setupMessage(code)
			default:
				st.step = advancedDone
				serviceRetryNow.Store(true)
			}
			if st.window != nil {
				st.window.Update(func() {})
			}
		})
	}()
}

func (a *app) advancedView(c *ui.Context) {
	t := c.Theme()
	st := &a.advanced
	muted := func(text string) ui.Element { return ui.Text(c, text).FontSize(12).TextColor(t.TextMuted) }
	heading := func(text string) { ui.Text(c, text).Bold() }
	bullets := func(lines ...string) {
		ui.Column(c).Gap(4).Children(func() {
			for _, l := range lines {
				// The dot stays by the first line of a long one.
				ui.Row(c).Gap(6).AlignItems(ui.Start).Children(func() {
					ui.Text(c, "•").TextColor(t.TextMuted)
					ui.Text(c, l).Grow(1)
				})
			}
		})
	}
	// The scroll bar runs in the right padding, clear of the content.
	ui.Column(c).Fill().Background(t.Surface).Padding(20, scrollBarEdge, 20, 20).Gap(16).Children(func() {
		switch st.step {
		case advancedWorking:
			ui.Column(c).Grow(1).Gap(16).Padding(0, scrollGutter, 0, 0).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
				ui.Spinner(c)
				switch st.work {
				case turnOn:
					ui.Text(c, tr("Installing what advanced fan control needs", "고급 팬 제어에 필요한 구성 요소를 설치하고 있어요")).Bold()
				case updateService:
					ui.Text(c, tr("Updating the Sharktic service", "Sharktic 서비스를 새 버전으로 바꾸고 있어요")).Bold()
				default:
					ui.Text(c, tr("Turning off advanced fan control", "고급 팬 제어를 끄고 있어요")).Bold()
				}
				muted(tr("When Windows asks for administrator approval, click Yes. It usually takes a few seconds.", "관리자 확인 창이 뜨면 '예'를 눌러 주세요. 보통 몇 초면 끝나요."))
				ui.Column(c).Padding(12).Background(t.SurfacePressed).Children(func() {
					i := int(time.Now().UnixNano()/int64(promoEvery)) % len(advancedPromos)
					ui.Text(c, advancedPromos[i].String()).FontSize(12)
				})
			})
		case advancedDone:
			ui.Column(c).Grow(1).Gap(12).Padding(0, scrollGutter, 0, 0).Justify(ui.Center).Children(func() {
				switch st.work {
				case turnOn:
					ui.Text(c, tr("Advanced fan control is on", "고급 팬 제어를 켰어요")).Bold().FontSize(16)
					muted(tr("The CPU temperature appears in the panel shortly. It keeps working when you restart the app or the PC.", "잠시 뒤 패널에 CPU 온도가 표시됩니다. 앱을 다시 켜도, 컴퓨터를 다시 시작해도 계속 동작해요."))
				case updateService:
					ui.Text(c, tr("The Sharktic service is updated", "Sharktic 서비스를 업데이트했어요")).Bold().FontSize(16)
					muted(fmt.Sprintf(tr("The service now runs version %s, the same as the app. Your settings are kept.", "서비스가 앱과 같은 %s 버전으로 바뀌었어요. 설정은 그대로예요."), mygo.App.Version()))
				default:
					ui.Text(c, tr("Advanced fan control is off", "고급 팬 제어를 껐어요")).Bold().FontSize(16)
					muted(tr("The Sharktic service was removed. Monitoring and the other features keep working.", "Sharktic 서비스를 제거했어요. 모니터링과 기존 기능은 그대로 쓸 수 있어요."))
				}
			})
			ui.Row(c).Padding(0, scrollGutter, 0, 0).Children(func() {
				ui.Spacer(c)
				ui.Button(c.Key("close"), tr("Close", "닫기")).OnClick(func() { st.window.Close() })
			})
		default:
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Gap(16).Padding(0, scrollGutter, 0, 0).Children(func() {
					if st.installed {
						a.advancedOnView(c, heading, muted, bullets)
					} else {
						a.advancedOffView(c, heading, muted, bullets)
					}
				})
			})
			if st.message != "" {
				ui.Text(c, st.message).FontSize(12).TextColor(t.Danger).Padding(0, scrollGutter, 0, 0)
			}
			ui.Row(c).Gap(8).Padding(0, scrollGutter, 0, 0).Children(func() {
				ui.Spacer(c)
				ui.Button(c.Key("close"), tr("Close", "닫기")).OnClick(func() { st.window.Close() })
				switch {
				case st.installed && a.serviceOutdated():
					ui.Button(c.Key("off"), tr("Turn off", "끄기")).OnClick(func() { a.runAdvancedSetup(turnOff) })
					ui.PrimaryButton(c.Key("update"), tr("Update service", "서비스 업데이트")).OnClick(func() { a.runAdvancedSetup(updateService) })
				case st.installed:
					ui.Button(c.Key("off"), tr("Turn off", "끄기")).OnClick(func() { a.runAdvancedSetup(turnOff) })
				default:
					ui.PrimaryButton(c.Key("on"), tr("Turn on", "켜기")).OnClick(func() { a.runAdvancedSetup(turnOn) })
				}
			})
		}
	})
}

func (a *app) advancedOffView(c *ui.Context, heading func(string), muted func(string) ui.Element, bullets func(...string)) {
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, tr("Advanced fan control", "고급 팬 제어")).Bold().FontSize(18)
		if a.fans == nil {
			ui.Text(c, tr("This PC has no way to control its fans through the maker's firmware. Desktop boards and laptops such as ThinkPads need the hardware read and controlled directly, which takes an extra component.", "이 PC는 제조사 펌웨어로 팬을 제어하는 방법이 없어요. 데스크톱 메인보드나 ThinkPad 같은 노트북은 하드웨어를 직접 읽고 제어해야 해서, 별도의 구성 요소가 필요해요."))
		} else {
			ui.Text(c, tr("Installs a component that works with the hardware directly, to read the exact temperature the CPU measures, and to control the fans of more devices in the future.", "CPU가 직접 재는 정확한 온도를 읽고, 앞으로 더 많은 기기의 팬을 제어할 수 있도록 하드웨어를 직접 다루는 구성 요소를 설치해요."))
		}
	})
	ui.Column(c).Gap(6).Children(func() {
		heading(tr("Available right away", "켜면 바로 되는 것"))
		bullets(
			tr("CPU temperature, as the CPU measures it. It shows even on desktops where the PC temperature reads '--'.", "CPU 온도: CPU가 직접 재는 값이에요. PC 온도가 '--'로 나오는 데스크톱에서도 보여요."),
			tr("Fan speeds of desktop boards with Nuvoton or ITE chips. ITE chips are still being tested.", "데스크톱 메인보드의 팬 속도: Nuvoton, ITE 칩을 쓰는 보드에서 읽어요. ITE 칩은 아직 시험 중이에요."),
		)
		heading(tr("Coming later", "차례로 추가될 것"))
		bullets(tr("Temperature-based control of desktop board fans", "데스크톱 메인보드 팬의 온도별 제어"), tr("Fan control of ThinkPads and other laptops controlled through the EC", "ThinkPad 등 EC 방식 노트북의 팬 제어"))
	})
	ui.Column(c).Gap(6).Children(func() {
		heading(tr("What gets installed", "설치되는 것"))
		bullets(
			tr("Sharktic service: only does the reading that needs administrator rights. The app itself keeps running with normal rights.", "Sharktic 서비스: 관리자 권한이 필요한 읽기만 맡아요. 앱 화면은 지금처럼 일반 권한으로 동작해요."),
			tr("PawnIO driver: a signed, open-source hardware access driver. LibreHardwareMonitor and FanControl use it too.", "PawnIO 드라이버: 서명된 오픈 소스 하드웨어 접근 드라이버예요. LibreHardwareMonitor와 FanControl도 같은 드라이버를 써요."),
		)
	})
	ui.Column(c).Gap(6).Children(func() {
		heading(tr("Good to know", "알아 둘 점"))
		bullets(
			tr("Windows asks for administrator approval (UAC) once when you turn it on.", "켤 때 Windows 관리자 확인(UAC)이 한 번 떠요."),
			tr("Some online games' anti-cheat (such as FACEIT) may object to this driver. If so, turn it off here.", "일부 온라인 게임의 안티치트(예: FACEIT)가 이 드라이버를 문제 삼을 수 있어요. 그럴 땐 여기서 끄면 돼요."),
			tr("You can turn it off from this window at any time.", "언제든 이 창에서 끌 수 있어요."),
		)
	})
}

func (a *app) advancedOnView(c *ui.Context, heading func(string), muted func(string) ui.Element, bullets func(...string)) {
	s := a.snap
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, tr("Advanced fan control is on", "고급 팬 제어가 켜져 있어요")).Bold().FontSize(18)
		switch {
		case !s.ServiceRunning:
			muted(tr("Connecting to the service. If it keeps failing, turn it off and on again.", "서비스에 연결하는 중이에요. 계속 연결되지 않으면 껐다가 다시 켜 주세요."))
		case a.serviceOutdated():
			muted(fmt.Sprintf(tr("The service is an older version (%s). Click Update service to bring it to %s, the same as the app.", "서비스가 이전 버전(%s)이에요. [서비스 업데이트]를 누르면 앱과 같은 %s 버전으로 바뀌어요."), s.ServiceVersion, mygo.App.Version()))
		case s.HasCPUTemp:
			muted(tr("CPU temperature ", "CPU 온도 ") + degrees(s.CPUTemp, true))
		default:
			muted(tr("The temperature of this CPU cannot be read yet.", "이 CPU의 온도는 아직 읽을 수 없어요."))
		}
		if s.ServiceRunning && s.SuperIO != "" {
			muted(tr("Board chip: ", "메인보드 칩: ") + superIONote(s.SuperIO))
		}
	})
	ui.Column(c).Gap(6).Children(func() {
		heading(tr("Turning it off", "끄면"))
		bullets(tr("Removes the Sharktic service and its files.", "Sharktic 서비스와 그 파일을 제거해요."), tr("The PawnIO driver may be used by other programs, so it is removed only if you check below.", "PawnIO 드라이버는 다른 프로그램도 쓸 수 있어서, 아래를 선택했을 때만 제거해요."))
		ui.Checkbox(c, &a.advanced.removePawnIO, tr("Also remove the PawnIO driver", "PawnIO 드라이버도 제거"))
	})
}

// superIONote is what the service tells of the board's chip, which it says
// in English, in the app's language.
func superIONote(note string) string {
	if appLanguage() != korean {
		return note
	}
	switch {
	case note == "no chip":
		return "칩 없음"
	case note == "not probed on laptops":
		return "노트북은 확인하지 않음"
	case strings.HasPrefix(note, "unsupported chip "):
		return "지원하지 않는 칩 " + strings.TrimPrefix(note, "unsupported chip ")
	case strings.HasPrefix(note, "error: "):
		return "오류: " + strings.TrimPrefix(note, "error: ")
	}
	return note
}

// serviceOutdated tells that the running service is of another version than
// the app, whose new requests it may not know.
func (a *app) serviceOutdated() bool {
	s := a.snap
	return s.ServiceRunning && s.ServiceVersion != mygo.App.Version()
}

// advancedSection is the panel's line of advanced fan control: the CPU
// temperature when it is on, and a way to it when it is off.
func (a *app) advancedSection(c *ui.Context) {
	t, s := c.Theme(), a.snap
	if s.ServiceRunning {
		ui.Column(c).Gap(4).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, tr("CPU temperature", "CPU 온도")).Bold().Grow(1)
				ui.Text(c, degrees(s.CPUTemp, s.HasCPUTemp)).Bold()
			})
			ui.Text(c, tr("CPU sensor · advanced fan control", "CPU 센서 · 고급 팬 제어")).FontSize(12).TextColor(t.TextMuted)
			if a.serviceOutdated() {
				ui.Text(c, tr("The advanced fan control service is out of date", "고급 팬 제어 서비스가 이전 버전이에요")).FontSize(12)
				ui.Button(c.Key("advanced-update"), tr("Update service", "서비스 업데이트")).OnClick(a.openAdvancedWindow)
			}
		})
		return
	}
	if a.advanced.installed {
		return // being connected to, or stopped: the window tells
	}
	ui.Column(c).Gap(6).Padding(10).Background(t.SurfacePressed).Children(func() {
		ui.Text(c, tr("Advanced fan control", "고급 팬 제어")).Bold()
		if a.fans == nil {
			ui.Text(c, tr("Reading this PC's fans and CPU temperature directly needs advanced fan control.", "이 PC의 팬과 CPU 온도를 직접 읽으려면 고급 팬 제어가 필요해요.")).FontSize(12)
		} else {
			ui.Text(c, tr("See the exact temperature the CPU measures.", "CPU가 직접 재는 정확한 온도를 볼 수 있어요.")).FontSize(12)
		}
		ui.Button(c.Key("advanced-more"), tr("Learn more", "알아보기")).OnClick(a.openAdvancedWindow)
	})
}
