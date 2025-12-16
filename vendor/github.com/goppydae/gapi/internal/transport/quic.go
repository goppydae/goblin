package transport

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"log"
	"strings"
	"sync"

	"github.com/goppydae/gapi/internal/eventbus"
	protopkg "github.com/goppydae/gapi/internal/proto"
	quic "github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

type QUIC struct {
	listener *quic.Listener
	conn     *quic.Conn
	mu       sync.Mutex

	onRemote func(eventbus.Event[*anypb.Any])
}

// ---- Constructors ----

func NewQUICServer(addr string, cert tls.Certificate) (*QUIC, error) {
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"gapi-quic"},
	}
	ln, err := quic.ListenAddr(addr, tlsConf, nil)
	if err != nil {
		return nil, err
	}
	q := &QUIC{listener: ln}
	go q.acceptLoop()
	return q, nil
}

func NewQUICClient(addr string, cert *tls.Certificate) (*QUIC, error) {
	tlsConf := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"gapi-quic"},
	}
	if cert != nil {
		tlsConf.Certificates = []tls.Certificate{*cert}
	}
	conn, err := quic.DialAddr(context.Background(), addr, tlsConf, nil)
	if err != nil {
		return nil, err
	}
	q := &QUIC{conn: conn}
	go q.handleConn(conn)
	return q, nil
}

// ---- Server / Client loops ----

func (q *QUIC) acceptLoop() {
	for {
		conn, err := q.listener.Accept(context.Background())
		if err != nil {
			log.Println("QUIC accept:", err)
			return
		}
		go q.handleConn(conn)
	}
}

func (q *QUIC) handleConn(conn *quic.Conn) {
	q.mu.Lock()
	q.conn = conn
	q.mu.Unlock()
	for {
		s, err := conn.AcceptStream(context.Background())
		if err != nil {
			log.Println("AcceptStream:", err)
			return
		}
		go q.handleStream(s)
	}
}

func (q *QUIC) handleStream(s *quic.Stream) {
	defer s.Close()

	var lenBuf [4]byte
	if _, err := io.ReadFull(s, lenBuf[:]); err != nil {
		log.Println("read len:", err)
		return
	}
	n := binary.BigEndian.Uint32(lenBuf[:])

	data := make([]byte, n)
	if _, err := io.ReadFull(s, data); err != nil {
		log.Println("read payload:", err)
		return
	}

	var env protopkg.Envelope
	if err := proto.Unmarshal(data, &env); err != nil {
		log.Println("unmarshal:", err)
		return
	}

	var payload *anypb.Any
	if env.Payload != nil {
		payload = env.Payload
	}

	scope := ""
	topic := env.Topic
	if i := strings.IndexByte(env.Topic, '/'); i > 0 {
		scope = env.Topic[:i]
		topic = env.Topic[i+1:]
	}

	e := eventbus.Event[*anypb.Any]{
		ID:      env.Id,
		Scope:   scope,
		Topic:   topic,
		Source:  env.Source,
		Payload: payload,
	}

	if q.onRemote != nil {
		q.onRemote(e)
	}
}

// ---- Publish / Broadcast ----

func (q *QUIC) PublishRemote(e eventbus.Event[*anypb.Any]) error {
	q.mu.Lock()
	conn := q.conn
	q.mu.Unlock()
	if conn == nil {
		return io.ErrUnexpectedEOF
	}

	s, err := conn.OpenStreamSync(context.Background())
	if err != nil {
		return err
	}
	defer s.Close()

	wireTopic := e.Topic
	if e.Scope != "" {
		wireTopic = e.Scope + "/" + e.Topic
	}
	env := &protopkg.Envelope{
		Id:      e.ID,
		Topic:   wireTopic,
		Source:  e.Source,
		Type:    "event",
		Payload: e.Payload,
	}

	b, err := proto.Marshal(env)
	if err != nil {
		return err
	}

	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(b)))

	if _, err := s.Write(lenBuf[:]); err != nil {
		return err
	}
	if _, err := s.Write(b); err != nil {
		return err
	}
	return nil
}

func (q *QUIC) Broadcast(e eventbus.Event[*anypb.Any]) error { return q.PublishRemote(e) }

func (q *QUIC) OnRemoteEvent(fn func(eventbus.Event[*anypb.Any])) { q.onRemote = fn }

func (q *QUIC) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	var err error
	if q.listener != nil {
		err = q.listener.Close()
		q.listener = nil
	}
	if q.conn != nil {
		_ = q.conn.CloseWithError(0, "shutdown")
		q.conn = nil
	}
	return err
}
