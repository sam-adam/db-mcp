// Package tunnel implements constrained, loopback-only SSH forwarding. It never
// accepts a caller supplied destination: each Tunnel is constructed from config.
package tunnel

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sam-adam/db-mcp/internal/config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type State string

const (
	Disconnected State = "disconnected"
	Connecting   State = "connecting"
	Connected    State = "connected"
	Reconnecting State = "reconnecting"
	Failed       State = "failed"
)

type Status struct {
	Profile           string    `json:"profile"`
	State             State     `json:"state"`
	LocalAddress      string    `json:"-"`
	LastError         string    `json:"last_error,omitempty"`
	LastConnected     time.Time `json:"last_connected,omitempty"`
	ReconnectAttempts uint64    `json:"reconnect_attempts"`
	Generation        uint64    `json:"generation"`
}
type Tunnel struct {
	profileID  string
	profile    config.SSHProfile
	logger     *slog.Logger
	listeners  map[string]net.Listener
	mu         sync.RWMutex
	client     *ssh.Client
	state      State
	lastErr    string
	connected  time.Time
	attempts   uint64
	generation uint64
	cancel     context.CancelFunc
	done       chan struct{}
	started    atomic.Bool
}

func New(profileID string, p config.SSHProfile, logger *slog.Logger) *Tunnel {
	if p.Port == 0 {
		p.Port = 22
	}
	if p.KeepaliveInterval.Value() == 0 {
		p.KeepaliveInterval = config.Duration(30 * time.Second)
	}
	if p.KeepaliveFailures == 0 {
		p.KeepaliveFailures = 3
	}
	if p.Reconnect.InitialDelay.Value() == 0 {
		p.Reconnect.InitialDelay = config.Duration(time.Second)
	}
	if p.Reconnect.MaxDelay.Value() == 0 {
		p.Reconnect.MaxDelay = config.Duration(30 * time.Second)
	}
	return &Tunnel{profileID: profileID, profile: p, logger: logger, listeners: map[string]net.Listener{}, state: Disconnected, done: make(chan struct{})}
}
func (t *Tunnel) Start(ctx context.Context) error {
	if !t.started.CompareAndSwap(false, true) {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	go t.maintain(runCtx)
	return nil
}

// AddDestination creates a loopback-only forward for one configured endpoint.
// The endpoint is supplied by the trusted configuration manager, never MCP input.
func (t *Tunnel) AddDestination(ctx context.Context, destination string) (string, error) {
	t.mu.Lock()
	if l := t.listeners[destination]; l != nil {
		a := l.Addr().String()
		t.mu.Unlock()
		return a, nil
	}
	t.mu.Unlock()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	t.mu.Lock()
	if existing := t.listeners[destination]; existing != nil {
		a := existing.Addr().String()
		t.mu.Unlock()
		_ = l.Close()
		return a, nil
	}
	t.listeners[destination] = l
	t.mu.Unlock()
	go t.accept(ctx, l, destination)
	return l.Addr().String(), nil
}
func (t *Tunnel) Status() Status {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return Status{Profile: t.profileID, State: t.state, LastError: t.lastErr, LastConnected: t.connected, ReconnectAttempts: t.attempts, Generation: t.generation}
}
func (t *Tunnel) set(state State, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state = state
	if err != nil {
		t.lastErr = sanitize(err)
	}
	if state == Connected {
		t.lastErr = ""
		t.connected = time.Now()
		t.generation++
	}
}
func (t *Tunnel) clientCopy() *ssh.Client { t.mu.RLock(); defer t.mu.RUnlock(); return t.client }
func (t *Tunnel) maintain(ctx context.Context) {
	defer close(t.done)
	delay := t.profile.Reconnect.InitialDelay.Value()
	for {
		if ctx.Err() != nil {
			return
		}
		t.set(Connecting, nil)
		c, err := t.connect(ctx)
		if err == nil {
			t.mu.Lock()
			t.client = c
			t.mu.Unlock()
			t.set(Connected, nil)
			err = t.keepalive(ctx, c)
			t.mu.Lock()
			if t.client == c {
				t.client = nil
			}
			t.mu.Unlock()
			_ = c.Close()
			if ctx.Err() != nil {
				return
			}
			t.set(Reconnecting, err)
		} else {
			t.set(Failed, err)
		}
		if !t.profile.Reconnect.Enabled {
			return
		}
		t.mu.Lock()
		t.attempts++
		t.mu.Unlock()
		wait := jitter(delay)
		t.logger.Warn("ssh tunnel reconnect scheduled", "profile", t.profileID, "delay", wait, "error", sanitize(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		delay *= 2
		if delay > t.profile.Reconnect.MaxDelay.Value() {
			delay = t.profile.Reconnect.MaxDelay.Value()
		}
	}
}
func (t *Tunnel) connect(ctx context.Context) (*ssh.Client, error) {
	key, err := os.ReadFile(t.profile.PrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	var signer ssh.Signer
	if t.profile.PrivateKeyPassphraseFile != "" {
		pass, e := config.ReadSecret(t.profile.PrivateKeyPassphraseFile)
		if e != nil {
			return nil, fmt.Errorf("read private-key passphrase: %w", e)
		}
		signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(pass))
	} else {
		signer, err = ssh.ParsePrivateKey(key)
	}
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	cb, err := knownhosts.New(t.profile.KnownHostsFile)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts: %w", err)
	}
	sc := &ssh.ClientConfig{User: t.profile.Username, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: cb, Timeout: 10 * time.Second}
	d := net.Dialer{Timeout: 10 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", net.JoinHostPort(t.profile.Host, fmt.Sprint(t.profile.Port)))
	if err != nil {
		return nil, fmt.Errorf("dial bastion: %w", err)
	}
	cc, ch, req, err := ssh.NewClientConn(raw, net.JoinHostPort(t.profile.Host, fmt.Sprint(t.profile.Port)), sc)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}
	return ssh.NewClient(cc, ch, req), nil
}
func (t *Tunnel) keepalive(ctx context.Context, c *ssh.Client) error {
	misses := 0
	tick := time.NewTicker(t.profile.KeepaliveInterval.Value())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
			if err != nil {
				misses++
				if misses >= t.profile.KeepaliveFailures {
					return fmt.Errorf("ssh keepalive failed")
				}
			} else {
				misses = 0
			}
		}
	}
}
func (t *Tunnel) accept(ctx context.Context, listener net.Listener, destination string) {
	for {
		c, err := listener.Accept()
		if err != nil {
			return
		}
		go t.forward(ctx, c, destination)
	}
}
func (t *Tunnel) forward(ctx context.Context, local net.Conn, destination string) {
	defer local.Close()
	c := t.clientCopy()
	if c == nil {
		return
	}
	remote, err := c.Dial("tcp", destination)
	if err != nil {
		return
	}
	defer remote.Close()
	go func() { _, _ = io.Copy(remote, local) }()
	_, _ = io.Copy(local, remote)
}
func (t *Tunnel) Close() error {
	if t.cancel != nil {
		t.cancel()
	}
	t.mu.Lock()
	listeners := make([]net.Listener, 0, len(t.listeners))
	for _, l := range t.listeners {
		listeners = append(listeners, l)
	}
	t.mu.Unlock()
	for _, l := range listeners {
		_ = l.Close()
	}
	select {
	case <-t.done:
	case <-time.After(5 * time.Second):
	}
	c := t.clientCopy()
	if c != nil {
		return c.Close()
	}
	return nil
}
func jitter(d time.Duration) time.Duration {
	n, err := rand.Int(rand.Reader, big.NewInt(4001))
	if err != nil {
		return d
	}
	return d*8/10 + time.Duration(n.Int64())*d/10000
}
func sanitize(err error) string {
	if err == nil {
		return ""
	}
	// The detailed cause can contain filenames or network details. MCP status is
	// intentionally coarse; operators diagnose from their local configuration.
	return "SSH transport error"
} // Errors never include key/password bytes; callers must not log config values.
