package config

import "testing"

func TestRejectsUnsafeConfigurations(t *testing.T) {
	c := &Config{Version: 1, Connections: []Connection{{ID: "db", Driver: "mysql", Host: "x", Port: 3306, Database: "x", Username: "u", PasswordFile: "x", Readonly: boolPtr(false)}}}
	if err := c.Validate("."); err == nil {
		t.Fatal("expected readonly rejection")
	}
}
func boolPtr(v bool) *bool { return &v }
