package discovery

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"testing"
	"time"
)

func testResponder() *Responder {
	return &Responder{
		ServerID: "0123456789abcdef0123456789abcdef", ServerName: "homeflix-dev", Port: 8096,
		interfaceAddrs: func() ([]net.Addr, error) {
			return []net.Addr{
				&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
				&net.IPNet{IP: net.ParseIP("10.0.0.5"), Mask: net.CIDRMask(24, 32)},
				&net.IPNet{IP: net.ParseIP("192.168.21.15"), Mask: net.CIDRMask(24, 32)},
			}, nil
		},
	}
}

func TestAnswer(t *testing.T) {
	r := testResponder()
	for _, tc := range []struct {
		msg, from, address string
	}{
		{"who is JellyfinServer?", "192.168.21.97", "http://192.168.21.15:8096"},
		{"Who is EmbyServer?\x00", "10.0.0.9", "http://10.0.0.5:8096"},
		{"who is JellyfinServer?", "172.16.1.1", "http://10.0.0.5:8096"},
		{"who is JellyfinServer?", "203.0.113.7", ""}, // not the local network
		{"hello", "192.168.21.97", ""},
	} {
		b, ok := r.answer(tc.msg, netip.MustParseAddr(tc.from))
		if tc.address == "" {
			if ok {
				t.Errorf("%q from %s answered: %s", tc.msg, tc.from, b)
			}
			continue
		}
		var reply Reply
		if !ok || json.Unmarshal(b, &reply) != nil || reply.Address != tc.address || reply.ID != r.ServerID || reply.Name != "homeflix-dev" {
			t.Errorf("%q from %s: %s, %v", tc.msg, tc.from, b, ok)
		}
	}
	r.Host = "192.168.21.15"
	if b, _ := r.answer("who is JellyfinServer?", netip.MustParseAddr("10.0.0.9")); !json.Valid(b) || string(b) == "" {
		t.Errorf("fixed host: %s", b)
	}
}

func TestServe(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	r := testResponder()
	go func() { done <- r.Serve(ctx, conn) }()

	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.WriteToUDP([]byte("who is JellyfinServer?"), conn.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	n, _, err := client.ReadFromUDP(buf)
	var reply Reply
	if err != nil || json.Unmarshal(buf[:n], &reply) != nil || reply.Address != "http://127.0.0.1:8096" {
		t.Errorf("reply %q, %v", buf[:n], err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Serve: %v", err)
	}
}
