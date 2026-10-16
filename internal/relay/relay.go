// Package relay is the small protocol the JRAFT launcher and the host's jraft-relay use
// to carry Minecraft and Simple Voice Chat through Tailscale Funnel.
//
// Funnel only routes TLS from the internet (its relays pick the machine by the TLS server
// name), and Minecraft is neither TLS nor, for voice chat, TCP. So every stream is one TLS
// connection that Funnel terminates and hands to jraft-relay as plain TCP, and the first
// byte says what it carries:
//
//	'G'  game: the bytes after it are the Minecraft connection, both ways, untouched
//	'V'  voice: UDP datagrams, each sent as a 2-byte big-endian length and the bytes
//
// Voice inside TCP means a lost packet delays the ones after it instead of being
// skipped, which is the price of having no UDP path at all.
package relay

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	Game  byte = 'G'
	Voice byte = 'V'

	// MaxDatagram bounds a voice frame. Simple Voice Chat's MTU setting defaults to
	// 1275 bytes, so anything near this is not voice and the connection is dropped.
	MaxDatagram = 4096
)

var ErrTooBig = errors.New("relay: voice frame too large")

// WriteFrame sends one datagram on a voice stream.
func WriteFrame(w io.Writer, p []byte) error {
	if len(p) > MaxDatagram {
		return ErrTooBig
	}
	buf := make([]byte, 2+len(p))
	binary.BigEndian.PutUint16(buf, uint16(len(p)))
	copy(buf[2:], p)
	_, err := w.Write(buf)
	return err
}

// ReadFrame reads one datagram from a voice stream into buf and returns its length.
func ReadFrame(r io.Reader, buf []byte) (int, error) {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint16(head[:]))
	if n > MaxDatagram || n > len(buf) {
		return 0, ErrTooBig
	}
	_, err := io.ReadFull(r, buf[:n])
	return n, err
}

// Pipe copies both ways between a and b until either side closes, then closes both.
func Pipe(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
	a.Close()
	b.Close()
	<-done
}
