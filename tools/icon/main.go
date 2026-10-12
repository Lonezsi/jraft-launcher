// icon turns assets/icon-source.png (any square-ish PNG) into the app icon
// files: assets/icon.png (512px, used for the Mac app) and assets/icon.ico.
//
//	go run ./tools/icon
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"os"

	"golang.org/x/image/draw"
)

func main() {
	f, err := os.Open("assets/icon-source.png")
	if err != nil {
		panic(err)
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		panic(err)
	}
	write("assets/icon.png", encode(fit(src, 512)))

	// ICO with PNG-compressed entries (supported since Windows Vista).
	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	var imgs [][]byte
	for _, s := range sizes {
		imgs = append(imgs, encode(fit(src, s)))
	}
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		d := byte(s)
		if s == 256 {
			d = 0
		}
		b.Write([]byte{d, d, 0, 0})
		binary.Write(&b, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&b, binary.LittleEndian, [2]uint32{uint32(len(imgs[i])), uint32(offset)})
		offset += len(imgs[i])
	}
	for _, img := range imgs {
		b.Write(img)
	}
	write("assets/icon.ico", b.Bytes())
}

// fit scales src to a size x size square, centred, keeping its aspect ratio.
func fit(src image.Image, size int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	sb := src.Bounds()
	w, h := size, size
	if sb.Dx() > sb.Dy() {
		h = size * sb.Dy() / sb.Dx()
	} else {
		w = size * sb.Dx() / sb.Dy()
	}
	r := image.Rect((size-w)/2, (size-h)/2, (size-w)/2+w, (size-h)/2+h)
	draw.CatmullRom.Scale(dst, r, src, sb, draw.Over, nil)
	return dst
}

func encode(img image.Image) []byte {
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func write(name string, data []byte) {
	if err := os.WriteFile(name, data, 0o644); err != nil {
		panic(err)
	}
}
