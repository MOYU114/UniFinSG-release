package finance

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/auth"
)

// RegisterDashboardRoutes exposes the same snapshot consumed by insight.Provider.
func (h *HTTPHandler) RegisterDashboardRoutes(api *gin.RouterGroup) {
	users := api.Group("/users/:id")
	users.Use(h.auth.APIMiddleware(), auth.RequirePathUser())
	users.GET("/dashboard", h.getDashboard)
}

func (h *HTTPHandler) getDashboard(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	snapshot, err := h.service.Snapshot(c.Request.Context(), userID, c.Query("month"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, snapshot)
}
