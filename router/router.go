package router

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"key-router/handler"
	"key-router/health"
	"key-router/middleware"
	"key-router/selector"

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
	r.Use(middleware.LocalOnlyMiddleware())

	// Auth middleware (token read from DB per request)
	r.Use(middleware.AuthMiddleware())

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
		api.POST("/keys/reorder", adminHandler.ReorderKeys)

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
		api.POST("/routes/reorder", adminHandler.ReorderRoutes)

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
			c.String(http.StatusOK, "<html><body><h1>KeyRouter</h1><p>Web UI not built. Run: cd web && npm install && npm run build</p></body></html>")
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
		// Unknown /api* and /v1* paths must 404 as JSON, not be swallowed by
		// the SPA fallback (which would mask API misconfiguration with
		// index.html and mislead API clients). The prefix check without the
		// trailing slash also covers typos like /v1foo or /apix; paths are
		// compared case-insensitively.
		p := strings.ToLower(c.Request.URL.Path)
		if strings.HasPrefix(p, "/api") || strings.HasPrefix(p, "/v1") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}

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
			// File (or index.html) missing → 404. Path already has an
			// extension or is exactly "index.html" here.
			c.Status(http.StatusNotFound)
			c.String(http.StatusNotFound, "404 not found")
			return
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
