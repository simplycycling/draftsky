package main

import (
	"log/slog"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/rsherman/draftsky/internal/handlers"
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

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.Logger())

	r.GET("/health", handlers.HandleHealth)

	slog.Info("starting server", "port", port, "env", appEnv)
	if err := r.Run(":" + port); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
