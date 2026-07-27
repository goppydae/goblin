package transport

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/goppydae/gapi/core/config"
	"github.com/goppydae/gapi/core/eventbus"
	protopkg "github.com/goppydae/gapi/pkg/proto"
	quic "github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// TLSConfig holds TLS configuration for the client
type TLSConfig struct {
	// InsecureSkipVerify controls whether a client verifies the server's certificate chain and host name.
	InsecureSkipVerify bool
	// CAFile is the path to the CA certificate file.
	CAFile string
}

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
	quicConfig := &quic.Config{
		KeepAlivePeriod: config.QUICStreamTimeout,
		MaxIdleTimeout:  config.QUICIdleTimeout,
	}
	ln, err := quic.ListenAddr(addr, tlsConf, quicConfig)
	if err != nil {
		return nil, err
	}
	q := &QUIC{listener: ln}
	go q.acceptLoop(ln)
	return q, nil
}

func NewQUICClient(addr string, cert *tls.Certificate, tlsConfig TLSConfig) (*QUIC, error) {
	tlsConf, err := CreateClientTLSConfig(tlsConfig)
	if err != nil {
		return nil, err
	}

	if cert != nil {
		tlsConf.Certificates = []tls.Certificate{*cert}
	}
	quicConfig := &quic.Config{
		KeepAlivePeriod: config.QUICStreamTimeout,
		MaxIdleTimeout:  config.QUICIdleTimeout,
	}
	conn, err := quic.DialAddr(context.Background(), addr, tlsConf, quicConfig)
	if err != nil {
		return nil, err
	}
	q := &QUIC{conn: conn}
	go q.handleConn(conn)
	return q, nil
}

// CreateClientTLSConfig builds a tls.Config from the provided settings
func CreateClientTLSConfig(cfg TLSConfig) (*tls.Config, error) {
	tlsConf := &tls.Config{
		NextProtos: []string{"gapi-quic"},
	}

	if cfg.InsecureSkipVerify {
		tlsConf.InsecureSkipVerify = true
	} else if cfg.CAFile != "" {
		// Load CA cert
		caCert, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA file: %w", err)
		}
		caPool := x509.NewCertPool()
		if !caPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to append CA cert")
		}
		tlsConf.RootCAs = caPool
	}

	return tlsConf, nil
}

// ---- Server / Client loops ----

// acceptLoop takes the listener as an argument rather than reading the
// mutable q.listener field: Close() nils that field under q.mu, and an
// unlocked field read here races it. Close() closing the listener makes
// Accept return ErrServerClosed, which remains the shutdown signal.
func (q *QUIC) acceptLoop(ln *quic.Listener) {
	log.Printf("[INFO] QUIC listener started on %s", ln.Addr())
	var tempDelay time.Duration

	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			// Check for intentional shutdown
			if errors.Is(err, quic.ErrServerClosed) {
				log.Println("[INFO] QUIC listener closed")
				return
			}

			// Handle temporary errors with exponential backoff
			if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}
				if max := 1 * time.Second; tempDelay > max {
					tempDelay = max
				}
				log.Printf("[WARN] QUIC accept temporary error: %v; retrying in %v", err, tempDelay)
				time.Sleep(tempDelay)
				continue
			}

			// Fatal error
			log.Printf("[ERROR] QUIC accept fatal error: %v", err)
			return
		}

		// Reset temporary delay on success
		tempDelay = 0
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

func (q *QUIC) PublishRemote(ctx context.Context, e eventbus.Event[*anypb.Any]) error {
	q.mu.Lock()
	conn := q.conn
	q.mu.Unlock()
	if conn == nil {
		return io.ErrUnexpectedEOF
	}

	// Capture values for async closure
	wireTopic := e.Topic
	if e.Scope != "" {
		wireTopic = e.Scope + "/" + e.Topic
	}

	// Async publish to prevent blocking on dead clients
	go func() {
		timeoutCtx, cancel := context.WithTimeout(ctx, config.QUICStreamTimeout)
		defer cancel()
		s, err := conn.OpenStreamSync(timeoutCtx)
		if err != nil {
			return
		}
		defer s.Close()

		env := &protopkg.Envelope{
			Id:      e.ID,
			Topic:   wireTopic,
			Source:  e.Source,
			Type:    "event",
			Payload: e.Payload,
		}

		data, err := proto.Marshal(env)
		if err != nil {
			log.Printf("marshal error: %v\n", err)
			return
		}

		// Length prefix
		lenBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(lenBuf, uint32(len(data)))
		if _, err := s.Write(lenBuf); err != nil {
			return
		}
		if _, err := s.Write(data); err != nil {
			return
		}
	}()

	return nil
}

func (q *QUIC) Broadcast(e eventbus.Event[*anypb.Any]) error {
	return q.PublishRemote(context.Background(), e)
}

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

// GenerateInsecureSelfSignedCert generates a self-signed cert for testing/insecure modes.
func GenerateInsecureSelfSignedCert() (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}
