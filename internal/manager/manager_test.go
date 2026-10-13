package manager

import "testing"

func TestReadOnly(t *testing.T) {
	for _, q := range []string{"select 1", " WITH x as (select 1) select * from x", "SHOW tables", "EXPLAIN SELECT 1"} {
		if !ReadOnly(q) {
			t.Fatalf("rejected %q", q)
		}
	}
	for _, q := range []string{"DELETE FROM x", "SELECT 1; DELETE FROM x", "SELECT * FROM x FOR UPDATE", "SELECT 1 INTO OUTFILE 'x'"} {
		if ReadOnly(q) {
			t.Fatalf("accepted %q", q)
		}
	}
}
