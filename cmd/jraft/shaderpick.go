package main

import (
	"bytes"
	_ "embed"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	xdraw "golang.org/x/image/draw"
)

// The first time the launcher sets up a PC whose preset has shaders on, it asks which of
// the pack's two shaders to use, with a screenshot of each from the same spot.

const (
	shaderComplementary = "complementary"
	shaderRethinking    = "rethinking-voxels"
)

//go:embed shaderpics/complementary-euphoria.jpg
var complementaryPic []byte

//go:embed shaderpics/rethinking-voxels.jpg
var rethinkingPic []byte

type shaderChoice struct {
	key, title, text string
	src              image.Image
	pick, imgClick   widget.Clickable
	// scaled is src resized for the card's current pixel size. Letting the GPU scale
	// the photo instead left specks in its dark areas.
	scaled  paint.ImageOp
	scaledW int
}

type shaderPicker struct {
	choices [2]*shaderChoice
	reply   chan string
}

func decodePic(b []byte) image.Image {
	src, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		return image.NewRGBA(image.Rect(0, 0, 16, 9))
	}
	return src
}

// sized returns the photo scaled to w x h pixels, redoing it only when w changes.
func (c *shaderChoice) sized(w, h int) paint.ImageOp {
	if c.scaledW != w {
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), c.src, c.src.Bounds(), xdraw.Src, nil)
		c.scaled, c.scaledW = paint.NewImageOp(dst), w
		c.scaled.Filter = paint.FilterNearest
	}
	return c.scaled
}

// askShader shows both shaders and waits for a pick.
func askShader() string {
	p := &shaderPicker{reply: make(chan string, 1)}
	p.choices[0] = &shaderChoice{
		key:   shaderComplementary,
		title: "Complementary Reimagined + Euphoria Patches",
		text:  "Darker caves, fog, reflective water, auroras and settling snow.",
		src:   decodePic(complementaryPic),
	}
	p.choices[1] = &shaderChoice{
		key:   shaderRethinking,
		title: "Rethinking Voxels",
		text:  "Light from lava, torches and glowstone spreads over nearby blocks in colour.",
		src:   decodePic(rethinkingPic),
	}
	ui.mu.Lock()
	ui.shader = p
	ui.wantWidth, ui.wantHeight = 780, 430
	ui.invalidate()
	ui.mu.Unlock()
	r := <-p.reply
	ui.mu.Lock()
	ui.wantWidth, ui.wantHeight = 560, 190
	ui.invalidate()
	ui.mu.Unlock()
	return r
}

func (u *uiState) shaderView(gtx layout.Context, th *material.Theme) layout.Dimensions {
	p := u.shader
	for _, c := range p.choices {
		if c.pick.Clicked(gtx) || c.imgClick.Clicked(gtx) {
			p.reply <- c.key
			u.shader = nil
			return layout.Dimensions{Size: gtx.Constraints.Min}
		}
	}
	card := func(c *shaderChoice) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return material.Clickable(gtx, &c.imgClick, func(gtx layout.Context) layout.Dimensions {
						w := gtx.Constraints.Max.X
						h := w * 9 / 16
						gtx.Constraints.Min = image.Pt(w, h)
						gtx.Constraints.Max = image.Pt(w, h)
						defer clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(8)).Push(gtx.Ops).Pop()
						c.sized(w, h).Add(gtx.Ops)
						paint.PaintOp{}.Add(gtx.Ops)
						return layout.Dimensions{Size: image.Pt(w, h)}
					})
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Body1(th, c.title)
					l.Font.Weight = 600
					return l.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Body2(th, c.text)
					l.Color = muted
					return l.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return material.Button(th, &c.pick, "Use this one").Layout(gtx)
				}),
			)
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(material.Body1(th, "Which shader do you want? Both are installed.").Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := material.Body2(th, "The same cave with each. You can switch later in Video Settings → Shader Packs.")
			l.Color = muted
			return l.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Spacing: layout.SpaceBetween}.Layout(gtx,
				layout.Flexed(1, card(p.choices[0])),
				layout.Rigid(layout.Spacer{Width: unit.Dp(16)}.Layout),
				layout.Flexed(1, card(p.choices[1])),
			)
		}),
	)
}

// chooseShader asks for a shader the first time a preset with shaders is applied, and
// points Iris at the chosen pack. presetApplied re-applies a saved choice after a
// preset has reset the shader pack. options are the preset's Complementary settings,
// written to whichever Complementary pack ends up selected.
func chooseShader(dataDir, gameDir string, presetApplied bool, options map[string]string) {
	p := loadPrefs(dataDir)
	if p.Shader == "" {
		p.Shader = askShader()
		logf("Shader: %s", shaderName(p.Shader))
	} else if !presetApplied && !p.ShaderPending {
		return
	}
	p.ShaderPending = applyShader(gameDir, p.Shader, options)
	savePrefs(dataDir, p)
}

func shaderName(key string) string {
	if key == shaderRethinking {
		return "Rethinking Voxels"
	}
	return "Complementary Reimagined + Euphoria Patches"
}

// applyShader selects the chosen shader pack in iris.properties and turns shaders on.
// It returns true when Euphoria's patched copy of Complementary doesn't exist yet: plain
// Complementary is used for that start (Euphoria builds its copy as the game starts),
// and the next run switches to the copy.
func applyShader(gameDir, key string, options map[string]string) (pending bool) {
	cfg := filepath.Join(gameDir, "config", "iris.properties")
	pack := ""
	switch key {
	case shaderRethinking:
		pack = findShaderPack(gameDir, func(n string) bool { return strings.HasPrefix(n, "rethinking-voxels") && strings.HasSuffix(n, ".zip") })
	default:
		plain := plainComplementary(gameDir)
		pack = euphoriaComplementary(gameDir, plain)
		if pack == "" {
			pack, pending = plain, plain != ""
		}
	}
	if pack == "" {
		return false
	}
	setOptions(cfg, "=", map[string]string{"shaderPack": pack, "enableShaders": "true"})
	if key != shaderRethinking && options != nil {
		setOptions(filepath.Join(gameDir, "shaderpacks", pack+".txt"), "=", options)
	}
	return pending
}

func findShaderPack(gameDir string, match func(string) bool) string {
	entries, _ := os.ReadDir(filepath.Join(gameDir, "shaderpacks"))
	for _, e := range entries {
		if match(e.Name()) {
			return e.Name()
		}
	}
	return ""
}

// euphoriaComplementary is the folder Euphoria Patches makes from the plain pack, named
// "<plain pack> + EuphoriaPatches_<version>", if it exists yet.
func euphoriaComplementary(gameDir, plain string) string {
	if plain == "" {
		return ""
	}
	base := strings.TrimSuffix(plain, ".zip") + " + EuphoriaPatches_"
	return findShaderPack(gameDir, func(n string) bool {
		st, err := os.Stat(filepath.Join(gameDir, "shaderpacks", n))
		return strings.HasPrefix(n, base) && err == nil && st.IsDir()
	})
}
