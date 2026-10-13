package manager

import "testing"

func TestReadOnly(t *testing.T) {
	for _, q := range []string{"select 1", "SHOW tables", "EXPLAIN SELECT 1"} {
		if !ReadOnly(q) {
			t.Fatalf("rejected %q", q)
		}
	}
	for _, q := range []string{"DELETE FROM x", "WITH x AS (DELETE FROM x RETURNING *) SELECT * FROM x", "SELECT 1; DELETE FROM x", "SELECT * FROM x FOR UPDATE", "SELECT pg_sleep(1)", "EXPLAIN ANALYZE SELECT 1", "SELECT 1 INTO OUTFILE 'x'"} {
		if ReadOnly(q) {
			t.Fatalf("accepted %q", q)
		}
	}
}
