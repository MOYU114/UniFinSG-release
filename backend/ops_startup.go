package main

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func registerHealthRoutes(router *gin.Engine, database, cache func(context.Context) error) {
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"alive": true})
	})
	ready := func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if database(ctx) != nil || cache(ctx) != nil {
			// Do not expose connection strings or dependency errors publicly.
			c.JSON(http.StatusServiceUnavailable, gin.H{"ready": false})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ready": true})
	}
	router.GET("/readyz", ready)
	// CloudFront forwards /api/*; this endpoint supports post-deploy checks.
	router.GET("/api/readyz", ready)
}
