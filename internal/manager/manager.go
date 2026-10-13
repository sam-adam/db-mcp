package manager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/sam-adam/db-mcp/internal/config"
	"github.com/sam-adam/db-mcp/internal/tunnel"
)

var ErrUnsafeSQL = errors.New("only read-only SELECT, WITH, SHOW, and EXPLAIN queries are permitted")
var ErrUncertain = errors.New("database connection was lost; query outcome is uncertain and was not replayed")

type ConnectionStatus struct {
	ID                        string       `json:"id"`
	Driver                    string       `json:"driver"`
	Mode                      string       `json:"mode"`
	SSHProfile                string       `json:"ssh_profile,omitempty"`
	TunnelState               tunnel.State `json:"tunnel_state,omitempty"`
	Reachable                 bool         `json:"reachable"`
	ServerVersion             string       `json:"server_version,omitempty"`
	LastSuccessfulHealthCheck time.Time    `json:"last_successful_health_check,omitempty"`
	Error                     string       `json:"error,omitempty"`
}
type dbConnection struct {
	cfg              config.Connection
	db               *sql.DB
	t                *tunnel.Tunnel
	generation       uint64
	mu               sync.Mutex
	lastOK           time.Time
	version, lastErr string
}
type Manager struct {
	cfg     *config.Config
	logger  *slog.Logger
	tunnels map[string]*tunnel.Tunnel
	dbs     map[string]*dbConnection
	ctx     context.Context
	cancel  context.CancelFunc
}

func New(cfg *config.Config, logger *slog.Logger) (*Manager, error) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{cfg: cfg, logger: logger, tunnels: map[string]*tunnel.Tunnel{}, dbs: map[string]*dbConnection{}, ctx: ctx, cancel: cancel}
	for _, c := range cfg.Connections {
		var t *tunnel.Tunnel
		if c.SSHProfile != "" {
			key := c.SSHProfile + "|" + c.Endpoint()
			t = m.tunnels[key]
			if t == nil {
				t = tunnel.New(c.SSHProfile, cfg.SSHProfiles[c.SSHProfile], c.Endpoint(), logger)
				if err := t.Start(ctx); err != nil {
					m.Close()
					return nil, err
				}
				m.tunnels[key] = t
			}
		}
		m.dbs[c.ID] = &dbConnection{cfg: c, t: t}
	}
	return m, nil
}
func (m *Manager) Close() error {
	m.cancel()
	for _, d := range m.dbs {
		d.mu.Lock()
		if d.db != nil {
			_ = d.db.Close()
		}
		d.mu.Unlock()
	}
	for _, t := range m.tunnels {
		_ = t.Close()
	}
	return nil
}
func (m *Manager) database(d *dbConnection) (*sql.DB, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var gen uint64
	if d.t != nil {
		st := d.t.Status()
		gen = st.Generation
		if st.State != tunnel.Connected {
			return nil, fmt.Errorf("SSH tunnel unavailable: %s", st.State)
		}
	}
	if d.db != nil && gen == d.generation {
		return d.db, nil
	}
	if d.db != nil {
		_ = d.db.Close()
		d.db = nil
	}
	secret, err := config.ReadSecret(d.cfg.PasswordFile)
	if err != nil {
		return nil, fmt.Errorf("database credential unavailable")
	}
	host, port := d.cfg.Host, d.cfg.Port
	if d.t != nil {
		h, p, e := net.SplitHostPort(d.t.Address())
		if e != nil {
			return nil, e
		}
		host = h
		port, _ = strconv.Atoi(p)
	}
	dsn := buildDSN(d.cfg, host, port, secret)
	db, err := sql.Open(d.cfg.Driver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	d.db = db
	d.generation = gen
	return db, nil
}
func buildDSN(c config.Connection, host string, port int, password string) string {
	if c.Driver == "postgres" {
		return fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=disable", host, port, c.Database, c.Username, password)
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?timeout=10s&readTimeout=30s&writeTimeout=30s", c.Username, password, host, port, c.Database)
}
func (m *Manager) Query(ctx context.Context, id, q string) ([]string, [][]any, error) {
	if !ReadOnly(q) {
		return nil, nil, ErrUnsafeSQL
	}
	d := m.dbs[id]
	if d == nil {
		return nil, nil, fmt.Errorf("unknown connection %q", id)
	}
	db, err := m.database(d)
	if err != nil {
		return nil, nil, err
	}
	timeout := d.cfg.QueryTimeout.Value()
	if timeout == 0 {
		timeout = m.cfg.Defaults.QueryTimeout.Value()
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		if d.t != nil && d.t.Status().State != tunnel.Connected {
			return nil, nil, ErrUncertain
		}
		return nil, nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	max := d.cfg.MaxRows
	if max == 0 {
		max = m.cfg.Defaults.MaxRows
	}
	if max == 0 {
		max = 500
	}
	out := make([][]any, 0)
	for rows.Next() {
		if len(out) >= max {
			break
		}
		values := make([]any, len(cols))
		ptr := make([]any, len(cols))
		for i := range values {
			ptr[i] = &values[i]
		}
		if err := rows.Scan(ptr...); err != nil {
			return nil, nil, err
		}
		out = append(out, values)
	}
	if err := rows.Err(); err != nil {
		if d.t != nil && d.t.Status().State != tunnel.Connected {
			return nil, nil, ErrUncertain
		}
		return nil, nil, err
	}
	return cols, out, nil
}
func (m *Manager) Status(ctx context.Context) []ConnectionStatus {
	out := make([]ConnectionStatus, 0, len(m.dbs))
	for _, d := range m.dbs {
		s := ConnectionStatus{ID: d.cfg.ID, Driver: d.cfg.Driver, Mode: "direct"}
		if d.t != nil {
			ts := d.t.Status()
			s.Mode = "ssh"
			s.SSHProfile = d.cfg.SSHProfile
			s.TunnelState = ts.State
			s.Error = ts.LastError
		}
		db, err := m.database(d)
		if err == nil {
			qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			var version string
			err = db.QueryRowContext(qctx, "SELECT version()").Scan(&version)
			cancel()
			if err == nil {
				s.Reachable = true
				s.ServerVersion = version
				d.mu.Lock()
				d.lastOK = time.Now()
				d.version = version
				d.lastErr = ""
				d.mu.Unlock()
			} else {
				s.Error = "database health check failed"
			}
		} else if s.Error == "" {
			s.Error = "database unavailable"
		}
		d.mu.Lock()
		s.LastSuccessfulHealthCheck = d.lastOK
		d.mu.Unlock()
		out = append(out, s)
	}
	return out
}
func (m *Manager) TunnelStatus() []tunnel.Status {
	out := make([]tunnel.Status, 0, len(m.tunnels))
	for _, t := range m.tunnels {
		out = append(out, t.Status())
	}
	return out
}
func ReadOnly(q string) bool {
	s := strings.TrimSpace(strings.TrimPrefix(q, "\ufeff"))
	for strings.HasPrefix(s, "--") {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			return false
		}
		s = strings.TrimSpace(s[i+1:])
	}
	u := strings.ToUpper(s)
	if strings.Contains(u, ";") || strings.Contains(u, "/*") || strings.Contains(u, "INTO OUTFILE") || strings.Contains(u, "FOR UPDATE") {
		return false
	}
	return strings.HasPrefix(u, "SELECT ") || strings.HasPrefix(u, "WITH ") || strings.HasPrefix(u, "SHOW ") || strings.HasPrefix(u, "EXPLAIN ")
}
