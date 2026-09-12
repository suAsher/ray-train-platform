package api

import "github.com/gin-gonic/gin"

func (h *Handler) RegisterMLflowIntegrationRoutes(v1 *gin.RouterGroup) {
	if h.mlflowIntegrations != nil {
		h.mlflowIntegrations.RegisterRoutes(v1)
	}
}
