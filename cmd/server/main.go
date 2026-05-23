package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/rsherman/draftsky/internal/auth"
	"github.com/rsherman/draftsky/internal/bluesky"
	db "github.com/rsherman/draftsky/internal/db/sqlc"
	"github.com/rsherman/draftsky/internal/handlers"
	"github.com/rsherman/draftsky/internal/middleware"
)

func main() {
	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file found, using environment variables")
	}

	appEnv := os.Getenv("APP_ENV")
	if appEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// --- Required environment variables ---
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("DATABASE_URL is not set")
		os.Exit(1)
	}

	sessionSecret := os.Getenv("SESSION_SECRET")
	if sessionSecret == "" {
		slog.Error("SESSION_SECRET is not set")
		os.Exit(1)
	}

	redirectURL := os.Getenv("OAUTH_REDIRECT_URL")
	if redirectURL == "" {
		slog.Error("OAUTH_REDIRECT_URL is not set")
		os.Exit(1)
	}

	// --- Database ---
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("failed to create database pool", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		slog.Error("failed to connect to database", "err", err)
		os.Exit(1)
	}
	slog.Info("database connection established")

	queries := db.New(pool)

	// --- AT Protocol OAuth client ---
	// In development (no OAUTH_CLIENT_ID), use the localhost config which
	// generates a special http://localhost client_id accepted by all PDSes.
	// In production, OAUTH_CLIENT_ID must be the https URL of the client
	// metadata document (served at GET /client-metadata.json).
	var oauthConfig oauth.ClientConfig
	clientID := os.Getenv("OAUTH_CLIENT_ID")
	scopes := []string{"atproto", "transition:generic"}

	if clientID == "" {
		oauthConfig = oauth.NewLocalhostConfig(redirectURL, scopes)
		slog.Info("OAuth: using localhost client config (development mode)")
	} else {
		oauthConfig = oauth.NewPublicConfig(clientID, redirectURL, scopes)
		slog.Info("OAuth: using public client config", "client_id", clientID)
	}

	// MemStore is an in-memory ClientAuthStore. All OAuth session state
	// (DPoP keys, nonces) is lost on restart. A PostgreSQL-backed store
	// will replace this in Phase 2.
	oauthApp := oauth.NewClientApp(&oauthConfig, oauth.NewMemStore())
	poster := bluesky.New(oauthApp)

	// --- HTTP server ---
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.Logger())

	secret := []byte(sessionSecret)
	secure := appEnv == "production"
	authHandler := auth.NewHandler(oauthApp, queries, secret, secure)

	// Public routes
	r.GET("/health", handlers.HandleHealth)
	r.GET("/client-metadata.json", authHandler.HandleClientMetadata)

	r.GET("/auth/login", authHandler.HandleLogin)
	r.GET("/auth/callback", authHandler.HandleCallback)
	r.POST("/auth/logout", authHandler.HandleLogout)

	// Protected route group — all routes added here require a valid session cookie.
	api := r.Group("/api", middleware.RequireAuth(secret))

	templateH := handlers.NewTemplateHandler(queries, pool)
	// /reorder must be registered before /:id so Gin's static-segment priority
	// kicks in and "reorder" is never matched as a template ID.
	api.PUT("/templates/reorder", templateH.HandleReorderTemplates)
	api.GET("/templates", templateH.HandleGetTemplates)
	api.POST("/templates", templateH.HandleCreateTemplate)
	api.PUT("/templates/:id", templateH.HandleUpdateTemplate)
	api.DELETE("/templates/:id", templateH.HandleDeleteTemplate)

	postH := handlers.NewPostHandler(queries, poster)
	api.POST("/post", postH.HandleCreatePost)

	slog.Info("starting server", "port", port, "env", appEnv)
	if err := r.Run(":" + port); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
