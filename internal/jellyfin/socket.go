package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"homeflix/internal/store"
)

// keepAliveSeconds is the interval clients are told to send KeepAlive in.
const keepAliveSeconds = 60

func (s *Server) registerSocket(rt *Router) {
	rt.HandleFunc(http.MethodGet, "/socket", s.authed(s.socket))
	rt.HandleFunc(http.MethodGet, "/embywebsocket", s.authed(s.socket))
}

type socketMessage struct {
	MessageType string `json:"MessageType"`
	MessageID   string `json:"MessageId,omitempty"`
	Data        any    `json:"Data,omitempty"`
}

// socket keeps the WebSocket connection clients open for server messages.
// homeflix sends none besides keep-alives, but clients reconnect again and
// again when the connection is refused.
func (s *Server) socket(w http.ResponseWriter, r *http.Request, sess *Session) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Native clients send no Origin; web clients come from their own.
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if s.Shutdown != nil {
		// Closing with a status tells the client the server goes away; a
		// cancelled read would only drop the connection.
		go func() {
			select {
			case <-s.Shutdown.Done():
				c.Close(websocket.StatusGoingAway, "server shutting down")
			case <-ctx.Done():
			}
		}()
	}
	s.Log.Debug("socket opened", "user", sess.User.Name, "client", sess.Token.Client)
	if err := s.sendSocket(ctx, c, "ForceKeepAlive", keepAliveSeconds); err != nil {
		return
	}
	for {
		readCtx, cancel := context.WithTimeout(ctx, 2*keepAliveSeconds*time.Second)
		_, data, err := c.Read(readCtx)
		cancel()
		switch {
		case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
			c.Close(websocket.StatusPolicyViolation, "no keep-alive")
			return
		case err != nil:
			s.Log.Debug("socket closed", "user", sess.User.Name, "client", sess.Token.Client)
			return
		}
		var msg socketMessage
		if json.Unmarshal(data, &msg) == nil && msg.MessageType == "KeepAlive" {
			if err := s.sendSocket(ctx, c, "KeepAlive", nil); err != nil {
				return
			}
		}
	}
}

func (s *Server) sendSocket(ctx context.Context, c *websocket.Conn, typ string, data any) error {
	id, err := store.NewID()
	if err != nil {
		return err
	}
	b, err := json.Marshal(socketMessage{MessageType: typ, MessageID: id, Data: data})
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.Write(wctx, websocket.MessageText, b)
}
