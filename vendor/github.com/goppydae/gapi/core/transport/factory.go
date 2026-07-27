package transport

import (
	"crypto/tls"
	"fmt"

	"github.com/goppydae/gapi/core/config"
	"github.com/goppydae/gapi/core/eventbus"
	"github.com/goppydae/gapi/internal/logging/logcore"
	"google.golang.org/protobuf/types/known/anypb"
)

// Local in-proc transport.
func NewLocal() eventbus.Transport[*anypb.Any] {
	return &Local[*anypb.Any]{}
}

// QUIC server transport.
func NewQUICServerTransport(addr, tlsCert, tlsKey string) (eventbus.Transport[*anypb.Any], error) {
	var cert tls.Certificate
	var err error

	if tlsCert == "" || tlsKey == "" {
		// No cert configured: fall back to a single canonical self-signed
		// generator (1-year validity) rather than a second, shorter-lived one.
		// This is insecure and only appropriate for dev/test, so warn loudly.
		logcore.Warn().Str("module", "transport").Str("addr", addr).
			Msg("no TLS cert/key configured; using auto-generated self-signed certificate (insecure, not for production)")
		cert, err = GenerateInsecureSelfSignedCert()
		if err != nil {
			return nil, fmt.Errorf("generate self-signed cert: %w", err)
		}
	} else {
		cert, err = tls.LoadX509KeyPair(tlsCert, tlsKey)
		if err != nil {
			return nil, fmt.Errorf("load cert: %w", err)
		}
	}
	// 👇 removed generic index syntax
	return NewQUICServer(addr, cert)
}

// QUIC client transport.
func NewQUICClientTransport(cfg config.TransportConfig) (eventbus.Transport[*anypb.Any], error) {
	var cert *tls.Certificate

	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		c, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("load cert: %w", err)
		}
		cert = &c
	}
	tlsCfg := TLSConfig{
		InsecureSkipVerify: cfg.InsecureSkipVerify,
		CAFile:             cfg.TLSCA,
	}
	return NewQUICClient(cfg.Address, cert, tlsCfg)
}

// Config-driven server.
func NewServerFromConfig(cfg config.TransportConfig) (eventbus.Transport[*anypb.Any], error) {
	switch cfg.Type {
	case "local":
		return &Local[*anypb.Any]{}, nil
	case "quic":
		return NewQUICServerTransport(cfg.Address, cfg.TLSCert, cfg.TLSKey)
	default:
		return nil, fmt.Errorf("unknown transport type: %s", cfg.Type)
	}
}

// Config-driven client.
func NewClientFromConfig(cfg config.TransportConfig) (eventbus.Transport[*anypb.Any], error) {
	switch cfg.Type {
	case "local":
		return &Local[*anypb.Any]{}, nil
	case "quic":
		return NewQUICClientTransport(cfg)
	default:
		return nil, fmt.Errorf("unknown transport type: %s", cfg.Type)
	}
}
