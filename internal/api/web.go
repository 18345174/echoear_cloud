package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

func connectSecurityHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Frame-Options", "DENY")
}

func (s *Server) connectPage(c *gin.Context) {
	connectSecurityHeaders(c)
	c.Data(http.StatusOK, "text/html; charset=utf-8", connectHTML)
}

func (s *Server) connectStyles(c *gin.Context) {
	connectSecurityHeaders(c)
	c.Data(http.StatusOK, "text/css; charset=utf-8", connectCSS)
}

func (s *Server) connectScript(c *gin.Context) {
	connectSecurityHeaders(c)
	c.Data(http.StatusOK, "text/javascript; charset=utf-8", connectJS)
}

func (s *Server) hapiWeb(c *gin.Context) {
	root := strings.TrimSpace(s.config.HapiWebDir)
	if root == "" {
		c.String(http.StatusServiceUnavailable, "HAPI Web is not installed")
		return
	}

	requested := strings.TrimPrefix(c.Param("path"), "/")
	clean := filepath.Clean(filepath.FromSlash(requested))
	if clean == "." {
		clean = "index.html"
	}
	candidate := filepath.Join(root, clean)
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		c.Status(http.StatusNotFound)
		return
	}

	info, statErr := os.Stat(candidate)
	if statErr == nil && info.IsDir() {
		candidate = filepath.Join(candidate, "index.html")
		info, statErr = os.Stat(candidate)
	}
	if statErr != nil {
		if filepath.Ext(clean) != "" {
			c.Status(http.StatusNotFound)
			return
		}
		candidate = filepath.Join(root, "index.html")
		info, statErr = os.Stat(candidate)
	}
	if statErr != nil || info.IsDir() {
		c.String(http.StatusServiceUnavailable, "HAPI Web is not installed")
		return
	}

	if filepath.Base(candidate) == "index.html" || filepath.Base(candidate) == "sw.js" {
		c.Header("Cache-Control", "no-cache")
	} else {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	}
	c.File(candidate)
}
