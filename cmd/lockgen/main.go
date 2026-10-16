// lockgen resolves pack/pack.json against Modrinth and writes pack/lock.json.
//
//	go run ./cmd/lockgen
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Lonezsi/jraft-launcher/internal/pack"
)

const modrinth = "https://api.modrinth.com/v2"

type mrVersion struct {
	ID           string `json:"id"`
	ProjectID    string `json:"project_id"`
	VersionNum   string `json:"version_number"`
	Dependencies []struct {
		ProjectID string `json:"project_id"`
		Type      string `json:"dependency_type"`
	} `json:"dependencies"`
	Files []struct {
		URL      string            `json:"url"`
		Filename string            `json:"filename"`
		Primary  bool              `json:"primary"`
		Size     int64             `json:"size"`
		Hashes   map[string]string `json:"hashes"`
	} `json:"files"`
}

func getJSON(u string, v any) error {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "Lonezsi/jraft-launcher-lockgen")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

type resolver struct {
	mc      string
	pins    map[string]string // slug or project id -> version_number
	ignore  map[string]bool   // dependency project ids to leave out
	seen    map[string]bool   // project id or slug
	files   []pack.File
	skipped []string
}

// resolve adds the newest 26.x-compatible version of a project plus its
// required dependencies. It returns false if the project has no usable build.
func (r *resolver) resolve(idOrSlug, kind string) bool {
	if r.seen[idOrSlug] {
		return true
	}
	q := url.Values{}
	q.Set("game_versions", fmt.Sprintf(`["%s"]`, r.mc))
	switch kind {
	case "mods":
		q.Set("loaders", `["fabric"]`)
	case "shaderpacks":
		q.Set("loaders", `["iris"]`)
	case "resourcepacks":
		// "minecraft" is Modrinth's loader for resource packs. Without it a pack's
		// listed dependencies resolve to whatever files those projects have, which for
		// some is a NeoForge mod jar landing in resourcepacks/.
		q.Set("loaders", `["minecraft"]`)
	}
	var versions []mrVersion
	if err := getJSON(modrinth+"/project/"+idOrSlug+"/version?"+q.Encode(), &versions); err != nil {
		r.skipped = append(r.skipped, fmt.Sprintf("%s (%v)", idOrSlug, err))
		return false
	}
	// Resource packs are tagged far less carefully than mods, and one made for 26.2
	// almost always works on 26.3: Minecraft just marks it "made for an older version"
	// and loads it. So a pack with no exact build falls back to its newest build for
	// any 26.x. Mods do not get this, because an older mod really does break.
	if len(versions) == 0 && kind == "resourcepacks" {
		var all []struct {
			mrVersion
			GameVersions []string `json:"game_versions"`
		}
		line := strings.SplitN(r.mc, ".", 2)[0] + "."
		if getJSON(modrinth+"/project/"+idOrSlug+"/version?loaders="+url.QueryEscape(`["minecraft"]`), &all) == nil {
			for _, v := range all {
				for _, g := range v.GameVersions {
					if strings.HasPrefix(g, line) && !strings.Contains(g, "-") {
						versions = append(versions, v.mrVersion)
						break
					}
				}
				if len(versions) > 0 {
					break
				}
			}
		}
	}
	if len(versions) == 0 {
		r.skipped = append(r.skipped, idOrSlug+" (no "+r.mc+" build yet)")
		return false
	}
	v := versions[0]
	if want, ok := r.pins[idOrSlug]; ok {
		found := false
		for _, c := range versions {
			if c.VersionNum == want {
				v, found = c, true
				break
			}
		}
		if !found {
			r.skipped = append(r.skipped, idOrSlug+" (pinned version "+want+" not found)")
			return false
		}
	}
	if r.seen[v.ProjectID] {
		r.seen[idOrSlug] = true
		return true
	}
	r.seen[idOrSlug], r.seen[v.ProjectID] = true, true

	for _, d := range v.Dependencies {
		if d.Type != "required" || d.ProjectID == "" || r.ignore[d.ProjectID] {
			continue
		}
		// A resource pack that lists a mod as required (Polytone, say) still works
		// without it in this pack, or the mod is already in mods/; it is not a reason
		// to drop the pack, and the mod is not a resource pack to download.
		if kind == "resourcepacks" && !r.isResourcePack(d.ProjectID) {
			continue
		}
		if !r.resolve(d.ProjectID, kind) {
			r.skipped = append(r.skipped, idOrSlug+" (missing required dependency "+d.ProjectID+")")
			return false
		}
	}
	f := v.Files[0]
	for _, c := range v.Files {
		if c.Primary {
			f = c
		}
	}
	r.files = append(r.files, pack.File{
		Path:    kind + "/" + f.Filename,
		URL:     f.URL,
		SHA512:  f.Hashes["sha512"],
		Size:    f.Size,
		Comment: idOrSlug + " " + v.VersionNum,
	})
	fmt.Printf("  %-32s %s\n", idOrSlug, v.VersionNum)
	return true
}

