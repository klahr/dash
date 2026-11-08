// Package tunnel connects runners to a hub. A runner (`dash runner`) opens
// one WebSocket out to the hub and keeps it open; the hub then reaches the
// runner's Dash API through it, as if it could dial the runner.
//
// mux.go multiplexes many byte streams (one per HTTP connection) over that
// WebSocket, with per-stream flow control so one slow reader (a log stream,
// a terminal) can't stall the others.
package tunnel

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Frame types. Every frame is one binary WebSocket message:
// type (1 byte) | stream id (4 bytes, big endian) | payload.
const (
	frameOpen   = 1 // a new stream; the receiver Accepts it
	frameData   = 2 // payload is stream data
	frameClose  = 3 // the sender is done with the stream, in both directions
	frameWindow = 4 // payload is a uint32: the receiver consumed this many more bytes
)

const (
	maxPayload   = 32 << 10  // data bytes per frame
	streamWindow = 256 << 10 // bytes a sender may have in flight per stream
	writeTimeout = 30 * time.Second
	pingEvery    = 20 * time.Second
	deadAfter    = 60 * time.Second // no message or pong for this long: the link is gone
	acceptQueue  = 64
)

// ErrSessionClosed is what streams report once their WebSocket is gone.
var ErrSessionClosed = errors.New("tunnel closed")

// Session is one end of a tunnel. Either end may Open streams; the other
// end Accepts them. A Session is a net.Listener, so an http.Server can serve
// the streams the other end opens.
type Session struct {
	ws  *websocket.Conn
	wmu sync.Mutex // one writer at a time on ws

	mu      sync.Mutex
	streams map[uint32]*Stream
	nextID  uint32
	accept  chan *Stream
	done    chan struct{}
	once    sync.Once
}

// NewSession runs a tunnel over ws. The two ends must pass different values
// for dialer, so the streams they open get distinct ids.
func NewSession(ws *websocket.Conn, dialer bool) *Session {
	s := &Session{
		ws:      ws,
		streams: map[uint32]*Stream{},
		accept:  make(chan *Stream, acceptQueue),
		done:    make(chan struct{}),
		nextID:  2,
	}
	if dialer {
		s.nextID = 1
	}
	ws.SetReadLimit(maxPayload + 64)
	_ = ws.SetReadDeadline(time.Now().Add(deadAfter))
	ws.SetPongHandler(func(string) error { return ws.SetReadDeadline(time.Now().Add(deadAfter)) })
	go s.readLoop()
	go s.pingLoop()
	return s
}

// Done is closed once the tunnel has gone.
func (s *Session) Done() <-chan struct{} { return s.done }

// Close tears the tunnel down; every stream on it fails.
func (s *Session) Close() error {
	s.once.Do(func() {
		close(s.done)
		// WriteControl may run alongside a WriteMessage in progress.
		_ = s.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		_ = s.ws.Close()
		s.mu.Lock()
		for _, st := range s.streams {
			st.fail(ErrSessionClosed)
		}
		s.streams = map[uint32]*Stream{}
		s.mu.Unlock()
	})
	return nil
}

// Accept waits for the next stream the other end opens.
func (s *Session) Accept() (net.Conn, error) {
	select {
	case st := <-s.accept:
		return st, nil
	case <-s.done:
		return nil, net.ErrClosed
	}
}

// Addr is the listener's (made-up) address.
func (s *Session) Addr() net.Addr { return addr{} }

// Open starts a new stream to the other end.
func (s *Session) Open() (net.Conn, error) {
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		return nil, ErrSessionClosed
	default:
	}
	id := s.nextID
	s.nextID += 2
	st := newStream(s, id)
	s.streams[id] = st
	s.mu.Unlock()
	if err := s.write(frameOpen, id, nil); err != nil {
		return nil, err
	}
	return st, nil
}

func (s *Session) write(typ byte, id uint32, payload []byte) error {
	buf := make([]byte, 5+len(payload))
	buf[0] = typ
	binary.BigEndian.PutUint32(buf[1:5], id)
	copy(buf[5:], payload)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	select {
	case <-s.done:
		return ErrSessionClosed
	default:
	}
	_ = s.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := s.ws.WriteMessage(websocket.BinaryMessage, buf); err != nil {
		go s.Close()
		return ErrSessionClosed
	}
	return nil
}

func (s *Session) pingLoop() {
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			if err := s.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				s.Close()
				return
			}
		}
	}
}

func (s *Session) readLoop() {
	defer s.Close()
	for {
		typ, msg, err := s.ws.ReadMessage()
		if err != nil {
			return
		}
		_ = s.ws.SetReadDeadline(time.Now().Add(deadAfter))
		if typ != websocket.BinaryMessage || len(msg) < 5 {
			return // not our protocol
		}
		id := binary.BigEndian.Uint32(msg[1:5])
		payload := msg[5:]
		s.mu.Lock()
		st := s.streams[id]
		s.mu.Unlock()
		switch msg[0] {
		case frameOpen:
			if st != nil || id%2 == s.nextID%2 {
				return // reused id, or one only we may open
			}
			st = newStream(s, id)
			s.mu.Lock()
			s.streams[id] = st
			s.mu.Unlock()
			select {
			case s.accept <- st:
			default: // nobody is accepting fast enough
				st.Close()
			}
		case frameData:
			if st != nil && !st.receive(payload) {
				return // the sender ignored our window
			}
		case frameClose:
			if st != nil {
				st.fail(io.EOF)
				s.forget(id)
			}
		case frameWindow:
			if st != nil && len(payload) == 4 {
				st.credit(int(binary.BigEndian.Uint32(payload)))
			}
		default:
			return
		}
	}
}

