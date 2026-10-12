package main

import (
	"fmt"
	"image/color"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font/gofont"
	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// The launcher window: a status line, a progress bar, a collapsible log,
// and the account picker when one is needed. Work runs on another goroutine
// and talks to the window through the functions at the bottom.

var (
	bg      = color.NRGBA{0x0e, 0x0b, 0x16, 0xff}
	panel   = color.NRGBA{0x07, 0x05, 0x0c, 0xff}
	fg      = color.NRGBA{0xee, 0xe9, 0xfa, 0xff}
	muted   = color.NRGBA{0xa3, 0x96, 0xc8, 0xff}
	accent  = color.NRGBA{0x8a, 0x62, 0xe6, 0xff}
	errored = color.NRGBA{0xe5, 0x5b, 0x5b, 0xff}
)

type picker struct {
	names    []string
	enum     widget.Enum
	remember widget.Bool
	play     widget.Clickable
	reply    chan [2]string
}

type uiState struct {
	mu       sync.Mutex
	win      *app.Window
	status   string
	progress float32 // 0..1, or <0 for "busy, no numbers"
	lines    []string
	logFile  *os.File
	showLog  bool
	failed   bool
	pick     *picker
	switchTo chan bool // non-nil while offering "Switch account"
	// wantHeight asks the event loop to resize the window; resizing from
	// inside a frame or from another goroutine can deadlock.
	wantHeight int

	toggle, closeBtn, switchBtn, copyBtn widget.Clickable
	copiedAt                             time.Time
	list                                 widget.List
	follow                               bool
}

var ui = &uiState{progress: -1, follow: true}

func runUI(work func() error) {
	go func() {
		err := work()
		if err != nil {
			ui.mu.Lock()
			ui.failed, ui.showLog = true, true
			ui.status = "Something went wrong: " + err.Error()
			ui.mu.Unlock()
			logf("ERROR: %v", err)
			logf("Send a screenshot of this window to whoever runs the pack.")
			return
		}
		time.Sleep(1500 * time.Millisecond)
		os.Exit(0)
	}()
	go func() {
		w := new(app.Window)
		w.Option(app.Title("JRAFT"), app.Size(unit.Dp(560), unit.Dp(190)), app.MinSize(unit.Dp(420), unit.Dp(190)))
		ui.mu.Lock()
		ui.win = w
		ui.mu.Unlock()
		loop(w)
		os.Exit(0)
	}()
	app.Main()
}

func loop(w *app.Window) {
	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	th.Palette = material.Palette{Bg: bg, Fg: fg, ContrastBg: accent, ContrastFg: color.NRGBA{0xff, 0xff, 0xff, 0xff}}
	ui.list.Axis = layout.Vertical
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			ui.mu.Lock()
			ui.frame(gtx, th)
			h := ui.wantHeight
			ui.wantHeight = 0
			ui.mu.Unlock()
			e.Frame(gtx.Ops)
			if h > 0 {
				w.Option(app.Size(unit.Dp(560), unit.Dp(float32(h))))
			}
		}
	}
}

func fill(gtx layout.Context, c color.NRGBA) {
	paint.FillShape(gtx.Ops, c, clip.Rect{Max: gtx.Constraints.Max}.Op())
}

