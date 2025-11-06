package protocol

import (
	"errors"
	"fmt"
	log "log/slog"
	"regexp"
	"strings"
	"sync"
)

type PtclConfig struct {
	Shard   string
	Url     string
	Reconn  uint
	EmitOut func(*Message)
}

type Protocol struct {
	ws *WebSocket

	shard string

	waiterMu sync.Mutex
	waiter   chan *Message

	emitOut func(*Message)
}

func NewProtocol(cfg PtclConfig) (*Protocol, error) {
	ws, err := NewWebSocket(cfg.Url, cfg.Reconn)
	if err != nil {
		log.Error("Failed to init ws connection")
		return nil, err
	}

	ptcl := &Protocol{
		shard:   cfg.Shard,
		ws:      ws,
		emitOut: cfg.EmitOut,
	}

	return ptcl, nil
}

func (ptcl *Protocol) EmitOut(f func(*Message)) {
	ptcl.emitOut = f
}

func (ptcl *Protocol) TransmitReceive(v any) (*Message, error) {
	err := ptcl.Transmit(v)
	if err != nil {
		return nil, err
	}
	return ptcl.Receive(), nil
}

func (ptcl *Protocol) Transmit(v any) error {
	var msg string

	switch m := v.(type) {
	case Message:
		m.From = ptcl.shard
		msg = m.String()
	case string:
		msg = fmt.Sprintf("%s:%s", m, ptcl.shard)
	case []string:
		pay := strings.Join(m, ":")
		msg = fmt.Sprintf("%s:%s", pay, ptcl.shard)
	default:
		log.Error("Provided unsupported type")
		return fmt.Errorf("Unsupported type")
	}

	err := ptcl.ws.Write([]byte(msg))
	if err != nil {
		log.Error("Failed to transmit", "msg", msg, "err", err)
	}
	return err
}

func (ptcl *Protocol) Receive() *Message {
	w := ptcl.installWaiter()
	defer ptcl.clearWaiter()

	resp, _ := <-w
	return resp
}

func (ptcl *Protocol) Run() {
	for {
		in := ptcl.ws.Read()
		switch in.kind {
		case CONN_CLOSE:
			log.Warn("Trying to reconnect on", "url", ptcl.ws.url)
			ptcl.ws.TryReconn()
			log.Info("Succefully reconnected")

		case READ_FAILURE:
			log.Error("Failed to read", "err", in.err)

		case READ_OK:
			if !ptcl.checkRecipient(in.msg) {
				continue
			}

			msg, err := ptcl.Parse(string(in.msg))
			if err != nil {
				log.Warn("Failed to parse", "msg", string(in.msg), "err", err)
				continue
			}

			if w := ptcl.currentWaiter(); w != nil {
				w <- msg
			} else {
				if ptcl.emitOut != nil {
					ptcl.emitOut(msg)
				}
			}
		}
	}
}

func (ptcl *Protocol) installWaiter() chan *Message {
	ptcl.waiterMu.Lock()
	defer ptcl.waiterMu.Unlock()
	ptcl.waiter = make(chan *Message, 1)
	return ptcl.waiter
}

func (ptcl *Protocol) clearWaiter() {
	ptcl.waiterMu.Lock()
	defer ptcl.waiterMu.Unlock()
	if ptcl.waiter != nil {
		close(ptcl.waiter)
		ptcl.waiter = nil
	}
}

func (ptcl *Protocol) currentWaiter() chan *Message {
	ptcl.waiterMu.Lock()
	defer ptcl.waiterMu.Unlock()
	return ptcl.waiter
}

func (ptcl *Protocol) checkRecipient(msg []byte) bool {
	return strings.Split(string(msg), ":")[0] == ptcl.shard
}

func (ptcl *Protocol) Parse(line string) (*Message, error) {
	s := strings.TrimSpace(line)
	if s == "" {
		return nil, errors.New("empty message")
	}
	if strings.ContainsAny(s, " \t\r\n") {
		// spaces not allowed (UART/WebSocket frames are single-line)
		return nil, fmt.Errorf("invalid whitespace present")
	}
	parts := strings.Split(s, ":")
	if len(parts) < 4 {
		return nil, fmt.Errorf("too few fields: got %d, want >= 4", len(parts))
	}

	to := parts[0]
	verb := parts[1]
	noun := parts[2]
	from := parts[len(parts)-1]
	args := append([]string(nil), parts[3:len(parts)-1]...)

	if !isToken(to) && !isHexID(to) && to != "ALL" {
		return nil, fmt.Errorf("invalid TO token: %q", to)
	}
	if !isToken(from) && !isHexID(from) {
		return nil, fmt.Errorf("invalid FROM token: %q", from)
	}

	if !isToken(noun) || !isToken(verb) {
		return nil, fmt.Errorf("invalid NOUN/VERB: %q %q", noun, verb)
	}
	for i, a := range args {
		if !isToken(a) {
			return nil, fmt.Errorf("invalid ARG[%d]: %q", i, a)
		}
	}

	msg := &Message{
		To:   to,
		Verb: strings.ToUpper(verb),
		Noun: strings.ToUpper(noun),
		Args: args,
		From: from,
	}
	return msg, nil
}

var (
	tokenRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	hexIDRe   = regexp.MustCompile(`^[0-9A-F]{2}$`)
	digitsRe  = regexp.MustCompile(`^[0-9]+$`)
	isoBasicR = regexp.MustCompile(`^\d{8}T\d{6}([+-]\d{4})$`)
)

func isToken(s string) bool {
	return tokenRe.MatchString(s)
}

func isHexID(s string) bool {
	return hexIDRe.MatchString(strings.ToUpper(s))
}

type Message struct {
	To   string
	Verb string
	Noun string
	Args []string
	From string
}

func (m *Message) String() string {
	parts := make([]string, 0, 4+len(m.Args))
	parts = append(parts, m.To)
	parts = append(parts, m.Verb)
	parts = append(parts, m.Noun)
	parts = append(parts, m.Args...)
	parts = append(parts, m.From)
	return strings.Join(parts, ":")
}

func (m *Message) Error(reason string, args ...string) {
	m.Verb = "ERR"
	m.Noun = reason
	m.Args = args
}

func (m *Message) Ok(reason string, args ...string) {
	m.Verb = "OK"
	m.Noun = reason
	m.Args = args
}
