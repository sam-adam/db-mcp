package sqlpolicy

import "testing"

func TestAllowsOnlyConservativeReads(t *testing.T) {
	for _, q := range []string{"SELECT id FROM accounts", "SELECT COUNT(*) FROM accounts", "SHOW TABLES", "EXPLAIN SELECT id FROM accounts"} {
		if err := Validate(q); err != nil {
			t.Fatalf("rejected %q", q)
		}
	}
	for _, q := range []string{"WITH x AS (DELETE FROM t RETURNING *) SELECT * FROM x", "SELECT 1; DELETE FROM t", "EXPLAIN ANALYZE SELECT 1", "SELECT pg_sleep(1)", "SELECT * FROM t FOR UPDATE", "SELECT 1 INTO OUTFILE 'x'", "CALL p()"} {
		if Validate(q) == nil {
			t.Fatalf("accepted %q", q)
		}
	}
}
