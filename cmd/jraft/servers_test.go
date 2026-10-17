package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestServerEntryKeepsOtherServers(t *testing.T) {
	dir := t.TempDir()
	src := os.Getenv("JRAFT_SERVERS_DAT")
	if src != "" {
		b, _ := os.ReadFile(src)
		os.WriteFile(filepath.Join(dir, "servers.dat"), b, 0o644)
	}
	for i := 0; i < 2; i++ { // twice: the entry must not be duplicated
		if err := addServerEntry(dir, 25565); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "servers.dat"))
	r := bufio.NewReader(bytes.NewReader(b))
	r.ReadByte()
	readNBTString(r)
	v, err := readNBTPayload(r, tagCompound)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := v.(nbtCompound).get("servers")
	items := l.(nbtList).items
	first := items[0].(nbtCompound)
	if n, _ := first.get("name"); n != serverListName {
		t.Fatalf("first entry is %v", n)
	}
	ours := 0
	for _, it := range items {
		n, _ := it.(nbtCompound).get("name")
		ip, _ := it.(nbtCompound).get("ip")
		t.Logf("%v -> %v", n, ip)
		if n == serverListName {
			ours++
		}
	}
	if ours != 1 {
		t.Fatalf("%d JRAFT entries", ours)
	}
}

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		tag, cur string
		want     bool
	}{{"v1.10.0", "v1.9.0", true}, {"v1.9.0", "v1.10.0", false}, {"v1.10.0", "v1.10.0", false}, {"v1.10.1", "v1.10.0", true}, {"v2.0.0", "v1.99.9", true}, {"garbage", "v1.0.0", false}} {
		if got := newerVersion(c.tag, c.cur); got != c.want {
			t.Errorf("newerVersion(%s, %s) = %v", c.tag, c.cur, got)
		}
	}
}
