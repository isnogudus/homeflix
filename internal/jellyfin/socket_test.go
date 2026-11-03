package jellyfin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSocketKeepAlive(t *testing.T) {
	s := newTestServer(t, nil)
	shutdown, stop := context.WithCancel(context.Background())
	defer stop()
	s.Shutdown = shutdown
	addUser(t, s, "anna", "pw", false)
	c := &apiClient{t: t, h: s.Handler()}
	login(t, c, "anna", "pw")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ws := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, resp, err := websocket.Dial(ctx, ws+"/socket", nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("socket without token: %v %v", resp, err)
	}
	conn, _, err := websocket.Dial(ctx, ws+"/socket?ApiKey="+c.token+"&deviceId=d1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	read := func() socketMessage {
		t.Helper()
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m socketMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := read(); m.MessageType != "ForceKeepAlive" || m.Data != float64(keepAliveSeconds) || m.MessageID == "" {
		t.Errorf("first message: %+v", m)
	}
	conn.Write(ctx, websocket.MessageText, []byte(`{"MessageType":"SessionsStart","Data":"0,1500"}`))
	conn.Write(ctx, websocket.MessageText, []byte(`{"MessageType":"KeepAlive"}`))
	if m := read(); m.MessageType != "KeepAlive" {
		t.Errorf("reply to KeepAlive: %+v", m)
	}
	// Shutting the server down closes the socket.
	stop()
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Errorf("after shutdown: %v", err)
	}
}
