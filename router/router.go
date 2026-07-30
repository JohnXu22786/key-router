package router

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"local-router/db"
	"local-router/handler"
	"local-router/health"
	"local-router/middleware"
	"local-router/model"
	"local-router/selector"

	"github.com/gin-gonic/gin"
)

// Setup configures all routes and returns the gin engine
func Setup(
	staticFS embed.FS,
	engine *selector.Engine,
	checker *health.Checker,
) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	// Auth middleware
	authToken := db.GetSetting(model.SettingAuthToken)
	log.Printf("[router] auth token from DB: %q (empty=disabled)", authToken)
	r.Use(middleware.AuthMiddleware(authToken))

	// Create handlers
	chatHandler := handler.NewChatHandler(engine)
	adminHandler := handler.NewAdminHandler(engine, checker)

	// ===== Forwarding API routes =====

	// OpenAI format
	r.POST("/v1/chat/completions", chatHandler.HandleChatCompletion)
	r.POST("/v1/embeddings", chatHandler.HandleEmbeddings)
	r.GET("/v1/models", chatHandler.HandleModels)

	// Anthropic format
	r.POST("/v1/messages", chatHandler.HandleMessages)

	// ===== Management API routes =====

	api := r.Group("/api")
	{
		api.GET("/health", adminHandler.Health)

		// Providers
		api.GET("/providers", adminHandler.GetProviders)
		api.POST("/providers", adminHandler.CreateProvider)
		api.PUT("/providers/:id", adminHandler.UpdateProvider)
		api.DELETE("/providers/:id", adminHandler.DeleteProvider)

		// Keys
		api.GET("/keys", adminHandler.GetKeys)
		api.POST("/keys", adminHandler.CreateKey)
		api.PUT("/keys/:id", adminHandler.UpdateKey)
		api.DELETE("/keys/:id", adminHandler.DeleteKey)

		// Model Groups
		api.GET("/model-groups", adminHandler.GetModelGroups)
		api.POST("/model-groups", adminHandler.CreateModelGroup)
		api.PUT("/model-groups/:id", adminHandler.UpdateModelGroup)
		api.DELETE("/model-groups/:id", adminHandler.DeleteModelGroup)

		// Routes
		api.GET("/routes", adminHandler.GetRoutes)
		api.POST("/routes", adminHandler.CreateRoute)
		api.PUT("/routes/:id", adminHandler.UpdateRoute)
		api.DELETE("/routes/:id", adminHandler.DeleteRoute)

		// Pricing
		api.GET("/pricings", adminHandler.GetPricings)
		api.POST("/pricings", adminHandler.CreatePricing)
		api.PUT("/pricings/:id", adminHandler.UpdatePricing)
		api.DELETE("/pricings/:id", adminHandler.DeletePricing)

		// Settings
		api.GET("/settings", adminHandler.GetSettings)
		api.PUT("/settings", adminHandler.UpdateSettings)

		// Stats & monitoring
		api.GET("/stats/overview", adminHandler.GetOverview)
		api.GET("/stats/keys/:id", adminHandler.GetKeyDetail)
		api.GET("/stats/consumptions", adminHandler.GetStatsConsumptions)
		api.GET("/status/keys", adminHandler.GetKeyStatuses)

		// Actions
		api.POST("/reload", adminHandler.ReloadConfig)
	}

	// ===== Static files (React SPA) =====
	// Use NoRoute so this ONLY runs for unmatched paths (not API routes)

	staticSubFS, err := fs.Sub(staticFS, "web/dist")
	if err != nil {
		log.Printf("[router] no embedded web UI found: %v", err)
		// Fallback: serve a simple index.html
		r.NoRoute(func(c *gin.Context) {
			c.String(http.StatusOK, "<html><body><h1>LocalRouter</h1><p>Web UI not built. Run: cd web && npm install && npm run build</p></body></html>")
		})
	} else {
		r.NoRoute(serveStaticFallback("/", staticSubFS))
	}

	return r
}

// serveStaticFallback serves static files with SPA fallback to index.html
// This avoids redirect loops by using http.ServeContent instead of http.FileServer
func serveStaticFallback(prefix string, fsys fs.FS) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if prefix != "/" {
			path = strings.TrimPrefix(path, prefix)
		}
		path = strings.TrimPrefix(path, "/")

		// SPA fallback: paths without file extensions serve index.html
		// Asset paths with extensions (e.g. /assets/chunk.js) return 404 on miss
		if path == "" || !strings.Contains(path, ".") {
			path = "index.html"
		}

		f, err := fsys.Open(path)
		if err != nil {
			// Only fall back to index.html for SPA routes (no extension)
			if strings.Contains(path, ".") {
				c.Status(http.StatusNotFound)
				c.String(http.StatusNotFound, "404 not found")
				return
			}
			f, err = fsys.Open("index.html")
			if err != nil {
				c.Status(http.StatusNotFound)
				return
			}
		}
		defer f.Close()

		// Read file into memory for ServeContent (fs.File doesn't support Seek)
		data, err := io.ReadAll(f)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		stat, _ := f.Stat()
		http.ServeContent(c.Writer, c.Request, path, stat.ModTime(), bytes.NewReader(data))
	}
}
