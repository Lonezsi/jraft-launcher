package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func varint(n int) []byte {
	var b []byte
	for {
		c := byte(n & 0x7f)
		n >>= 7
		if n != 0 {
			c |= 0x80
		}
		b = append(b, c)
		if n == 0 {
			return b
		}
	}
}

func readVarint(r io.Reader) int {
	n := 0
	for i := 0; i < 5; i++ {
		var b [1]byte
		r.Read(b[:])
		n |= int(b[0]&0x7f) << (7 * i)
		if b[0]&0x80 == 0 {
			break
		}
	}
	return n
}

// Live: a Minecraft status ping through the real tunnel and Funnel. JRAFT_LIVE=1 to run.
func TestLiveTunnel(t *testing.T) {
	if os.Getenv("JRAFT_LIVE") == "" {
		t.Skip("set JRAFT_LIVE=1")
	}
	relayIP = os.Getenv("JRAFT_RELAY_IP")
	tun, err := startTunnel("jriter.dikdik-kettle.ts.net:10000")
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tun.gamePort), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	var hs bytes.Buffer
	hs.Write(varint(0))
	hs.Write(varint(775))
	hs.Write(varint(9))
	hs.WriteString("localhost")
	binary.Write(&hs, binary.BigEndian, uint16(25565))
	hs.Write(varint(1))
	c.Write(append(varint(hs.Len()), hs.Bytes()...))
	c.Write([]byte{1, 0})
	readVarint(c)
	readVarint(c)
	n := readVarint(c)
	body := make([]byte, n)
	if _, err := io.ReadFull(c, body); err != nil {
		t.Fatal(err)
	}
	var st struct {
		Version struct{ Name string }     `json:"version"`
		Players struct{ Online, Max int } `json:"players"`
	}
	json.Unmarshal(body, &st)
	t.Logf("through the tunnel: Minecraft %s, %d/%d players", st.Version.Name, st.Players.Online, st.Players.Max)
	if st.Version.Name == "" {
		t.Fatal("no status answer")
	}
}
