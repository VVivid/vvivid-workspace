// Package config validates application settings before a listener is opened.
package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"agentshoppinglist/internal/identity"
)

type Config struct {
	HTTPAddr            string
	ShutdownTimeout     time.Duration
	DatabasePath        string
	DatabaseBusyTimeout time.Duration
	Identity            *identity.Config
}

// Load validates listener, persistence and the optional complete identity bundle
// before opening any listener.
func Load(lookupEnv func(string) (string, bool)) (Config, error) {
	cfg := Config{HTTPAddr: "127.0.0.1:8090", ShutdownTimeout: 10 * time.Second, DatabasePath: "data/shopping.sqlite", DatabaseBusyTimeout: 2 * time.Second}
	if value, ok := lookupEnv("SHOPPING_HTTP_ADDR"); ok {
		cfg.HTTPAddr = value
	}
	host, port, err := net.SplitHostPort(cfg.HTTPAddr)
	if err != nil || (host != "" && host != "localhost" && net.ParseIP(host) == nil) {
		return Config{}, fmt.Errorf("SHOPPING_HTTP_ADDR must be an IP address or localhost followed by a port, for example 127.0.0.1:8090")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return Config{}, fmt.Errorf("SHOPPING_HTTP_ADDR port must be between 1 and 65535")
	}
	if value, ok := lookupEnv("SHOPPING_SHUTDOWN_TIMEOUT"); ok {
		cfg.ShutdownTimeout, err = time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("SHOPPING_SHUTDOWN_TIMEOUT must be a duration, for example 10s")
		}
	}
	if cfg.ShutdownTimeout <= 0 || cfg.ShutdownTimeout > time.Minute {
		return Config{}, fmt.Errorf("SHOPPING_SHUTDOWN_TIMEOUT must be greater than zero and at most 1m")
	}
	if value, ok := lookupEnv("SHOPPING_DB_PATH"); ok {
		cfg.DatabasePath = value
	}
	if cfg.DatabasePath == "" || cfg.DatabasePath == ":memory:" || strings.ContainsRune(cfg.DatabasePath, '\x00') {
		return Config{}, fmt.Errorf("SHOPPING_DB_PATH must be a persistent file path")
	}
	if value, ok := lookupEnv("SHOPPING_DB_BUSY_TIMEOUT"); ok {
		cfg.DatabaseBusyTimeout, err = time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("SHOPPING_DB_BUSY_TIMEOUT must be a duration, for example 2s")
		}
	}
	if cfg.DatabaseBusyTimeout < time.Millisecond || cfg.DatabaseBusyTimeout > 10*time.Second {
		return Config{}, fmt.Errorf("SHOPPING_DB_BUSY_TIMEOUT must be between 1ms and 10s")
	}
	cfg.Identity, err = identity.LoadConfig(lookupEnv)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}
