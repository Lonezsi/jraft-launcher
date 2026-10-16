package pack

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestStripZip(t *testing.T) {
	name := filepath.Join(t.TempDir(), "p.zip")
	f, _ := os.Create(name)
	w := zip.NewWriter(f)
	for _, n := range []string{"pack.mcmeta", "26.3/assets/minecraft/items/shield.json", "26.3/assets/minecraft/shaders/core/item.fsh", "assets/minecraft/items/poppy.json"} {
		e, _ := w.Create(n)
		e.Write([]byte(n))
	}
	w.Close()
	f.Close()

	if err := StripZip(name, []string{"26.3/assets/minecraft/items/", "pack.mcmeta"}); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(name)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var got []string
	for _, f := range r.File {
		got = append(got, f.Name)
	}
	want := []string{"26.3/assets/minecraft/shaders/core/item.fsh", "assets/minecraft/items/poppy.json"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}
