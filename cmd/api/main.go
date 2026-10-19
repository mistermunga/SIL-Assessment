package main

import (
	"clinic/internal/accounts"
	"clinic/internal/auth"
	"clinic/internal/config"
	"clinic/internal/httpapi"
	"clinic/internal/store"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {

	if err := config.SetUpVariables(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	conf, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, conf.DatabaseURL)
	if err != nil {
		return fmt.Errorf("create pool: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("database unreachable: %w", err)
	}

	st := store.NewStore(pool)
	tokens := auth.NewTokenManager(conf.JWTSecret, conf.AccessTokenTTL)
	authSvc := auth.NewService(st, tokens)
	accountsSvc := accounts.NewService(st)

	api := httpapi.NewServer(authSvc, slog.Default(),
		httpapi.NewHealthModule(pool),
		httpapi.NewAccountsModule(accountsSvc),
	)

	srv := &http.Server{
		Addr:              ":" + conf.Port,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
