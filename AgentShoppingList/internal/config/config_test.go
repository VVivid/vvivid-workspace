package config

import "testing"

func TestLoadDefaultsAndInvalidSettings(t *testing.T) {
	cfg, err := Load(func(string) (string, bool) { return "", false })
	if err != nil || cfg.HTTPAddr != "127.0.0.1:8090" {
		t.Fatalf("default configuration: %#v, %v", cfg, err)
	}
	for _, settings := range []map[string]string{
		{"SHOPPING_HTTP_ADDR": ""},
		{"SHOPPING_HTTP_ADDR": "https://localhost:8090"},
		{"SHOPPING_HTTP_ADDR": "127.0.0.1:0"},
		{"SHOPPING_HTTP_ADDR": "127.0.0.1:65536"},
		{"SHOPPING_SHUTDOWN_TIMEOUT": "soon"},
		{"SHOPPING_SHUTDOWN_TIMEOUT": "0s"},
		{"SHOPPING_SHUTDOWN_TIMEOUT": "2m"},
		{"SHOPPING_DB_PATH": ""},
		{"SHOPPING_DB_PATH": ":memory:"},
		{"SHOPPING_DB_PATH": "bad\x00path"},
		{"SHOPPING_DB_BUSY_TIMEOUT": "soon"},
		{"SHOPPING_DB_BUSY_TIMEOUT": "0s"},
		{"SHOPPING_DB_BUSY_TIMEOUT": "2us"},
		{"SHOPPING_DB_BUSY_TIMEOUT": "11s"},
	} {
		t.Run("reject invalid setting", func(t *testing.T) {
			_, err := Load(func(key string) (string, bool) { value, ok := settings[key]; return value, ok })
			if err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
