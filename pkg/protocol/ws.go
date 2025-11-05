package protocol

import (
	log "log/slog"
	"time"

	ws "github.com/gorilla/websocket"
)

type WebSocket struct {
	conn   *ws.Conn
	url    string
	reconn uint
}

func NewWebSocket(url string, reconn uint) (*WebSocket, error) {
	log.Debug("init websocket protocol", "url", url)

	web := &WebSocket{
		url:    url,
		reconn: reconn,
	}

	conn, _, err := ws.DefaultDialer.Dial(url, nil)
	if err != nil {
		log.Error("Failed to dial url", "err", err)
		return nil, err
	}
	web.conn = conn

	return web, nil
}

func (web *WebSocket) Write(payload []byte) error {
	log.Debug("Write ws", "msg", string(payload))
	err := web.conn.WriteMessage(ws.TextMessage, payload)
	return err
}

type WsIncomeKind uint

const (
	CONN_CLOSE WsIncomeKind = iota
	READ_FAILURE
	READ_OK
)

type Income struct {
	kind WsIncomeKind
	msg  []byte
	err  error
}

func (web *WebSocket) Read() Income {
	_, msg, err := web.conn.ReadMessage()
	if err != nil {
		if WsIsClosed(err) {
			return Income{
				kind: CONN_CLOSE,
				err:  err,
			}
		}
		return Income{
			kind: READ_FAILURE,
			err:  err,
		}
	}

	log.Debug("Read ws", "msg", string(msg))
	return Income{
		kind: READ_OK,
		msg:  msg,
	}
}

func (web *WebSocket) TryReconn() {
	for {
		conn, _, err := ws.DefaultDialer.Dial(web.url, nil)
		if err == nil {
			web.conn = conn
			break
		}

		time.Sleep(time.Second * time.Duration(web.reconn))
	}
}

func WsIsClosed(err error) bool {
	return ws.IsCloseError(err,
		ws.CloseNormalClosure,
		ws.CloseGoingAway,
		ws.CloseAbnormalClosure)
}
