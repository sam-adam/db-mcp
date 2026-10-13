package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/sam-adam/db-mcp/internal/manager"
	"io"
	"log/slog"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type response struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func Serve(ctx context.Context, in io.Reader, out io.Writer, m *manager.Manager, logger *slog.Logger) error {
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), 4<<20)
	enc := json.NewEncoder(out)
	for scan.Scan() {
		var r request
		if err := json.Unmarshal(scan.Bytes(), &r); err != nil {
			_ = enc.Encode(response{JSONRPC: "2.0", Error: &rpcError{-32700, "invalid JSON"}})
			continue
		}
		res, err := handle(ctx, r, m)
		reply := response{JSONRPC: "2.0", ID: r.ID, Result: res}
		if err != nil {
			reply.Result = nil
			reply.Error = &rpcError{-32602, err.Error()}
		}
		if err := enc.Encode(reply); err != nil {
			return err
		}
	}
	return scan.Err()
}
func handle(ctx context.Context, r request, m *manager.Manager) (any, error) {
	switch r.Method {
	case "initialize":
		return map[string]any{"protocolVersion": "2024-11-05", "serverInfo": map[string]string{"name": "readonly-db-mcp", "version": "0.1.0"}, "capabilities": map[string]any{"tools": map[string]any{}}}, nil
	case "tools/list":
		return map[string]any{"tools": tools()}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(r.Params, &p); err != nil {
			return nil, err
		}
		switch p.Name {
		case "db_connection_status":
			return content(m.Status(ctx)), nil
		case "db_list_connections":
			return content(m.ListConnections()), nil
		case "db_connection_capabilities":
			var a struct {
				ConnectionID string `json:"connection_id"`
			}
			if err := json.Unmarshal(p.Arguments, &a); err != nil {
				return nil, err
			}
			if a.ConnectionID == "" {
				return nil, fmt.Errorf("connection_id is required")
			}
			v, err := m.ConnectionCapabilities(a.ConnectionID)
			if err != nil {
				return nil, err
			}
			return content(v), nil
		case "db_tunnel_status":
			return content(m.TunnelStatus()), nil
		case "db_query":
			var a struct {
				ConnectionID string `json:"connection_id"`
				SQL          string `json:"sql"`
			}
			if err := json.Unmarshal(p.Arguments, &a); err != nil {
				return nil, err
			}
			cols, rows, err := m.Query(ctx, a.ConnectionID, a.SQL)
			if err != nil {
				return nil, err
			}
			return content(map[string]any{"connection_id": a.ConnectionID, "columns": cols, "rows": rows}), nil
		case "db_explain":
			var a struct {
				ConnectionID string `json:"connection_id"`
				SQL          string `json:"sql"`
			}
			if err := json.Unmarshal(p.Arguments, &a); err != nil {
				return nil, err
			}
			if a.ConnectionID == "" || a.SQL == "" {
				return nil, fmt.Errorf("connection_id and sql are required")
			}
			cols, rows, err := m.Query(ctx, a.ConnectionID, "EXPLAIN "+a.SQL)
			if err != nil {
				return nil, err
			}
			return content(map[string]any{"connection_id": a.ConnectionID, "columns": cols, "rows": rows}), nil
		case "db_list_databases", "db_list_schemas", "db_list_tables", "db_describe_table", "db_list_columns", "db_list_indexes", "db_list_foreign_keys":
			var a struct {
				ConnectionID string `json:"connection_id"`
				Schema       string `json:"schema"`
				Table        string `json:"table"`
			}
			if err := json.Unmarshal(p.Arguments, &a); err != nil {
				return nil, err
			}
			if a.ConnectionID == "" {
				return nil, fmt.Errorf("connection_id is required")
			}
			if (p.Name == "db_describe_table" || p.Name == "db_list_columns" || p.Name == "db_list_indexes" || p.Name == "db_list_foreign_keys") && a.Table == "" {
				return nil, fmt.Errorf("table is required")
			}
			kind := map[string]string{"db_list_databases": "databases", "db_list_schemas": "schemas", "db_list_tables": "tables", "db_describe_table": "describe_table", "db_list_columns": "columns", "db_list_indexes": "indexes", "db_list_foreign_keys": "foreign_keys"}[p.Name]
			rows, err := m.Catalog(ctx, a.ConnectionID, kind, a.Schema, a.Table)
			if err != nil {
				return nil, err
			}
			return content(map[string]any{"connection_id": a.ConnectionID, "rows": rows}), nil
		default:
			return nil, fmt.Errorf("unknown tool")
		}
	default:
		return nil, fmt.Errorf("unsupported method")
	}
}
func content(v any) map[string]any {
	b, _ := json.Marshal(v)
	return map[string]any{"content": []map[string]string{{"type": "text", "text": string(b)}}}
}
func tools() []map[string]any {
	connection := map[string]any{"type": "object", "properties": map[string]any{"connection_id": map[string]string{"type": "string"}}, "required": []string{"connection_id"}}
	query := map[string]any{"type": "object", "properties": map[string]any{"connection_id": map[string]string{"type": "string"}, "sql": map[string]string{"type": "string"}}, "required": []string{"connection_id", "sql"}}
	metadata := map[string]any{"type": "object", "properties": map[string]any{"connection_id": map[string]string{"type": "string"}, "schema": map[string]string{"type": "string"}, "table": map[string]string{"type": "string"}}, "required": []string{"connection_id"}}
	items := []map[string]any{{"name": "db_list_connections", "description": "List configured read-only connections.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}, {"name": "db_connection_status", "description": "Report connection health and tunnel state.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}, {"name": "db_tunnel_status", "description": "Report SSH tunnel states without secrets or destinations.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}, {"name": "db_connection_capabilities", "description": "Report safe capabilities for one configured connection.", "inputSchema": connection}, {"name": "db_query", "description": "Run one policy-validated read-only query against an explicit connection.", "inputSchema": query}, {"name": "db_explain", "description": "Explain a policy-validated read-only query; EXPLAIN ANALYZE is forbidden.", "inputSchema": query}}
	for _, name := range []string{"db_list_databases", "db_list_schemas", "db_list_tables", "db_describe_table", "db_list_columns", "db_list_indexes", "db_list_foreign_keys"} {
		items = append(items, map[string]any{"name": name, "description": "Read-only schema metadata for an explicit connection.", "inputSchema": metadata})
	}
	return items
}
