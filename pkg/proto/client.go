package proto

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// HandlerFunc processes an incoming message addressed to this node.
// Use req.Reply() to send a response back through the concentrator.
type HandlerFunc func(req *Request)

type Option func(*Client)

func WithReconnect(interval time.Duration) Option {
	return func(c *Client) { c.reconnectInterval = interval }
}

func WithLogger(l *log.Logger) Option {
	return func(c *Client) { c.log = l }
}

func WithDialTimeout(d time.Duration) Option {
	return func(c *Client) { c.dialTimeout = d }
}

// WithOnConnect sets a callback that fires after every successful connection
// (including reconnects). Useful for re-announcing state to the concentrator.
func WithOnConnect(fn func(*Client)) Option {
	return func(c *Client) { c.onConnect = fn }
}

// WithInbox enables an inbox channel of the given buffer size.
// Every incoming message is pushed there (non-blocking drop on full).
// Read it with Inbox(). This is the integration point for event-loop
// consumers like Bubble Tea — no need to register Handle() callbacks.
func WithInbox(size int) Option {
	return func(c *Client) { c.inbox = make(chan Message, size) }
}

type Client struct {
	nodeID string
	url    string

	reconnectInterval time.Duration
	dialTimeout       time.Duration
	log               *log.Logger
	onConnect         func(*Client)

	conn   *websocket.Conn
	connMu sync.Mutex

	// Verb-keyed handlers. "*" is the catch-all.
	handlers  map[string]HandlerFunc
	handlerMu sync.RWMutex

	// Inbox receives every incoming message (for event-loop consumers like TUI).
	// Nil unless WithInbox is used.
	inbox chan Message

	done chan struct{}
	wg   sync.WaitGroup
}

// New creates a concentrator client.
//
//	c := concentrator.New("LUCH", "ws://hal9000:9090/ws")
func New(nodeID, url string, opts ...Option) *Client {
	c := &Client{
		nodeID:            strings.ToUpper(nodeID),
		url:               url,
		reconnectInterval: 3 * time.Second,
		dialTimeout:       5 * time.Second,
		log:               log.Default(),
		handlers:          make(map[string]HandlerFunc),
		done:              make(chan struct{}),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Client) NodeID() string { return c.nodeID }

// Connected reports whether the WebSocket connection is currently alive.
func (c *Client) Connected() bool {
	c.connMu.Lock()
	ok := c.conn != nil
	c.connMu.Unlock()
	return ok
}

// Inbox returns the channel that receives all incoming messages.
// Returns nil if WithInbox was not used.
func (c *Client) Inbox() <-chan Message { return c.inbox }

// Handle registers a handler that fires when an incoming message's VERB
// matches the given verb. Use "*" to catch all unmatched verbs.
//
//	c.Handle("LAMP", func(req *concentrator.Request) {
//	    switch req.Msg.Noun {
//	    case "ON":  ...
//	    case "OFF": ...
//	    }
//	    req.Reply("OK", "LAMP")
//	})
func (c *Client) Handle(verb string, fn HandlerFunc) {
	c.handlerMu.Lock()
	c.handlers[strings.ToUpper(verb)] = fn
	c.handlerMu.Unlock()
}

// Connect dials the concentrator and starts the read loop.
// Blocks until the first successful connection or ctx cancellation.
func (c *Client) Connect(ctx context.Context) error {
	if err := c.dial(ctx); err != nil {
		return fmt.Errorf("initial connection: %w", err)
	}
	c.wg.Add(1)
	go c.readLoop()
	return nil
}

// Close shuts down the client and waits for goroutines to exit.
func (c *Client) Close() error {
	close(c.done)
	c.connMu.Lock()
	var err error
	if c.conn != nil {
		err = c.conn.Close()
		c.conn = nil
	}
	c.connMu.Unlock()
	c.wg.Wait()
	return err
}

// Send builds and writes a message to the concentrator.
// FROM is filled in automatically from the client's node ID.
//
//	c.Send("VERTEX", "LAMP", "ON")
//	c.Send("ACHTUNG", "NEW", "TIMER", "qwe", "10s")
func (c *Client) Send(to, verb, noun string, args ...string) error {
	wire := Encode(to, verb, noun, c.nodeID, args...)
	return c.writeRaw(wire)
}

// SendRaw writes an already-encoded wire string. Use Send() when possible.
func (c *Client) SendRaw(wire string) error {
	return c.writeRaw(wire)
}

func (c *Client) writeRaw(wire string) error {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.conn == nil {
		return fmt.Errorf("not connected")
	}
	return c.conn.WriteMessage(websocket.TextMessage, []byte(wire))
}

func (c *Client) dial(ctx context.Context) error {
	dialer := websocket.Dialer{HandshakeTimeout: c.dialTimeout}
	conn, _, err := dialer.DialContext(ctx, c.url, nil)
	if err != nil {
		return err
	}

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	if c.onConnect != nil {
		c.onConnect(c)
	}
	return nil
}

func (c *Client) readLoop() {
	defer c.wg.Done()

	for {
		select {
		case <-c.done:
			return
		default:
		}

		c.connMu.Lock()
		conn := c.conn
		c.connMu.Unlock()
		if conn == nil {
			if !c.tryReconnect() {
				return
			}
			continue
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				c.log.Printf("[%s] connection closed", c.nodeID)
				return
			}
			c.log.Printf("[%s] read error: %v", c.nodeID, err)

			c.connMu.Lock()
			c.conn = nil
			c.connMu.Unlock()

			if !c.tryReconnect() {
				return
			}
			continue
		}

		raw := strings.TrimSpace(string(data))
		if raw == "" {
			continue
		}

		msg, err := Parse(raw)
		if err != nil {
			c.log.Printf("[%s] %v", c.nodeID, err)
			continue
		}

		c.dispatch(msg)
	}
}

func (c *Client) tryReconnect() bool {
	if c.reconnectInterval <= 0 {
		return false
	}

	for {
		select {
		case <-c.done:
			return false
		case <-time.After(c.reconnectInterval):
		}

		c.log.Printf("[%s] reconnecting to %s ...", c.nodeID, c.url)
		ctx, cancel := context.WithTimeout(context.Background(), c.dialTimeout)
		err := c.dial(ctx)
		cancel()
		if err == nil {
			c.log.Printf("[%s] reconnected", c.nodeID)
			return true
		}
		c.log.Printf("[%s] reconnect failed: %v", c.nodeID, err)
	}
}

func (c *Client) dispatch(msg Message) {
	// Push to inbox (non-blocking) for event-loop consumers.
	if c.inbox != nil {
		select {
		case c.inbox <- msg:
		default:
			c.log.Printf("[%s] inbox full, dropping: %s", c.nodeID, msg.Raw)
		}
	}

	// Dispatch to verb handlers (for headless / callback-style usage).
	verb := strings.ToUpper(msg.Verb)

	c.handlerMu.RLock()
	fn, ok := c.handlers[verb]
	if !ok {
		fn, ok = c.handlers["*"]
	}
	c.handlerMu.RUnlock()

	if ok {
		go fn(&Request{Msg: msg, client: c})
	}
}
