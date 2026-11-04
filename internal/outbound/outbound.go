// Package outbound is the only way homeflix connects to other hosts. A
// Client reaches only the hosts it was created for, logs every request and
// refuses everything else, so nothing leaves the house unless the operator
// configured it.
package outbound

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrNotAllowed is returned for requests to hosts the client was not
// created for.
var ErrNotAllowed = errors.New("outbound connection not allowed")

// dial opens connections; tests replace it to prove that nothing dials.
var dial = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext

// Client sends HTTP requests to a fixed set of hosts.
type Client struct {
	allowed map[string]bool
	log     *slog.Logger
	http    *http.Client

	mu    sync.Mutex
	hours [24]hourCount // by hour of the day, for Counts
	last  time.Time
}

// hourCount counts the requests of one hour.
type hourCount struct {
	hour       int64 // Unix time / 3600
	ok, failed int
}

// Counts are the requests of the last 24 hours.
type Counts struct {
	OK, Failed int
	Last       time.Time
}

// Counts returns the requests sent in the last 24 hours: answered with a
// status below 400, and failed otherwise. Refused requests were never sent
// and do not count.
func (c *Client) Counts() Counts {
	if c == nil {
		return Counts{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().Unix() / 3600
	var out Counts
	for _, h := range c.hours {
		if now-h.hour < 24 {
			out.OK += h.ok
			out.Failed += h.failed
		}
	}
	out.Last = c.last
	return out
}

func (c *Client) count(ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	h := now.Unix() / 3600
	b := &c.hours[h%24]
	if b.hour != h {
		*b = hourCount{hour: h}
	}
	if ok {
		b.ok++
	} else {
		b.failed++
	}
	c.last = now
}

// New returns a client for the given host names. Without hosts every
// request is refused.
func New(log *slog.Logger, hosts ...string) *Client {
	allowed := map[string]bool{}
	for _, h := range hosts {
		allowed[strings.ToLower(h)] = true
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dial(ctx, network, addr)
		},
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   4,
		ForceAttemptHTTP2:     true,
	}
	return &Client{
		allowed: allowed,
		log:     log,
		http: &http.Client{
			Transport: transport,
			Timeout:   time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if !allowed[strings.ToLower(req.URL.Hostname())] {
					return fmt.Errorf("redirect to %s: %w", req.URL.Hostname(), ErrNotAllowed)
				}
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
	}
}

// Do sends req if its host is allowed. The log line names method, host and
// path; query strings and headers, which may hold search terms or
// credentials, are left out.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	host := strings.ToLower(req.URL.Hostname())
	if c == nil || !c.allowed[host] {
		return nil, fmt.Errorf("%s: %w", host, ErrNotAllowed)
	}
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("%s: only https is allowed", req.URL)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	c.count(err == nil && status < 400)
	if c.log != nil {
		c.log.Debug("outbound request", "method", req.Method, "host", host, "path", req.URL.Path,
			"status", status, "duration", time.Since(start).Round(time.Millisecond), "err", err)
	}
	return resp, err
}
