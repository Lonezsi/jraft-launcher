package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// servers.dat is the multiplayer list, in Minecraft's NBT format (uncompressed): a root
// compound holding a "servers" list of compounds with "name" and "ip". The launcher keeps
// one JRAFT entry at the top, pointing at its own relay on localhost, and leaves every
// other entry as it found it. So this reads and writes the whole format, not just the
// parts it changes.

const (
	tagEnd byte = iota
	tagByte
	tagShort
	tagInt
	tagLong
	tagFloat
	tagDouble
	tagByteArray
	tagString
	tagList
	tagCompound
	tagIntArray
	tagLongArray
)

type nbtField struct {
	name  string
	tag   byte
	value any
}

// nbtCompound keeps fields in their original order.
type nbtCompound []nbtField

type nbtList struct {
	elem  byte
	items []any
}

func (c nbtCompound) get(name string) (any, bool) {
	for _, f := range c {
		if f.name == name {
			return f.value, true
		}
	}
	return nil, false
}

func readNBTString(r *bufio.Reader) (string, error) {
	var n uint16
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return "", err
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return string(b), err
}

func readNBTPayload(r *bufio.Reader, tag byte) (any, error) {
	be := binary.BigEndian
	switch tag {
	case tagByte:
		return r.ReadByte()
	case tagShort:
		var v int16
		return v, binary.Read(r, be, &v)
	case tagInt:
		var v int32
		return v, binary.Read(r, be, &v)
	case tagLong:
		var v int64
		return v, binary.Read(r, be, &v)
	case tagFloat:
		var v uint32
		err := binary.Read(r, be, &v)
		return math.Float32frombits(v), err
	case tagDouble:
		var v uint64
		err := binary.Read(r, be, &v)
		return math.Float64frombits(v), err
	case tagByteArray:
		var n int32
		if err := binary.Read(r, be, &n); err != nil || n < 0 {
			return nil, fmt.Errorf("bad byte array")
		}
		b := make([]byte, n)
		_, err := io.ReadFull(r, b)
		return b, err
	case tagString:
		return readNBTString(r)
	case tagList:
		elem, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		var n int32
		if err := binary.Read(r, be, &n); err != nil || n < 0 {
			return nil, fmt.Errorf("bad list")
		}
		l := nbtList{elem: elem}
		for i := int32(0); i < n; i++ {
			v, err := readNBTPayload(r, elem)
			if err != nil {
				return nil, err
			}
			l.items = append(l.items, v)
		}
		return l, nil
	case tagCompound:
		var c nbtCompound
		for {
			t, err := r.ReadByte()
			if err != nil {
				return nil, err
			}
			if t == tagEnd {
				return c, nil
			}
			name, err := readNBTString(r)
			if err != nil {
				return nil, err
			}
			v, err := readNBTPayload(r, t)
			if err != nil {
				return nil, err
			}
			c = append(c, nbtField{name, t, v})
		}
	case tagIntArray, tagLongArray:
		var n int32
		if err := binary.Read(r, be, &n); err != nil || n < 0 {
			return nil, fmt.Errorf("bad array")
		}
		if tag == tagIntArray {
			v := make([]int32, n)
			return v, binary.Read(r, be, v)
		}
		v := make([]int64, n)
		return v, binary.Read(r, be, v)
	}
	return nil, fmt.Errorf("unknown NBT tag %d", tag)
}

func writeNBTString(w *bytes.Buffer, s string) {
	binary.Write(w, binary.BigEndian, uint16(len(s)))
	w.WriteString(s)
}

func writeNBTPayload(w *bytes.Buffer, tag byte, v any) {
	be := binary.BigEndian
	switch tag {
	case tagByte:
		w.WriteByte(v.(byte))
	case tagShort, tagInt, tagLong:
		binary.Write(w, be, v)
	case tagFloat:
		binary.Write(w, be, math.Float32bits(v.(float32)))
	case tagDouble:
		binary.Write(w, be, math.Float64bits(v.(float64)))
	case tagByteArray:
		b := v.([]byte)
		binary.Write(w, be, int32(len(b)))
		w.Write(b)
	case tagString:
		writeNBTString(w, v.(string))
	case tagList:
		l := v.(nbtList)
		w.WriteByte(l.elem)
		binary.Write(w, be, int32(len(l.items)))
		for _, it := range l.items {
			writeNBTPayload(w, l.elem, it)
		}
	case tagCompound:
		for _, f := range v.(nbtCompound) {
			w.WriteByte(f.tag)
			writeNBTString(w, f.name)
			writeNBTPayload(w, f.tag, f.value)
		}
		w.WriteByte(tagEnd)
	case tagIntArray:
		a := v.([]int32)
		binary.Write(w, be, int32(len(a)))
		binary.Write(w, be, a)
	case tagLongArray:
		a := v.([]int64)
		binary.Write(w, be, int32(len(a)))
		binary.Write(w, be, a)
	}
}

// The entry's name. Section-sign codes: light purple, bold, the pack's colour; then grey.
const serverListName = "§d§l» JRAFT «§r §7click to play"

// addServerEntry puts the pack's server first in the multiplayer list, pointing at the
// launcher's relay. Run before the game starts, since the game rewrites the file itself.
func addServerEntry(gameDir string, port int) error {
	path := filepath.Join(gameDir, "servers.dat")
	root := nbtCompound{}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		r := bufio.NewReader(bytes.NewReader(b))
		if t, err := r.ReadByte(); err == nil && t == tagCompound {
			if _, err := readNBTString(r); err == nil {
				if v, err := readNBTPayload(r, tagCompound); err == nil {
					root = v.(nbtCompound)
				}
			}
		}
	}

	ip := "localhost"
	if port != 25565 {
		ip = fmt.Sprintf("localhost:%d", port)
	}
	entry := nbtCompound{
		{"name", tagString, serverListName},
		{"ip", tagString, ip},
		{"acceptTextures", tagByte, byte(1)},
		{"hidden", tagByte, byte(0)},
	}

	list := nbtList{elem: tagCompound}
	if v, ok := root.get("servers"); ok {
		if l, ok := v.(nbtList); ok && l.elem == tagCompound {
			list = l
		}
	}
	// Ours is recognised by its name or by pointing at the relay, so an older copy of
	// the entry is replaced instead of added to.
	kept := []any{entry}
	for _, it := range list.items {
		c := it.(nbtCompound)
		name, _ := c.get("name")
		addr, _ := c.get("ip")
		a, _ := addr.(string)
		if name == serverListName || a == ip || strings.HasPrefix(a, "localhost") || strings.HasPrefix(a, "127.0.0.1") {
			// Keep the icon the game cached for it, so the list doesn't flicker.
			if icon, ok := c.get("icon"); ok {
				entry = append(entry, nbtField{"icon", tagString, icon})
				kept[0] = entry
			}
			continue
		}
		kept = append(kept, c)
	}
	list.items = kept

	replaced := false
	for i, f := range root {
		if f.name == "servers" {
			root[i] = nbtField{"servers", tagList, list}
			replaced = true
		}
	}
	if !replaced {
		root = append(root, nbtField{"servers", tagList, list})
	}

	var out bytes.Buffer
	out.WriteByte(tagCompound)
	writeNBTString(&out, "")
	writeNBTPayload(&out, tagCompound, root)
	return os.WriteFile(path, out.Bytes(), 0o644)
}
