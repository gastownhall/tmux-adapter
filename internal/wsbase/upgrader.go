package wsbase

import (
	"log"
	"net/http"

	"nhooyr.io/websocket"
)

// AcceptWebSocket upgrades an HTTP request to a WebSocket connection
// with the given origin patterns.
func AcceptWebSocket(w http.ResponseWriter, r *http.Request, originPatterns []string) (*websocket.Conn, error) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: originPatterns,
	})
	if err != nil {
		log.Printf("websocket accept: %v", err)
		return nil, err
	}
	return conn, nil
}
