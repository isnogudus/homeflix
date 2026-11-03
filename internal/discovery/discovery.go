// Package discovery answers the UDP broadcast Jellyfin clients send to find
// servers on the local network.
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// Port is where Jellyfin clients broadcast "who is JellyfinServer?".
const Port = 7359

// question is what clients send; Emby clients ask for EmbyServer.
var questions = []string{"who is jellyfinserver?", "who is embyserver?"}

// Reply describes the server to a client.
type Reply struct {
	Address         string `json:"Address"`
	ID              string `json:"Id"`
	Name            string `json:"Name"`
	EndpointAddress any    `json:"EndpointAddress"`
}

// Responder answers discovery requests.
type Responder struct {
	ServerID   string
	ServerName string
	// Host is the address clients reach the HTTP API on; "" means the
	// address of the interface the request came in on.
	Host string
	Port int
	Log  *slog.Logger
	// interfaceAddrs is replaced in tests.
	interfaceAddrs func() ([]net.Addr, error)
}

// Serve answers requests on conn until ctx is done.
func (r *Responder) Serve(ctx context.Context, conn *net.UDPConn) error {
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	buf := make([]byte, 512)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		reply, ok := r.answer(string(buf[:n]), from.Addr())
		if !ok {
			continue
		}
		if _, err := conn.WriteToUDPAddrPort(reply, from); err != nil && r.Log != nil {
			r.Log.Warn("discovery reply failed", "to", from, "err", err)
		}
	}
}

// answer returns the reply to a request from addr, or false when the
// request is not a discovery question or does not come from the local
// network.
func (r *Responder) answer(msg string, from netip.Addr) ([]byte, bool) {
	q := strings.ToLower(strings.TrimSpace(strings.TrimRight(msg, "\x00")))
	known := false
	for _, want := range questions {
		known = known || q == want
	}
	from = from.Unmap()
	if !known || !(from.IsPrivate() || from.IsLoopback() || from.IsLinkLocalUnicast()) {
		return nil, false
	}
	host := r.Host
	if host == "" {
		host = r.localAddressFor(from)
	}
	if host == "" {
		return nil, false
	}
	b, err := json.Marshal(Reply{
		Address: "http://" + net.JoinHostPort(host, strconv.Itoa(r.Port)),
		ID:      r.ServerID,
		Name:    r.ServerName,
	})
	if err != nil {
		return nil, false
	}
	if r.Log != nil {
		r.Log.Debug("discovery request answered", "from", from, "address", host)
	}
	return b, true
}

// localAddressFor picks the address of the interface in the client's
// network, else the first non-loopback IPv4 address.
func (r *Responder) localAddressFor(client netip.Addr) string {
	addrs := r.interfaceAddrs
	if addrs == nil {
		addrs = net.InterfaceAddrs
	}
	list, err := addrs()
	if err != nil {
		return ""
	}
	fallback := ""
	for _, a := range list {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipnet.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		ones, _ := ipnet.Mask.Size()
		if p, err := ip.Prefix(ones); err == nil && p.Contains(client) {
			return ip.String()
		}
		if fallback == "" && ip.Is4() && !ip.IsLoopback() {
			fallback = ip.String()
		}
	}
	return fallback
}
