package handler

import (
	"net/http"

	"infinite-canvas/backend/internal/app"
	"infinite-canvas/backend/internal/runtimelog"

	"github.com/gin-gonic/gin"
)

func RegisterRuntimeLogRoutes(api *gin.RouterGroup, logger *runtimelog.Logger) {
	if api == nil || logger == nil {
		return
	}
	api.POST("/runtime-logs/frontend", func(c *gin.Context) {
		if !requireTrustedDesktopWritePrincipal(c, "运行日志仅接受受信任桌面界面写入") {
			c.Abort()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10)
		var request struct {
			Entries []runtimelog.Entry `json:"entries"`
		}
		if err := c.ShouldBindJSON(&request); err != nil || len(request.Entries) == 0 || len(request.Entries) > 100 {
			failService(c, app.BadAuthRequest("日志批次无效"))
			return
		}
		if err := logger.Append("frontend", request.Entries); err != nil {
			failInternal(c, http.StatusInternalServerError, err)
			return
		}
		ok(c, gin.H{"accepted": len(request.Entries)})
	})
}
