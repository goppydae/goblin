package supervisor

import (
	"context"
	"crypto/tls"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/goppydae/goblin/internal/logattr"
)

// CertManager serves the control-plane certificate and reloads it from
// disk when the file changes, so a rotation does not require a restart.
// One instance backs every plane on the shared listener: the TLS config
// holds the callbacks, not a snapshot of the key pair.
type CertManager struct {
	mu       sync.RWMutex
	cert     *tls.Certificate
	certFile string
	keyFile  string
}

func NewCertManager(certFile, keyFile string) (*CertManager, error) {
	cm := &CertManager{
		certFile: certFile,
		keyFile:  keyFile,
	}
	if err := cm.Load(); err != nil {
		return nil, err
	}
	return cm, nil
}

func (cm *CertManager) Load() error {
	cert, err := tls.LoadX509KeyPair(cm.certFile, cm.keyFile)
	if err != nil {
		return err
	}
	cm.mu.Lock()
	cm.cert = &cert
	cm.mu.Unlock()
	return nil
}

func (cm *CertManager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.cert, nil
}

func (cm *CertManager) GetClientCertificate(req *tls.CertificateRequestInfo) (*tls.Certificate, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.cert, nil
}

// Watch polls the certificate file and reloads on a newer mtime. It
// runs until ctx is cancelled; Supervisor tracks it as a tierRun loop
// so shutdown joins it.
func (cm *CertManager) Watch(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	var lastMod time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(cm.certFile)
			if err != nil {
				continue
			}
			if info.ModTime().After(lastMod) {
				slog.Default().LogAttrs(ctx, slog.LevelInfo, "certificate change detected, reloading")
				if err := cm.Load(); err != nil {
					slog.Default().LogAttrs(ctx, slog.LevelError, "failed to reload certificate", logattr.Err(err))
				} else {
					slog.Default().LogAttrs(ctx, slog.LevelInfo, "certificate reloaded")
					lastMod = info.ModTime()
				}
			}
		}
	}
}
