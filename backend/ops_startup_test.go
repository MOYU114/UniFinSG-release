package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestReadinessTracksBothDependenciesAndLivenessSurvives(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, databaseDown := range []bool{false, true} {
		for _, cacheDown := range []bool{false, true} {
			router := gin.New()
			probe := func(down bool) func(context.Context) error {
				return func(ctx context.Context) error {
					if _, ok := ctx.Deadline(); !ok {
						t.Error("missing probe deadline")
					}
					if down {
						return errors.New("sensitive connection detail")
					}
					return nil
				}
			}
			registerHealthRoutes(router, probe(databaseDown), probe(cacheDown))
			for _, path := range []string{"/healthz", "/readyz", "/api/readyz"} {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
				want := 200
				if path != "/healthz" && (databaseDown || cacheDown) {
					want = 503
				}
				if response.Code != want || strings.Contains(response.Body.String(), "sensitive") {
					t.Fatalf("%s: status=%d body=%s", path, response.Code, response.Body)
				}
			}
		}
	}
}
