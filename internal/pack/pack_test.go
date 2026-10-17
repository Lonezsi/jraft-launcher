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

func TestPackCompatible(t *testing.T) {
	cases := []struct {
		name, mcmeta string
		want         bool
	}{
		// Shapes from packs in this pack, which Minecraft 26.3 (97.1) loads as compatible.
		{"Better Leaves", `{"pack":{"pack_format":15,"supported_formats":[15,255],"min_format":15,"max_format":255}}`, true},
		{"Grassrise", `{"pack":{"description":{"text":"x"},"min_format":[97,1],"max_format":[97,1]}}`, true},
		{"Fresh Flowers", `{"pack":{"pack_format":46,"supported_formats":[46,128],"min_format":46,"max_format":128}}`, true},
		{"Connected Paths", `{"pack":{"min_format":88,"max_format":97.1}}`, true},
		{"jraft merge", `{"pack":{"min_format":97,"max_format":97}}`, true},
		{"Bushier Bushes", `{"pack":{"min_format":66,"max_format":100,"pack_format":66}}`, true},
		{"old style range", `{"pack":{"pack_format":34,"supported_formats":{"min_inclusive":34,"max_inclusive":200}}}`, true},
		// Made for other versions.
		{"26.1 only", `{"pack":{"min_format":76,"max_format":88}}`, false},
		{"97.0 only", `{"pack":{"min_format":[97,0],"max_format":[97,0]}}`, false},
		{"newer minor", `{"pack":{"min_format":[97,2],"max_format":[98,0]}}`, false},
		{"old pack_format only", `{"pack":{"pack_format":34}}`, false},
		{"old supported list", `{"pack":{"pack_format":46,"supported_formats":[46,55]}}`, false},
	}
	for _, c := range cases {
		got, err := PackCompatible([]byte(c.mcmeta), "97.1")
		if err != nil || got != c.want {
			t.Errorf("%s: got %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	if _, err := PackCompatible([]byte(`{"pack":{}}`), "97.1"); err == nil {
		t.Error("a pack without any format should be an error")
	}
}
