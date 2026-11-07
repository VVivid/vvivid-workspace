package service

import (
	"testing"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/identity"
)

func testConfig() Config {
	return Config{Listen: "127.0.0.1:8080", Database: "private.db", BackendOrigin: "http://memory.internal:8080", Identity: identity.Config{
		GatewayIssuer: "https://gateway.example", Audience: "https://gateway.example/memory/mcp", IdentityIssuer: "https://identity.example/realms/test",
		JWKSFile: "public.jwks", AllowedClients: []string{"agent-one", "agent-two"},
	}}
}

func TestFailClosedConfiguration(t *testing.T) {
	if _, err := FromEnv(func(string) string { return "" }); err == nil {
		t.Fatal("unconfigured service accepted")
	}
	for name, mutate := range map[string]func(*Config){
		"database":          func(c *Config) { c.Database = "" },
		"gateway":           func(c *Config) { c.Identity.GatewayIssuer = "" },
		"http gateway":      func(c *Config) { c.Identity.GatewayIssuer = "http://gateway.example" },
		"audience":          func(c *Config) { c.Identity.Audience = "" },
		"provider":          func(c *Config) { c.Identity.IdentityIssuer = "http://identity.example" },
		"keys":              func(c *Config) { c.Identity.JWKSFile = "" },
		"clients":           func(c *Config) { c.Identity.AllowedClients = nil },
		"duplicate client":  func(c *Config) { c.Identity.AllowedClients = []string{"agent-one", "agent-one"} },
		"path origin":       func(c *Config) { c.BackendOrigin += "/mcp" },
		"noncanonical host": func(c *Config) { c.BackendOrigin = "http://MEMORY.internal:8080" },
		"origin wildcard":   func(c *Config) { c.BackendOrigin = "http://*.example" },
		"empty port":        func(c *Config) { c.BackendOrigin = "http://memory.internal:" },
		"listen":            func(c *Config) { c.Listen = "localhost" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig()
			mutate(&cfg)
			if cfg.Validate() == nil {
				t.Fatal("partial or invalid configuration accepted")
			}
		})
	}
	for _, origin := range []string{"http://memory.internal:8080", "http://127.0.0.1:8080", "http://[::1]:8080", "https://memory.example"} {
		cfg := testConfig()
		cfg.Listen, cfg.BackendOrigin = ":8080", origin
		if cfg.Validate() != nil {
			t.Error("explicit private container listener or canonical origin rejected")
		}
	}
	env := map[string]string{"MEMORY_DATABASE": "private.db", "MEMORY_BACKEND_ORIGIN": "http://memory.internal:8080", "MEMORY_GATEWAY_ISSUER": "https://gateway.example", "MEMORY_AUDIENCE": "https://gateway.example/memory/mcp", "MEMORY_IDENTITY_ISSUER": "https://identity.example/realms/test", "MEMORY_GATEWAY_JWKS_FILE": "public.jwks", "MEMORY_ALLOWED_CLIENT_IDS": "agent-one,agent-two"}
	cfg, err := FromEnv(func(name string) string { return env[name] })
	if err != nil || cfg.Listen != "127.0.0.1:8080" {
		t.Fatal("local service did not default to loopback")
	}
}
