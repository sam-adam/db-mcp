package manager

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/sam-adam/db-mcp/internal/config"
	"github.com/sam-adam/db-mcp/internal/sqlpolicy"
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
type ConnectionInfo struct {
	ID         string `json:"id"`
	Driver     string `json:"driver"`
	Mode       string `json:"mode"`
	SSHProfile string `json:"ssh_profile,omitempty"`
}
type Capabilities struct {
	ConnectionID    string `json:"connection_id"`
	Driver          string `json:"driver"`
	ReadOnly        bool   `json:"read_only"`
	SupportsSchemas bool   `json:"supports_schemas"`
	SupportsExplain bool   `json:"supports_explain"`
}
type dbConnection struct {
	cfg              config.Connection
	db               *sql.DB
	t                *tunnel.Tunnel
	forwardAddress   string
	generation       uint64
	mu               sync.Mutex
	lastOK           time.Time
	version, lastErr string
	querySlots       chan struct{}
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
		var forwardAddress string
		if c.SSHProfile != "" {
			key := c.SSHProfile
			t = m.tunnels[key]
			if t == nil {
				t = tunnel.New(c.SSHProfile, cfg.SSHProfiles[c.SSHProfile], logger)
				if err := t.Start(ctx); err != nil {
					m.Close()
					return nil, err
				}
				m.tunnels[key] = t
			}
			var err error
			forwardAddress, err = t.AddDestination(ctx, c.Endpoint())
			if err != nil {
				m.Close()
				return nil, err
			}
		}
		limit := c.MaxConcurrentQueries
		if limit == 0 {
			limit = cfg.Defaults.MaxConcurrentQueries
		}
		if limit == 0 {
			limit = 4
		}
		m.dbs[c.ID] = &dbConnection{cfg: c, t: t, forwardAddress: forwardAddress, querySlots: make(chan struct{}, limit)}
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
	secret, err := d.cfg.Password()
	if err != nil {
		return nil, fmt.Errorf("database credential unavailable")
	}
	host, port := d.cfg.Host, d.cfg.Port
	if d.t != nil {
		h, p, e := net.SplitHostPort(d.forwardAddress)
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
	if err := sqlpolicy.Validate(q); err != nil {
		return nil, nil, ErrUnsafeSQL
	}
	d := m.dbs[id]
	if d == nil {
		return nil, nil, fmt.Errorf("unknown connection %q", id)
	}
	select {
	case d.querySlots <- struct{}{}:
		defer func() { <-d.querySlots }()
	case <-ctx.Done():
		return nil, nil, fmt.Errorf("query admission timed out")
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
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, q)
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
	maxBytes := d.cfg.MaxResultBytes
	if maxBytes == 0 {
		maxBytes = m.cfg.Defaults.MaxResultBytes
	}
	if maxBytes == 0 {
		maxBytes = 4 << 20
	}
	used := 0
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
		encoded, _ := json.Marshal(values)
		used += len(encoded)
		if used > maxBytes {
			return nil, nil, fmt.Errorf("result exceeds configured byte limit")
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
func (m *Manager) ListConnections() []ConnectionInfo {
	out := make([]ConnectionInfo, 0, len(m.dbs))
	for _, d := range m.dbs {
		mode := "direct"
		if d.t != nil {
			mode = "ssh"
		}
		out = append(out, ConnectionInfo{ID: d.cfg.ID, Driver: d.cfg.Driver, Mode: mode, SSHProfile: d.cfg.SSHProfile})
	}
	return out
}
func (m *Manager) ConnectionCapabilities(id string) (Capabilities, error) {
	d := m.dbs[id]
	if d == nil {
		return Capabilities{}, fmt.Errorf("unknown connection %q", id)
	}
	return Capabilities{ConnectionID: id, Driver: d.cfg.Driver, ReadOnly: true, SupportsSchemas: true, SupportsExplain: true}, nil
}

// Catalog executes only fixed, parameterized metadata queries through the same
// connection, admission control, timeout, and read-only transaction path.
func (m *Manager) Catalog(ctx context.Context, id, kind, schema, table string) ([]map[string]any, error) {
	d := m.dbs[id]
	if d == nil {
		return nil, fmt.Errorf("unknown connection %q", id)
	}
	q, args, err := catalogQuery(d.cfg.Driver, kind, schema, table)
	if err != nil {
		return nil, err
	}
	select {
	case d.querySlots <- struct{}{}:
		defer func() { <-d.querySlots }()
	case <-ctx.Done():
		return nil, fmt.Errorf("query admission timed out")
	}
	db, err := m.database(d)
	if err != nil {
		return nil, err
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
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		if len(out) >= 500 {
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, col := range cols {
			row[col] = vals[i]
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
func catalogQuery(driver, kind, schema, table string) (string, []any, error) {
	if schema == "" {
		schema = "public"
	}
	if driver == "mysql" && schema == "public" {
		schema = ""
	}
	if driver == "postgres" {
		switch kind {
		case "databases":
			return "SELECT datname AS database FROM pg_database WHERE datistemplate = false ORDER BY datname", nil, nil
		case "schemas":
			return "SELECT schema_name FROM information_schema.schemata ORDER BY schema_name", nil, nil
		case "tables":
			return "SELECT table_schema, table_name, table_type FROM information_schema.tables WHERE table_schema = $1 ORDER BY table_name", []any{schema}, nil
		case "columns", "describe_table":
			return "SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 ORDER BY ordinal_position", []any{schema, table}, nil
		case "indexes":
			return "SELECT indexname, indexdef FROM pg_indexes WHERE schemaname = $1 AND tablename = $2 ORDER BY indexname", []any{schema, table}, nil
		case "foreign_keys":
			return "SELECT constraint_name, column_name, foreign_table_name, foreign_column_name FROM information_schema.key_column_usage WHERE table_schema = $1 AND table_name = $2 AND position_in_unique_constraint IS NOT NULL", []any{schema, table}, nil
		}
	}
	if driver == "mysql" {
		switch kind {
		case "databases":
			return "SHOW DATABASES", nil, nil
		case "schemas":
			return "SELECT schema_name FROM information_schema.schemata ORDER BY schema_name", nil, nil
		case "tables":
			return "SELECT table_schema, table_name, table_type FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_name", nil, nil
		case "columns", "describe_table":
			return "SELECT column_name, column_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position", []any{table}, nil
		case "indexes":
			return "SELECT index_name, column_name, non_unique FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? ORDER BY index_name, seq_in_index", []any{table}, nil
		case "foreign_keys":
			return "SELECT constraint_name, column_name, referenced_table_name, referenced_column_name FROM information_schema.key_column_usage WHERE table_schema = DATABASE() AND table_name = ? AND referenced_table_name IS NOT NULL", []any{table}, nil
		}
	}
	return "", nil, fmt.Errorf("unsupported metadata operation")
}
func ReadOnly(q string) bool { return sqlpolicy.Validate(q) == nil }