func (u *uiState) frame(gtx layout.Context, th *material.Theme) {
	if u.toggle.Clicked(gtx) {
		u.showLog = !u.showLog
		h := 190
		if u.showLog {
			h = 460
		}
		u.wantHeight = h
	}
	if u.copyBtn.Clicked(gtx) {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(strings.Join(u.lines, "\n")))})
		u.copiedAt = time.Now()
		go func() { time.Sleep(2 * time.Second); u.invalidate() }()
	}
	if u.closeBtn.Clicked(gtx) {
		os.Exit(1)
	}
	if u.switchTo != nil && u.switchBtn.Clicked(gtx) {
		u.switchTo <- true
		u.switchTo = nil
	}
	if p := u.pick; p != nil && p.play.Clicked(gtx) && p.enum.Value != "" {
		rem := ""
		if p.remember.Value {
			rem = "1"
		}
		p.reply <- [2]string{p.enum.Value, rem}
		u.pick = nil
	}

	fill(gtx, bg)
	layout.UniformInset(unit.Dp(18)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		if u.pick != nil {
			return u.pickerView(gtx, th)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Body1(th, u.status)
				l.MaxLines = 2
				if u.failed {
					l.Color = errored
				}
				return l.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				p := u.progress
				if p < 0 {
					p = 0
				}
				bar := material.ProgressBar(th, p)
				bar.Height = unit.Dp(8)
				bar.TrackColor = panel
				if u.failed {
					bar.Color = errored
				}
				return bar.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						label := "Show log"
						if u.showLog {
							label = "Hide log"
						}
						b := material.Button(th, &u.toggle, label)
						b.Background = panel
						b.Color = muted
						b.TextSize = unit.Sp(13)
						return b.Layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						label := "Copy log"
						if time.Since(u.copiedAt) < 2*time.Second {
							label = "Copied!"
						}
						b := material.Button(th, &u.copyBtn, label)
						b.Background = panel
						b.Color = muted
						b.TextSize = unit.Sp(13)
						return b.Layout(gtx)
					}),
					layout.Flexed(1, layout.Spacer{}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						switch {
						case u.failed:
							b := material.Button(th, &u.closeBtn, "Close")
							b.Background = errored
							return b.Layout(gtx)
						case u.switchTo != nil:
							return material.Button(th, &u.switchBtn, "Switch account").Layout(gtx)
						}
						return layout.Dimensions{}
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				if !u.showLog {
					return layout.Dimensions{Size: gtx.Constraints.Min}
				}
				fill(gtx, panel)
				return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					if u.follow {
						u.list.Position.First = len(u.lines)
					}
					return material.List(th, &u.list).Layout(gtx, len(u.lines), func(gtx layout.Context, i int) layout.Dimensions {
						l := material.Body2(th, u.lines[i])
						l.Color = muted
						l.TextSize = unit.Sp(12)
						return l.Layout(gtx)
					})
				})
			}),
		)
	})
}

func (u *uiState) pickerView(gtx layout.Context, th *material.Theme) layout.Dimensions {
	p := u.pick
	children := []layout.FlexChild{
		layout.Rigid(material.Body1(th, "Which account do you want to play on?").Layout),
		layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
	}
	for _, n := range p.names {
		children = append(children, layout.Rigid(material.RadioButton(th, &p.enum, n, n).Layout))
	}
	children = append(children,
		layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					c := material.CheckBox(th, &p.remember, "Don't ask again")
					c.Color = muted
					return c.Layout(gtx)
				}),
				layout.Flexed(1, layout.Spacer{}.Layout),
				layout.Rigid(material.Button(th, &p.play, "Play").Layout),
			)
		}),
	)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func (u *uiState) invalidate() {
	if u.win != nil {
		u.win.Invalidate()
	}
}

// --- called from the worker goroutine --------------------------------------

// logf adds a log line and shows it above the progress bar.
func logf(format string, a ...any) {
	line := strings.TrimSpace(fmt.Sprintf(format, a...))
	if line == "" {
		return
	}
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.lines = append(ui.lines, line)
	if !ui.failed {
		ui.status = line
	}
	if ui.logFile != nil {
		fmt.Fprintln(ui.logFile, time.Now().Format("15:04:05"), line)
	}
	ui.invalidate()
}

// setProgress sets the bar to done/total; total <= 0 means "busy".
func setProgress(done, total int64) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	if total <= 0 {
		ui.progress = -1
	} else {
		ui.progress = float32(done) / float32(total)
	}
	ui.invalidate()
}

func openLogFile(path string) {
	f, err := os.Create(path)
	if err == nil {
		ui.mu.Lock()
		ui.logFile = f
		ui.mu.Unlock()
	}
}

// askAccount shows the account picker and waits for "Play".
func askAccount(names []string, preselect string) (string, bool) {
	p := &picker{names: names, reply: make(chan [2]string, 1)}
	p.enum.Value = preselect
	if p.enum.Value == "" {
		p.enum.Value = names[0]
	}
	h := 150 + 34*len(names)
	ui.mu.Lock()
	ui.pick = p
	ui.wantHeight = h
	ui.invalidate()
	ui.mu.Unlock()
	r := <-p.reply
	ui.mu.Lock()
	ui.wantHeight = 190
	ui.invalidate()
	ui.mu.Unlock()
	return r[0], r[1] != ""
}

// offerSwitch shows a "Switch account" button for a few seconds.
func offerSwitch(d time.Duration) bool {
	ch := make(chan bool, 1)
	ui.mu.Lock()
	ui.switchTo = ch
	ui.invalidate()
	ui.mu.Unlock()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		ui.mu.Lock()
		ui.switchTo = nil
		ui.invalidate()
		ui.mu.Unlock()
		return false
	}
}
