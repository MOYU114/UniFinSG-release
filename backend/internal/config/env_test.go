package config

import (
	"strings"
	"testing"
)

func TestRuntimeEnvironmentPreservesSecretBytes(t *testing.T) {
	secret := "quote\"slash\\dollar${UNCHANGED}\n"
	t.Setenv("MYSQL_UNICHATGO_PASSWD", secret)
	t.Setenv("REDIS_PASSWD", "")
	t.Setenv("MYSQL_PORT", "3307")
	cfg := &Config{Databases: map[string]DatabaseConfig{"mysql": {Host: "from-file", Password: "old"}}, Redis: RedisConfig{Password: "old"}}
	if err := ApplyRuntimeEnvironment(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Databases["mysql"].Password != secret || cfg.Databases["mysql"].Host != "from-file" || cfg.Databases["mysql"].Port != 3307 || cfg.Redis.Password != "" {
		t.Fatal("environment precedence or secret preservation failed")
	}
}

func TestRuntimeEnvironmentRejectsInvalidPortWithoutEchoingValue(t *testing.T) {
	t.Setenv("MYSQL_PORT", "secret-invalid-value")
	err := ApplyRuntimeEnvironment(&Config{})
	if err == nil || strings.Contains(err.Error(), "secret-invalid-value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRuntimeEnvironmentRedisTLS(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "cloud", false: "local"}[enabled], func(t *testing.T) {
			t.Setenv("REDIS_TLS", map[bool]string{true: "true", false: "false"}[enabled])
			cfg := &Config{Redis: RedisConfig{TLS: !enabled}}
			if err := ApplyRuntimeEnvironment(cfg); err != nil {
				t.Fatal(err)
			}
			if cfg.Redis.TLS != enabled {
				t.Fatal("Redis TLS environment override was not applied")
			}
		})
	}
	t.Setenv("REDIS_TLS", "invalid-secret-value")
	if err := ApplyRuntimeEnvironment(&Config{}); err == nil || strings.Contains(err.Error(), "invalid-secret-value") {
		t.Fatalf("unexpected TLS validation error: %v", err)
	}
}
