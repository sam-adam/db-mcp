package config

import (
	"os"
	"testing"
)

func TestRejectsUnsafeConfigurations(t *testing.T) {
	c := &Config{Version: 1, Connections: []Connection{{ID: "db", Driver: "mysql", Host: "x", Port: 3306, Database: "x", Username: "u", PasswordFile: "x", Readonly: boolPtr(false)}}}
	if err := c.Validate("."); err == nil {
		t.Fatal("expected readonly rejection")
	}
}
func boolPtr(v bool) *bool { return &v }

func TestEnvironmentPasswordSource(t *testing.T) {
	t.Setenv("DB_MCP_TEST_PASSWORD", "secret")
	c := Connection{PasswordEnv: "DB_MCP_TEST_PASSWORD"}
	got, err := c.Password()
	if err != nil || got != "secret" {
		t.Fatalf("unexpected password result: %q, %v", got, err)
	}
	if err := os.Unsetenv("DB_MCP_TEST_PASSWORD"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Password(); err == nil {
		t.Fatal("expected unavailable environment error")
	}
}
