package outbound

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
)

func TestRefusesHostsNotAllowed(t *testing.T) {
	dials := 0
	old := dial
	dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials++
		return nil, errors.New("dial blocked by test")
	}
	defer func() { dial = old }()

	c := New(nil, "api.example.org")
	for _, u := range []string{"https://other.example.org/x", "https://api.example.org.evil/x"} {
		req, _ := http.NewRequest("GET", u, nil)
		if _, err := c.Do(req); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s: err %v, want ErrNotAllowed", u, err)
		}
	}
	plain, _ := http.NewRequest("GET", "http://api.example.org/x", nil)
	if _, err := c.Do(plain); err == nil {
		t.Error("plain http allowed")
	}
	var none *Client
	req, _ := http.NewRequest("GET", "https://api.example.org/x", nil)
	if _, err := none.Do(req); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("nil client: %v", err)
	}
	if _, err := New(nil).Do(req); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("client without hosts: %v", err)
	}
	if dials != 0 {
		t.Errorf("%d dials for refused requests", dials)
	}
	// An allowed host is dialled.
	if _, err := c.Do(req); err == nil || dials == 0 {
		t.Errorf("allowed host not dialled: dials %d, err %v", dials, err)
	}
}