// isResourcePack asks Modrinth what kind of project an id is.
func (r *resolver) isResourcePack(id string) bool {
	var p struct {
		Type string `json:"project_type"`
	}
	return getJSON(modrinth+"/project/"+id, &p) == nil && p.Type == "resourcepack"
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	raw, err := os.ReadFile("pack/pack.json")
	if err != nil {
		return err
	}
	var src pack.Source
	if err := json.Unmarshal(raw, &src); err != nil {
		return err
	}

	var loaders []struct {
		Loader struct {
			Version string `json:"version"`
			Stable  bool   `json:"stable"`
		} `json:"loader"`
	}
	if err := getJSON("https://meta.fabricmc.net/v2/versions/loader/"+src.Minecraft, &loaders); err != nil {
		return err
	}
	lock := pack.Lock{Name: src.Name, Minecraft: src.Minecraft, JavaArgs: src.JavaArgs,
		ForceOverrides: src.ForceOverrides, OverridesRevision: src.OverridesRevision}
	for _, l := range loaders {
		if l.Loader.Stable {
			lock.LoaderVersion = l.Loader.Version
			break
		}
	}
	if lock.LoaderVersion == "" {
		return fmt.Errorf("no stable Fabric loader for %s", src.Minecraft)
	}
	fmt.Println("Fabric loader", lock.LoaderVersion)

	r := &resolver{mc: src.Minecraft, seen: map[string]bool{}, pins: map[string]string{}, ignore: map[string]bool{}}
	for _, id := range src.IgnoreDependencies {
		r.ignore[id] = true
	}
	// A pin is written with the slug, but the project may first be reached as some
	// other mod's dependency, which names it by id; so both point at the pin.
	for slug, ver := range src.Pins {
		r.pins[slug] = ver
		var proj struct {
			ID string `json:"id"`
		}
		if err := getJSON(modrinth+"/project/"+slug, &proj); err == nil && proj.ID != "" {
			r.pins[proj.ID] = ver
		}
	}
	for _, group := range []struct {
		kind  string
		slugs []string
	}{{"mods", src.Mods}, {"resourcepacks", src.Resourcepacks}, {"shaderpacks", src.Shaderpacks}} {
		fmt.Println(group.kind + ":")
		for _, s := range group.slugs {
			r.resolve(s, group.kind)
		}
	}
	lock.Files = append(r.files, src.Extra...)
	for slug, strip := range src.Strip {
		for i, f := range lock.Files {
			if strings.HasPrefix(f.Comment, slug+" ") {
				lock.Files[i].Strip = strip
			}
		}
	}
	sort.Slice(lock.Files, func(i, j int) bool { return lock.Files[i].Path < lock.Files[j].Path })
	lock.Skipped = r.skipped

	for _, slug := range src.EnabledResourcepacks {
		// A mod's built-in pack (continuity:default, punchy:punchy) is written by id.
		if strings.Contains(slug, ":") {
			lock.ResourcePacks = append(lock.ResourcePacks, slug)
			continue
		}
		found := false
		for _, f := range lock.Files {
			if strings.HasPrefix(f.Path, "resourcepacks/") && strings.HasPrefix(f.Comment, slug+" ") {
				lock.ResourcePacks = append(lock.ResourcePacks, "file/"+strings.TrimPrefix(f.Path, "resourcepacks/"))
				found = true
			}
		}
		// Some built-in packs have no namespace (Simple Voice Chat's black_icons).
		if !found {
			fmt.Printf("enabledResourcepacks: %q is not a pack in the lock, writing it as a built-in pack id\n", slug)
			lock.ResourcePacks = append(lock.ResourcePacks, slug)
		}
	}

	err = filepath.WalkDir("pack/overrides", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		h, err := pack.HashFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("pack/overrides", p)
		lock.Overrides = append(lock.Overrides, pack.File{Path: filepath.ToSlash(rel), SHA512: h})
		return nil
	})
	if err != nil {
		return err
	}

	out, _ := json.MarshalIndent(lock, "", "  ")
	if err := os.WriteFile("pack/lock.json", append(out, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("\nWrote pack/lock.json: %d files, %d overrides\n", len(lock.Files), len(lock.Overrides))
	if len(r.skipped) > 0 {
		fmt.Println("Skipped (no", src.Minecraft, "build):")
		for _, s := range r.skipped {
			fmt.Println("  -", s)
		}
	}
	return nil
}
