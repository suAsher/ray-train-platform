package api

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"ray-train-platform-backend/domain"
)

// CLICompatibilityGuard is a compatibility check, not authentication. Legacy
// unidentified clients and native Ray keep their existing protocol path.
func CLICompatibilityGuard(minimum string) gin.HandlerFunc {
	return func(c *gin.Context) {
		version := c.GetHeader("X-Spk-Rayjob-Version")
		path := c.Request.URL.Path
		submission := c.Request.Method == http.MethodPost && (path == "/api/v1/jobs" || path == "/api/v1/jobs/submit" || path == "/api/v1/jobs/preflight" || path == "/api/v1/source-artifacts")
		if minimum == "" || version == "" || !submission {
			c.Next()
			return
		}
		comparison, err := domain.CompareCLIReleaseVersions(version, minimum)
		if err != nil {
			c.AbortWithStatusJSON(400, gin.H{"success": false, "error": gin.H{"code": "CLI_VERSION_UNKNOWN", "message": "无法识别 spk-rayjob 版本，请使用平台下载页的正式客户端"}})
			return
		}
		if comparison < 0 {
			c.AbortWithStatusJSON(426, gin.H{"success": false, "error": gin.H{"code": "CLI_UPGRADE_REQUIRED", "message": "spk-rayjob 不再兼容，请执行 spk-rayjob upgrade；旧客户端没有此命令时请从外部提交页重新下载", "minimumVersion": minimum}})
			return
		}
		c.Next()
	}
}
