package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/Lonezsi/jraft-launcher/internal/relay"
)

// fakeRelay stands in for jraft-relay behind Funnel: TLS, then the kind byte. A game
// stream echoes what it gets until the test kicks it; a voice stream echoes frames and
// also sends whatever is pushed to it.
type fakeRelay struct {
	addr string
	kick chan struct{}
	push chan []byte
}

func startFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "example.com"},
		DNSNames:              []string{"example.com"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)

	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	relayIP, relayRoots = "127.0.0.1", pool
	t.Cleanup(func() { relayIP, relayRoots = "", nil })

	f := &fakeRelay{
		addr: fmt.Sprintf("example.com:%d", l.Addr().(*net.TCPAddr).Port),
		kick: make(chan struct{}),
		push: make(chan []byte),
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go f.handle(c)
		}
	}()
	return f
}

func (f *fakeRelay) handle(c net.Conn) {
	defer c.Close()
	var kind [1]byte
	if _, err := io.ReadFull(c, kind[:]); err != nil {
		return
	}
	switch kind[0] {
	case relay.Game:
		go func() { io.Copy(c, c) }()
		<-f.kick
	case relay.Voice:
		go func() {
			for p := range f.push {
				if relay.WriteFrame(c, p) != nil {
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
			relay.WriteFrame(c, buf[:n])
		}
	}
}

func echoOnce(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != msg {
		t.Fatalf("got %q, want %q", got, msg)
	}
}

// After the server drops the player, joining again from the server list has to work
// with the same launcher still running.
func TestTunnelRejoinAfterKick(t *testing.T) {
	f := startFakeRelay(t)
	tun, err := startTunnel(f.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer tun.close()
	game := fmt.Sprintf("127.0.0.1:%d", tun.gamePort)

	c1, err := net.Dial("tcp", game)
	if err != nil {
		t.Fatal(err)
	}
	echoOnce(t, c1, "first join")
	if !tun.inUse() || !tun.used.Load() {
		t.Fatal("tunnel doesn't count the open connection")
	}

	f.kick <- struct{}{} // the server ends the first connection
	c1.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadAll(c1); err != nil {
		t.Fatalf("first connection didn't end cleanly: %v", err)
	}
	c1.Close()
	waitUntil(t, func() bool { return !tun.inUse() })

	c2, err := net.Dial("tcp", game)
	if err != nil {
		t.Fatalf("rejoin refused: %v", err)
	}
	defer c2.Close()
	echoOnce(t, c2, "second join")
}

// The game's voice socket closes on a disconnect while the server may still be sending to
// it. Voice chat from the rejoined game, on a new socket, has to get through regardless.
func TestTunnelVoiceAfterRejoin(t *testing.T) {
	f := startFakeRelay(t)
	tun, err := startTunnel(f.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer tun.close()
	if tun.udp == nil {
		t.Skip("voice port in use")
	}
	voice := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 24454}

	a, err := net.DialUDP("udp", nil, voice)
	if err != nil {
		t.Fatal(err)
	}
	udpEcho(t, a, "before")
	a.Close()

	// The server still talks to the old address, which nobody listens on now.
	f.push <- []byte("to the old socket")
	time.Sleep(300 * time.Millisecond)

	b, err := net.DialUDP("udp", nil, voice)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	udpEcho(t, b, "after")
}

func udpEcho(t *testing.T, c *net.UDPConn, msg string) {
	t.Helper()
	buf := make([]byte, 64)
	// Voice chat resends on its own; so does this, in case the first one raced the dial.
	for i := 0; i < 20; i++ {
		c.Write([]byte(msg))
		c.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, err := c.Read(buf)
		if err == nil && string(buf[:n]) == msg {
			return
		}
	}
	t.Fatalf("no voice echo for %q", msg)
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestWaitForGame(t *testing.T) {
	finishes := func(running func() bool, tun *tunnel) bool {
		done := make(chan struct{})
		go func() { waitForGame(running, tun, time.Millisecond, 50*time.Millisecond); close(done) }()
		select {
		case <-done:
			return true
		case <-time.After(300 * time.Millisecond):
			return false
		}
	}
	no := func() bool { return false }

	// The game was never found and never connected: a failed launch.
	if !finishes(no, &tunnel{}) {
		t.Error("waits forever for a game that never started")
	}
	// The game connected, but its process can't be found: keep the tunnel up.
	used := &tunnel{}
	used.used.Store(true)
	if finishes(no, used) {
		t.Error("quit while the game may still be running")
	}
	// Connected right now, process not found: never cut the player off.
	busy := &tunnel{}
	busy.used.Store(true)
	busy.open.Store(1)
	if finishes(no, busy) {
		t.Error("quit with the game connected")
	}
	// The game ran, was kicked (no connection) and is still open: wait.
	if finishes(func() bool { return true }, used) {
		t.Error("quit while the game is open")
	}
	// The game ran and then closed.
	calls := 0
	closes := func() bool { calls++; return calls < 3 }
	if !finishes(closes, used) {
		t.Error("didn't notice the game closing")
	}
	// One failed look at the process list isn't the game closing.
	calls = 0
	blip := func() bool { calls++; return calls != 3 }
	if finishes(blip, used) {
		t.Error("quit on a single missed process check")
	}
}

func TestCommandLinesHaveGame(t *testing.T) {
	dir := `C:\Users\p\AppData\Roaming\PrismLauncher\instances\jraft\minecraft`
	prism := `"C:/x/javaw.exe" -XX:+UseZGC ` + gameMarker + ` -Xms1024m -Xmx6144m -cp C:/x/NewLaunch.jar org.prismlauncher.EntryPoint`
	if !commandLinesHaveGame(prism, dir) {
		t.Error("marker not found")
	}
	old := `"C:/x/javaw.exe" -Djava.library.path=C:/Users/p/AppData/Roaming/PrismLauncher/instances/jraft/minecraft/natives -cp a.jar`
	if !commandLinesHaveGame(old, dir) {
		t.Error("game directory not found")
	}
	if commandLinesHaveGame(`"C:/x/javaw.exe" -XX:+UseZGC -cp C:/x/NewLaunch.jar org.prismlauncher.EntryPoint`, dir) {
		t.Error("another Java taken for the game")
	}
}
