package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/caarlos0/env/v11"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/nanohype/portal/internal/domain"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	direction := flag.String("direction", "up", "Migration direction: up or down")
	steps := flag.Int("steps", 0, "Number of migrations to run (0 = all)")
	migrationsPath := flag.String("path", "file://migrations", "Path to migrations directory")
	flag.Parse()

	cfg := &domain.Config{}
	if err := env.Parse(cfg); err != nil {
		logger.Error("failed to parse config", "error", err)
		os.Exit(1)
	}

	// Run application migrations (golang-migrate)
	m, err := migrate.New(*migrationsPath, cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed to create migrator", "error", err)
		os.Exit(1)
	}
	defer m.Close()

	switch *direction {
	case "up":
		if *steps > 0 {
			err = m.Steps(*steps)
		} else {
			err = m.Up()
		}
	case "down":
		if *steps > 0 {
			err = m.Steps(-*steps)
		} else {
			err = m.Down()
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown direction: %s\n", *direction)
		os.Exit(1)
	}

	if err != nil && err != migrate.ErrNoChange {
		logger.Error("app migration failed", "error", err)
		os.Exit(1)
	}

	version, dirty, _ := m.Version()
	logger.Info("app migration complete", "version", version, "dirty", dirty)

	// Run River queue migrations
	dir := rivermigrate.DirectionUp
	if *direction == "down" {
		dir = rivermigrate.DirectionDown
	}

	dbPool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed to connect for river migration", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	riverMigrator, err := rivermigrate.New[pgx.Tx](riverpgxv5.New(dbPool), nil)
	if err != nil {
		logger.Error("failed to create river migrator", "error", err)
		os.Exit(1)
	}

	res, err := riverMigrator.Migrate(context.Background(), dir, nil)
	if err != nil {
		logger.Error("river migration failed", "error", err)
		os.Exit(1)
	}

	if len(res.Versions) > 0 {
		logger.Info("river migration complete", "versions_applied", len(res.Versions))
	} else {
		logger.Info("river migration complete", "status", "no change")
	}
}
