package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/18345174/echoear_cloud/internal/database"
)

func ok(c *gin.Context, message string, data any) {
	response := gin.H{"code": 0, "message": message}
	if data != nil {
		response["data"] = data
	}
	c.JSON(http.StatusOK, response)
}

func fail(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"code": status, "message": message})
}

func currentSession(c *gin.Context) *database.Session {
	value, _ := c.Get("session")
	session, _ := value.(*database.Session)
	return session
}

func currentUserID(c *gin.Context) int64 {
	if session := currentSession(c); session != nil {
		return session.UserID
	}
	return 0
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func heartbeatRequest(c *gin.Context) (database.Heartbeat, bool) {
	var request struct {
		LastIP    string `json:"last_ip"`
		Hostname  string `json:"hostname"`
		FWVersion string `json:"fw_version"`
	}
	if err := c.ShouldBindJSON(&request); err != nil && c.Request.ContentLength != 0 {
		fail(c, http.StatusBadRequest, "请求参数错误")
		return database.Heartbeat{}, false
	}
	return database.Heartbeat{LastIP: request.LastIP, Hostname: request.Hostname, FWVersion: request.FWVersion}, true
}
