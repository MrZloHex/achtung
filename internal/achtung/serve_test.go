package achtung

import (
	"context"
	"errors"
	"io"
	stdlog "log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/MrZloHex/monolink"
)

// relay is a bus that hands every frame to every other client, and keeps
// them all, so a test sees each answer given.
type relay struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]bool
	frames  []string
}

func newRelay(t *testing.T) (*relay, string) {
	t.Helper()
	r := &relay{clients: map[*websocket.Conn]bool{}}
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		r.mu.Lock()
		r.clients[c] = true
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			delete(r.clients, c)
			r.mu.Unlock()
			c.Close()
		}()
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			r.mu.Lock()
			r.frames = append(r.frames, string(data))
			for o := range r.clients {
				if o != c {
					o.WriteMessage(mt, data)
				}
			}
			r.mu.Unlock()
		}
	}))
	t.Cleanup(srv.Close)
	return r, "ws" + strings.TrimPrefix(srv.URL, "http")
}

// answers counts achtung's answers, by the id of the request answered.
func (r *relay) answers() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int{}
	for _, f := range r.frames {
		m, err := monolink.Parse(f)
		if err == nil && strings.HasPrefix(m.From, NodeName) && (m.Verb == monolink.VerbOK || m.Verb == monolink.VerbErr) {
			out[m.ID]++
		}
	}
	return out
}

func quietClient(node, url string, opts ...monolink.Option) *monolink.Client {
	return monolink.New(node, url, append([]monolink.Option{monolink.WithReconnect(0),
		monolink.WithLogger(stdlog.New(io.Discard, "", 0)), monolink.WithDialect(monolink.V2)}, opts...)...)
}

// 15: requests are taken from the inbox in order, beside the object model,
// and every one is answered once — whether achtung answers it or monolink's
// Node does.
func TestEveryRequestIsAnsweredOnce(t *testing.T) {
	r, url := newRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := quietClient(NodeName, url, monolink.WithInbox(64))
	a, err := NewAchtung(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	go a.Serve(c.Inbox())
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { a.Shutdown(); c.Close() }()

	panel := quietClient("MONOWEB", url)
	if err := panel.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer panel.Close()
	panel.SetActor("mzh")

	ask := func(verb, noun string, args ...string) monolink.Message {
		t.Helper()
		m, err := panel.Request(ctx, NodeName, verb, noun, args...)
		var re *monolink.ReplyError
		if err != nil && !errors.As(err, &re) {
			t.Fatalf("%s %s: %v", verb, noun, err)
		}
		return m
	}
	if m := ask("NEW", "TIMER", "tea", "5m"); m.Verb != "OK" || m.Arg(0) != "tea" {
		t.Fatalf("NEW TIMER: %+v", m)
	}
	if m := ask("GET", "LIST"); !slices.Equal(m.Args, []string{"TIMER", "tea"}) {
		t.Fatalf("GET LIST: %+v", m)
	}
	if m := ask("GET", "JOBS.COUNT"); m.Arg(0) != "1" {
		t.Fatalf("GET JOBS.COUNT: %+v", m)
	}
	ask("GET", "UPTIME")
	ask("PING", "PING")
	if m := ask("STOP", "ALARM", "tea"); m.Verb != "ERR" || m.Noun != monolink.CodeNAC {
		t.Fatalf("STOP ALARM on a TIMER: %+v", m)
	}
	if m := ask("STOP", "TIMER", "tea"); m.Verb != "OK" {
		t.Fatalf("STOP TIMER: %+v", m)
	}
	if m := ask("GET", "LIST"); len(m.Args) != 0 {
		t.Fatalf("GET LIST after STOP: %+v", m)
	}
	time.Sleep(100 * time.Millisecond) // a second answer, if there were one, would be on its way
	got := r.answers()
	if len(got) != 8 {
		t.Fatalf("%d requests answered of 8: %v", len(got), got)
	}
	for id, n := range got {
		if n != 1 {
			t.Fatalf("request %s answered %d times", id, n)
		}
	}
}
