// icon draws the JRAFT icon (a 16x16 pixel-art grass block with a J) and
// writes it as assets/icon.png (512px) and assets/icon.ico.
//
//	go run ./tools/icon
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
)

func main() {
	src := draw16()
	os.MkdirAll("assets", 0o755)
	write("assets/icon.png", encode(scale(src, 512)))

	// ICO with PNG-compressed entries (supported since Windows Vista).
	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	var imgs [][]byte
	for _, s := range sizes {
		imgs = append(imgs, encode(scale(src, s)))
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

func draw16() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	r := rand.New(rand.NewSource(7))
	grass := []color.NRGBA{{0x5f, 0xb8, 0x4a, 255}, {0x4f, 0xa0, 0x3c, 255}, {0x72, 0xcc, 0x5a, 255}}
	dirt := []color.NRGBA{{0x8a, 0x5a, 0x3a, 255}, {0x74, 0x4a, 0x2f, 255}, {0x9b, 0x6a, 0x46, 255}, {0x63, 0x3f, 0x28, 255}}
	// How far the grass hangs down in each column.
	drip := []int{4, 5, 4, 4, 6, 5, 4, 5, 4, 4, 5, 6, 4, 5, 4, 4}
	for x := 0; x < 16; x++ {
		for y := 0; y < 16; y++ {
			if y < drip[x] {
				img.Set(x, y, grass[r.Intn(len(grass))])
			} else {
				img.Set(x, y, dirt[r.Intn(len(dirt))])
			}
		}
	}
	// A cream "J" in the dirt.
	j := []string{
		"..######..",
		"....##....",
		"....##....",
		"....##....",
		".##.##....",
		"..###.....",
	}
	cream := color.NRGBA{0xf3, 0xe9, 0xd2, 255}
	shadow := color.NRGBA{0x4a, 0x2f, 0x1e, 255}
	for dy, row := range j {
		for dx, c := range row {
			if c == '#' {
				x, y := 3+dx, 8+dy
				if x+1 < 16 && y+1 < 16 {
					img.Set(x+1, y+1, shadow)
				}
			}
		}
	}
	for dy, row := range j {
		for dx, c := range row {
			if c == '#' {
				img.Set(3+dx, 8+dy, cream)
			}
		}
	}
	return img
}

// scale resizes with nearest-neighbour so the pixels stay crisp.
func scale(src *image.NRGBA, size int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dst.Set(x, y, src.At(x*16/size, y*16/size))
		}
	}
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
