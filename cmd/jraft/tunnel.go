package main

import (
	"crypto/tls"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Lonezsi/jraft-launcher/internal/relay"
)

// The JRAFT server is reachable only through Tailscale Funnel, which carries TLS and
// nothing else. So while the game runs, the launcher listens on this PC for Minecraft
// (TCP) and Simple Voice Chat (UDP) and carries both to the host over TLS. See
// internal/relay for the format and cmd/jraft-relay for the other end.

type tunnel struct {
	remote   string // host:port of the Funnel endpoint
	gamePort int
	mu       sync.Mutex
	voiceTo  *net.UDPAddr // where the game's voice chat last sent from
	voice    net.Conn     // the TLS voice stream, opened on first use
}

// relayIP, when set (tests only), is dialled instead of the name's address, so a PC on
// the tailnet can still test the public Funnel path.
var relayIP string

func dialRelay(remote string, kind byte) (net.Conn, error) {
	host, port, _ := strings.Cut(remote, ":")
	addr := remote
	if relayIP != "" {
		addr = relayIP + ":" + port
	}
	c, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr,
		&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return nil, err
	}
	if _, err := c.Write([]byte{kind}); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// startTunnel opens the local listeners. The game listener prefers 25565, so "localhost"
// in the server list works too, and falls back to any free port.
func startTunnel(remote string) (*tunnel, error) {
	t := &tunnel{remote: remote}
	l, err := net.Listen("tcp", "127.0.0.1:25565")
	if err != nil {
		if l, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return nil, err
		}
	}
	t.gamePort = l.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				r, err := dialRelay(t.remote, relay.Game)
				if err != nil {
					logf("Couldn't reach the JRAFT server: %v", err)
					c.Close()
					return
				}
				relay.Pipe(c, r)
			}()
		}
	}()

	// Voice chat: the server tells clients to send to 127.0.0.1:24454, which is here.
	// If another program already holds that port, voice chat just won't connect; the
	// game itself is unaffected.
	u, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 24454})
	if err != nil {
		logf("Voice chat relay unavailable: %v", err)
		return t, nil
	}
	go t.voiceLoop(u)
	return t, nil
}

func (t *tunnel) voiceLoop(u *net.UDPConn) {
	buf := make([]byte, relay.MaxDatagram)
	for {
		n, from, err := u.ReadFromUDP(buf)
		if err != nil {
			return
		}
		t.mu.Lock()
		t.voiceTo = from
		c := t.voice
		if c == nil {
			if c, err = dialRelay(t.remote, relay.Voice); err != nil {
				t.mu.Unlock()
				continue // dropped; voice chat retries on its own
			}
			t.voice = c
			go t.voiceBack(u, c)
		}
		t.mu.Unlock()
		if relay.WriteFrame(c, buf[:n]) != nil {
			t.dropVoice(c)
		}
	}
}

// voiceBack hands datagrams from the server back to the game's voice chat.
func (t *tunnel) voiceBack(u *net.UDPConn, c net.Conn) {
	buf := make([]byte, relay.MaxDatagram)
	for {
		n, err := relay.ReadFrame(c, buf)
		if err != nil {
			t.dropVoice(c)
			return
		}
		t.mu.Lock()
		to := t.voiceTo
		t.mu.Unlock()
		if to != nil {
			u.WriteToUDP(buf[:n], to)
		}
	}
}

func (t *tunnel) dropVoice(c net.Conn) {
	t.mu.Lock()
	if t.voice == c {
		t.voice = nil
	}
	t.mu.Unlock()
	c.Close()
}
