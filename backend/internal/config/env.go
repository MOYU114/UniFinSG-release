package config

import (
	"fmt"
	"os"
	"strconv"
)

// ApplyRuntimeEnvironment overrides connection settings from environment
// variables. It is shared by the API server and the command-line tools so an
// env-only deployment (Compose, ECS) works the same everywhere.
//
// Apply individual fields after JSON decoding: secrets containing quotes or
// backslashes must never be interpolated into JSON text.
func ApplyRuntimeEnvironment(cfg *Config) error {
	if cfg.Databases == nil {
		cfg.Databases = make(map[string]DatabaseConfig)
	}
	db := cfg.Databases["mysql"]
	for name, destination := range map[string]*string{
		"MYSQL_HOST": &db.Host, "MYSQL_USER": &db.Username,
		"MYSQL_UNICHATGO_PASSWD": &db.Password, "MYSQL_DATABASE": &db.DBName,
		"MYSQL_PARAMS": &db.Params, "REDIS_HOST": &cfg.Redis.Host,
		"REDIS_USER": &cfg.Redis.Username, "REDIS_PASSWD": &cfg.Redis.Password,
		"SERVER_ADDRESS": &cfg.BasicConfig.ServerAddress,
	} {
		if value, present := os.LookupEnv(name); present {
			*destination = value
		}
	}
	for _, field := range []struct {
		name     string
		value    *int
		min, max int
	}{
		{"MYSQL_PORT", &db.Port, 1, 65535},
		{"REDIS_PORT", &cfg.Redis.Port, 1, 65535},
		{"REDIS_DB", &cfg.Redis.DB, 0, 15},
	} {
		if raw, present := os.LookupEnv(field.name); present {
			value, err := strconv.Atoi(raw)
			if err != nil || value < field.min || value > field.max {
				return fmt.Errorf("%s must be an integer between %d and %d", field.name, field.min, field.max)
			}
			*field.value = value
		}
	}
	cfg.Databases["mysql"] = db
	if raw, present := os.LookupEnv("REDIS_TLS"); present {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("REDIS_TLS must be a boolean")
		}
		cfg.Redis.TLS = enabled
	}
	return nil
}
