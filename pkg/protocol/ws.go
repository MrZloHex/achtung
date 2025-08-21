package protocol

import ws "github.com/gorilla/websocket"

func WsIsClosed(err error) bool {
	return ws.IsCloseError(err,
		ws.CloseNormalClosure,
		ws.CloseGoingAway,
		ws.CloseAbnormalClosure)
}
