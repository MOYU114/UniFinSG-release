package redis

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"unichatgo/internal/config"
)

func TestRedisTLSRejectsUntrustedCertificate(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("Redis must reject the certificate before sending application data")
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	host, rawPort, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(rawPort)
	client, err := NewRedisClient(&config.Config{Redis: config.RedisConfig{Host: host, Port: port, TLS: true}})
	if client != nil {
		client.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected verified TLS to reject an untrusted certificate, got %v", err)
	}
}
