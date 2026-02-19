package proto

import (
	"fmt"
	"strings"
)

// Wire format (DSKY-style):
//   TO:VERB:NOUN[:ARG1:ARG2:...]:FROM
//
// Minimum 4 fields. First = TO, second = VERB, third = NOUN, last = FROM.
// Everything between NOUN and FROM is variable-length args.
//
// Examples from the real system:
//   VERTEX:LAMP:OFF:LUCH
//   VERTEX:LED:BRIGHT:255:LUCH
//   ACHTUNG:NEW:TIMER:qwe:10s:LUCH
//   LUCH:OK:TIMER:qwe:ACHTUNG

const Sep = ":"

type Message struct {
	To   string
	Verb string
	Noun string
	Args []string
	From string
	Raw  string
}

func (m Message) String() string {
	parts := make([]string, 0, 4+len(m.Args))
	parts = append(parts, m.To, m.Verb, m.Noun)
	parts = append(parts, m.Args...)
	parts = append(parts, m.From)
	return strings.Join(parts, Sep)
}

// Parse decodes a raw wire string into a Message.
// Returns an error if the format has fewer than 4 colon-separated fields.
func Parse(raw string) (Message, error) {
	parts := strings.Split(raw, Sep)
	if len(parts) < 4 {
		return Message{}, fmt.Errorf("bad message (need at least TO:VERB:NOUN:FROM): %q", raw)
	}

	return Message{
		To:   parts[0],
		Verb: parts[1],
		Noun: parts[2],
		Args: parts[3 : len(parts)-1],
		From: parts[len(parts)-1],
		Raw:  raw,
	}, nil
}

// Encode builds a wire string from individual fields.
// Convenience wrapper so callers don't have to construct a Message struct.
func Encode(to, verb, noun, from string, args ...string) string {
	m := Message{To: to, Verb: verb, Noun: noun, Args: args, From: from}
	return m.String()
}

// Request wraps an incoming message and the client that received it,
// giving handlers a way to reply back through the concentrator.
type Request struct {
	Msg    Message
	client *Client
}

// Reply sends a response back to the originator.
//
//	req.Reply("OK", "LAMP")           -> SENDER:OK:LAMP:US
//	req.Reply("OK", "TIMER", "qwe")   -> SENDER:OK:TIMER:qwe:US
func (r *Request) Reply(verb, noun string, args ...string) error {
	return r.client.Send(r.Msg.From, verb, noun, args...)
}
