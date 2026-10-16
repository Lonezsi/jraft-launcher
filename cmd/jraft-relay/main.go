// jraft-relay runs on the host, behind Tailscale Funnel, and hands the streams the
// JRAFT launcher sends to the Minecraft server and to Simple Voice Chat.
//
// Funnel terminates TLS on the public port and connects here with plain TCP. Each
// connection says in its first byte whether it is the game or voice (see internal/relay).
// The two targets are fixed and local: this cannot be used to reach anything else.
//
//	jraft-relay -listen 127.0.0.1:25600 -game 127.0.0.1:25565 -voice 127.0.0.1:24454
package main

import (
	"flag"
	"log"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/Lonezsi/jraft-launcher/internal/relay"
)

var (
	listen = flag.String("listen", "127.0.0.1:25600", "where Funnel connects to")
	game   = flag.String("game", "127.0.0.1:25565", "the Minecraft server")
	voice  = flag.String("voice", "127.0.0.1:24454", "Simple Voice Chat's UDP port")
	// A bound on simultaneous streams, so a flood of connections from the internet
	// cannot exhaust the host. Each player uses two.
	maxConns = flag.Int("max", 64, "most streams at once")
)

var open atomic.Int64

func main() {
	flag.Parse()
	log.SetOutput(os.Stdout)
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("relaying %s -> game %s, voice %s", *listen, *game, *voice)
	for {
		c, err := l.Accept()
		if err != nil {
			log.Print(err)
			time.Sleep(time.Second)
			continue
		}
		if open.Load() >= int64(*maxConns) {
			c.Close()
			continue
		}
		open.Add(1)
		go func() {
			defer open.Add(-1)
			handle(c)
		}()
	}
}

func handle(c net.Conn) {
	defer c.Close()
	// The first byte has to arrive promptly; a connection that says nothing is dropped.
	c.SetReadDeadline(time.Now().Add(15 * time.Second))
	var kind [1]byte
	if _, err := c.Read(kind[:]); err != nil {
		return
	}
	c.SetReadDeadline(time.Time{})
	switch kind[0] {
	case relay.Game:
		s, err := net.DialTimeout("tcp", *game, 10*time.Second)
		if err != nil {
			log.Printf("game: %v", err)
			return
		}
		relay.Pipe(c, s)
	case relay.Voice:
		voiceStream(c)
	}
}

// voiceStream gives each player's voice stream its own UDP socket, so Simple Voice Chat
// sees one address per player, as it would without the relay.
func voiceStream(c net.Conn) {
	addr, err := net.ResolveUDPAddr("udp", *voice)
	if err != nil {
		return
	}
	u, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		log.Printf("voice: %v", err)
		return
	}
	defer u.Close()
	go func() {
		buf := make([]byte, relay.MaxDatagram)
		for {
			// Voice chat sends keep-alives every second, so two minutes of silence
			// means the player is gone.
			u.SetReadDeadline(time.Now().Add(2 * time.Minute))
			n, err := u.Read(buf)
			if err != nil {
				c.Close()
				return
			}
			if relay.WriteFrame(c, buf[:n]) != nil {
				return
			}
		}
	}()
	buf := make([]byte, relay.MaxDatagram)
	for {
		n, err := relay.ReadFrame(c, buf)
		if err != nil {
			return
		}
		u.Write(buf[:n])
	}
}
