package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/rsherman/draftsky/internal/auth"
	"github.com/rsherman/draftsky/internal/bluesky"
	db "github.com/rsherman/draftsky/internal/db/sqlc"
	"github.com/rsherman/draftsky/internal/feed"
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

	// ADMIN_DID gates GET /admin/stats. Optional: unset leaves the route 404ing for
	// everyone (RequireAdmin treats an empty admin DID as "no admin").
	adminDID := os.Getenv("ADMIN_DID")
	if adminDID == "" {
		slog.Info("ADMIN_DID not set — /admin/stats is disabled (404 for all)")
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

	oauthApp := oauth.NewClientApp(&oauthConfig, auth.NewPGStore(pool))
	poster := bluesky.New(oauthApp)
	feedClient := feed.New(oauthApp)

	// --- HTTP server ---
	r := gin.New()

	// www redirect — must be first so it fires before any other middleware or routes.
	r.Use(func(c *gin.Context) {
		if c.Request.Host == "draftsky.social" {
			c.Redirect(301, "https://www.draftsky.social"+c.Request.RequestURI)
			c.Abort()
			return
		}
		c.Next()
	})

	r.Use(gin.Recovery())
	r.Use(gin.Logger())
	r.Use(middleware.SecurityHeaders())
	// Trusted proxies (reviewed 2026-07-11). Railway terminates TLS at its edge and
	// forwards requests through an internal proxy that connects from private address
	// space within Railway's network; Railway publishes no stable public egress CIDR
	// for the edge to pin. So we trust only the RFC 1918 private ranges plus the
	// RFC 6598 CGNAT range (100.64.0.0/10, which Railway's internal networking uses):
	// X-Forwarded-For is honoured when the immediate peer is one of those internal
	// proxies, but a direct external client cannot forge an arbitrary client IP by
	// setting the header itself — Gin ignores XFF from an untrusted peer.
	//
	// This affects LOG ACCURACY ONLY today: rate limiting is keyed per-DID (see the
	// OperationsRateLimiter and post limiter), not per-IP, so a spoofed client IP could
	// not evade a limit even if the header were trusted. c.ClientIP() is used for
	// request logging.
	r.ForwardedByClientIP = true
	if err := r.SetTrustedProxies([]string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"100.64.0.0/10",
	}); err != nil {
		slog.Error("failed to set trusted proxies", "err", err)
		os.Exit(1)
	}

	secret := []byte(sessionSecret)
	secure := appEnv == "production"
	authHandler := auth.NewHandler(oauthApp, queries, secret, secure)

	// Static assets. Now that every template reference is content-hash cache-busted
	// (?v=<hash>, see computeAssetVersions), a given URL's bytes never change, so we
	// can safely serve /static with a one-year immutable cache. This replaces the
	// previous browser-default heuristic caching, which was the root cause of the
	// stale-app.js phantom blocker (Gotcha 10): a deploy bumps the ?v= hash, minting
	// a fresh URL the browser must re-fetch, while unchanged assets stay fully cached.
	staticGroup := r.Group("/static", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	})
	staticGroup.Static("/", "./static")

	// Well-known root files
	r.GET("/robots.txt", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(
			"User-agent: *\n"+
				"Allow: /\n"+
				"Allow: /login\n"+
				"Disallow: /api/\n"+
				"Disallow: /auth/\n"+
				"Disallow: /feed/\n"+
				"Disallow: /profile/\n"+
				"Disallow: /templates\n"+
				"Disallow: /settings\n",
		))
	})
	r.GET("/favicon.svg", func(c *gin.Context) {
		c.Header("Content-Type", "image/svg+xml")
		c.File("./static/favicon.svg")
	})
	r.GET("/favicon.ico", func(c *gin.Context) {
		c.Header("Content-Type", "image/svg+xml")
		c.File("./static/favicon.svg")
	})

	// Public routes
	r.GET("/health", handlers.HandleHealth)
	r.GET("/client-metadata.json", authHandler.HandleClientMetadata)

	r.GET("/auth/login", authHandler.HandleLogin)
	r.GET("/auth/callback", authHandler.HandleCallback)
	r.POST("/auth/logout", authHandler.HandleLogout)

	// Login page (redirects to / if already authenticated)
	uiH, err := handlers.NewUIHandler(queries, secret, feedClient)
	if err != nil {
		slog.Error("failed to parse templates", "err", err)
		os.Exit(1)
	}
	r.GET("/login", uiH.HandleLoginPage)
	r.NoRoute(uiH.Handle404)

	// Authenticated web UI routes. RequireCSRF runs after RequireSession (which
	// populates the session ID it verifies against) and only guards mutating
	// methods, so the GET routes below are unaffected.
	// /admin/stats is owner-only. RequireAdmin is self-contained (validates the
	// session itself) and 404s every non-owner case, so it is NOT mounted under the
	// RequireSession web group — a redirect would advertise the route's existence.
	r.GET("/admin/stats", middleware.RequireAdmin(secret, adminDID), uiH.HandleAdminStats)

	web := r.Group("/", middleware.RequireSession(secret), middleware.TouchLastSeen(queries), middleware.RequireCSRF(secret))
	web.GET("", uiH.HandleHome)
	web.GET("/thread", uiH.HandleThreadPage)
	web.GET("/notifications", uiH.HandleNotificationsPage)
	web.GET("/feed/following", uiH.HandleFollowingFeedPartial)
	web.GET("/feed/hashtags", uiH.HandleHashtagFeedPartial)
	web.GET("/feed/custom", uiH.HandleCustomFeedPartial)
	web.GET("/profile/:actor", uiH.HandleProfilePage)
	web.GET("/profile/:actor/feed", uiH.HandleProfileFeedPartial)
	web.GET("/templates", uiH.HandleTemplatesPage)
	web.GET("/settings", uiH.HandleSettingsPage)
	web.POST("/templates", uiH.HandleWebCreateTemplate)
	web.PUT("/templates/:id", uiH.HandleWebUpdateTemplate)

	// Protected route group — all routes added here require a valid session cookie.
	// RequireCSRF runs after RequireAuth and guards only mutating methods, so the
	// GET feed routes are unaffected.
	api := r.Group("/api", middleware.RequireAuth(secret), middleware.TouchLastSeen(queries), middleware.RequireCSRF(secret))

	postH := handlers.NewPostHandler(queries, poster)
	api.POST("/post", postH.HandleCreatePost)

	feedH := handlers.NewFeedHandler(feedClient)
	api.GET("/feed/following", feedH.HandleGetFollowingFeed)
	api.GET("/feed/hashtags", feedH.HandleGetHashtagFeed)
	api.GET("/notifications", feedH.HandleGetNotifications)
	api.GET("/notifications/unread-count", feedH.HandleGetUnreadCount)

	profileH := handlers.NewProfileHandler(queries, feedClient)
	api.GET("/profile/:actor", profileH.HandleGetProfile)
	api.GET("/profile/:actor/feed", profileH.HandleGetProfileFeed)

	// Rate-limited operations — 60 req/min per DID for template CRUD and likes.
	opsLimiter := middleware.NewOperationsRateLimiter()
	rated := api.Group("/", opsLimiter.Middleware())

	templateH := handlers.NewTemplateHandler(queries, pool)
	// /reorder must be registered before /:id so Gin's static-segment priority
	// kicks in and "reorder" is never matched as a template ID.
	rated.PUT("/templates/reorder", templateH.HandleReorderTemplates)
	rated.GET("/templates", templateH.HandleGetTemplates)
	rated.POST("/templates", templateH.HandleCreateTemplate)
	rated.PUT("/templates/:id", templateH.HandleUpdateTemplate)
	rated.DELETE("/templates/:id", templateH.HandleDeleteTemplate)
	rated.GET("/composer/templates", templateH.HandleGetComposerTemplates)

	likeH := handlers.NewLikeHandler(oauthApp)
	rated.POST("/like", likeH.HandleCreateLike)
	rated.DELETE("/like", likeH.HandleDeleteLike)

	settingsH := handlers.NewSettingsHandler(queries)
	rated.PUT("/settings/theme", settingsH.HandleUpdateTheme)

	repostH := handlers.NewRepostHandler(oauthApp)
	rated.POST("/repost", repostH.HandleCreateRepost)
	rated.DELETE("/repost", repostH.HandleDeleteRepost)

	rated.PUT("/profile", profileH.HandleUpdateProfile)
	rated.GET("/actors/typeahead", profileH.HandleActorTypeahead)

	followH := handlers.NewFollowHandler(oauthApp)
	rated.POST("/follow", followH.HandleCreateFollow)
	rated.DELETE("/follow", followH.HandleDeleteFollow)

	slog.Info("starting server", "port", port, "env", appEnv)
	if err := r.Run(":" + port); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
