// Package webui 提供无需运行时外部文件的管理界面资源。
package webui

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed assets/*
var assets embed.FS

// Handler 返回管理界面的静态资源处理器。API 路由应在外层 mux 中注册。
func Handler() http.Handler {
	files, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
		if name == "." || name == "" {
			name = "index.html"
		} else if strings.HasPrefix(name, "assets/") {
			name = strings.TrimPrefix(name, "assets/")
		} else {
			name = "index.html"
		}

		content, err := fs.ReadFile(files, name)
		if err != nil {
			http.NotFound(response, request)
			return
		}

		response.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		response.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if name == "index.html" {
			response.Header().Set("Cache-Control", "no-store")
		} else {
			response.Header().Set("Cache-Control", "public, max-age=3600")
		}
		if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
			response.Header().Set("Content-Type", contentType)
		} else if strings.HasSuffix(name, ".js") {
			response.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		} else if strings.HasSuffix(name, ".css") {
			response.Header().Set("Content-Type", "text/css; charset=utf-8")
		}
		_, _ = response.Write(content)
	})
}
