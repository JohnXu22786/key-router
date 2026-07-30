package router

import (
	"embed"
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

	// Serve static assets
	staticSubFS, err := fs.Sub(staticFS, "web/dist")
	if err != nil {
		log.Printf("[router] no embedded web UI found: %v", err)
		// Fallback: serve a simple index.html
		r.GET("/", func(c *gin.Context) {
			c.String(http.StatusOK, "<html><body><h1>LocalRouter</h1><p>Web UI not built. Run: cd web && npm install && npm run build</p></body></html>")
		})
	} else {
		r.Use(serveStaticFallback("/", staticSubFS))
	}

	return r
}

// serveStaticFallback serves static files with SPA fallback to index.html
func serveStaticFallback(prefix string, fs fs.FS) gin.HandlerFunc {
	fileServer := http.FileServer(http.FS(fs))

	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// Always strip prefix
		if prefix != "/" {
			path = strings.TrimPrefix(path, prefix)
		}

		// Try to serve the exact file
		if _, err := fs.Open(strings.TrimPrefix(path, "/")); err == nil {
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}

		// Try index.html (SPA fallback)
		if _, err := fs.Open("index.html"); err == nil {
			c.Request.URL.Path = "/index.html"
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}

		// Not found in static files
		c.Status(http.StatusNotFound)
	}
}