func (s *Session) forget(id uint32) {
	s.mu.Lock()
	delete(s.streams, id)
	s.mu.Unlock()
}

// Stream is one connection inside a tunnel. It implements net.Conn.
type Stream struct {
	s  *Session
	id uint32

	mu       sync.Mutex
	buf      bytes.Buffer // received, not yet read
	err      error        // set once the stream is over: io.EOF (remote closed) or why
	closed   bool         // closed here
	sendWin  int          // bytes we may still send
	unacked  int          // bytes read here but not yet credited to the sender
	rdl, wdl time.Time
	rwake    chan struct{}
	wwake    chan struct{}
}

func newStream(s *Session, id uint32) *Stream {
	return &Stream{s: s, id: id, sendWin: streamWindow, rwake: make(chan struct{}, 1), wwake: make(chan struct{}, 1)}
}

func wake(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

func (st *Stream) receive(p []byte) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed || st.err != nil {
		return true // late data for a stream we're done with
	}
	if st.buf.Len()+len(p) > streamWindow {
		return false
	}
	st.buf.Write(p)
	wake(st.rwake)
	return true
}

func (st *Stream) credit(n int) {
	st.mu.Lock()
	st.sendWin += n
	st.mu.Unlock()
	wake(st.wwake)
}

// fail ends the stream: buffered data can still be read, then reads return err.
func (st *Stream) fail(err error) {
	st.mu.Lock()
	if st.err == nil {
		st.err = err
	}
	st.mu.Unlock()
	wake(st.rwake)
	wake(st.wwake)
}

// wait blocks until woken, the deadline passes or the session ends.
func (st *Stream) wait(c chan struct{}, deadline time.Time) error {
	var timeout <-chan time.Time
	if !deadline.IsZero() {
		d := time.Until(deadline)
		if d <= 0 {
			return os.ErrDeadlineExceeded
		}
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-c:
		return nil
	case <-timeout:
		return os.ErrDeadlineExceeded
	case <-st.s.done:
		st.fail(ErrSessionClosed)
		return nil
	}
}

func (st *Stream) Read(p []byte) (int, error) {
	for {
		st.mu.Lock()
		if st.closed {
			st.mu.Unlock()
			return 0, net.ErrClosed
		}
		if st.buf.Len() > 0 {
			n, _ := st.buf.Read(p)
			st.unacked += n
			var ack int
			if st.unacked >= streamWindow/2 && st.err == nil {
				ack, st.unacked = st.unacked, 0
			}
			st.mu.Unlock()
			if ack > 0 {
				var b [4]byte
				binary.BigEndian.PutUint32(b[:], uint32(ack))
				_ = st.s.write(frameWindow, st.id, b[:])
			}
			return n, nil
		}
		if st.err != nil {
			err := st.err
			st.mu.Unlock()
			return 0, err
		}
		dl := st.rdl
		st.mu.Unlock()
		if err := st.wait(st.rwake, dl); err != nil {
			return 0, err
		}
	}
}

func (st *Stream) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		st.mu.Lock()
		switch {
		case st.closed:
			st.mu.Unlock()
			return written, net.ErrClosed
		case st.err != nil:
			st.mu.Unlock()
			return written, io.ErrClosedPipe // the other end has gone
		}
		if st.sendWin <= 0 {
			dl := st.wdl
			st.mu.Unlock()
			if err := st.wait(st.wwake, dl); err != nil {
				return written, err
			}
			continue
		}
		n := min(len(p), st.sendWin, maxPayload)
		st.sendWin -= n
		st.mu.Unlock()
		if err := st.s.write(frameData, st.id, p[:n]); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

// Close ends the stream at both ends.
func (st *Stream) Close() error {
	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return nil
	}
	st.closed = true
	gone := st.err != nil
	st.mu.Unlock()
	wake(st.rwake)
	wake(st.wwake)
	st.s.forget(st.id)
	if !gone {
		_ = st.s.write(frameClose, st.id, nil)
	}
	return nil
}

func (st *Stream) LocalAddr() net.Addr  { return addr{} }
func (st *Stream) RemoteAddr() net.Addr { return addr{} }

func (st *Stream) SetDeadline(t time.Time) error {
	st.mu.Lock()
	st.rdl, st.wdl = t, t
	st.mu.Unlock()
	wake(st.rwake)
	wake(st.wwake)
	return nil
}

func (st *Stream) SetReadDeadline(t time.Time) error {
	st.mu.Lock()
	st.rdl = t
	st.mu.Unlock()
	wake(st.rwake)
	return nil
}

func (st *Stream) SetWriteDeadline(t time.Time) error {
	st.mu.Lock()
	st.wdl = t
	st.mu.Unlock()
	wake(st.wwake)
	return nil
}

type addr struct{}

func (addr) Network() string { return "tunnel" }
func (addr) String() string  { return "tunnel:0" }
