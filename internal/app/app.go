package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"clinic/internal/accounts"
	"clinic/internal/auth"
	"clinic/internal/booking"
	"clinic/internal/config"
	"clinic/internal/httpapi"
	"clinic/internal/scheduling"
	"clinic/internal/store"
)

// New wires every service and module, seeds the initial administrator, and returns
// the HTTP handler. main and the integration tests both call it.
func New(ctx context.Context, conf config.Config, pool *pgxpool.Pool, log *slog.Logger) (http.Handler, error) {
	st := store.NewStore(pool)
	tokens := auth.NewTokenManager(conf.JWTSecret, conf.AccessTokenTTL)

	authSvc := auth.NewService(st, tokens)
	accountsSvc := accounts.NewService(st)
	schedulingSvc := scheduling.NewService(st, conf.ClinicTZ)
	bookingSvc := booking.NewService(st, conf.ClinicTZ)

	created, err := accountsSvc.EnsureAdmin(ctx, accounts.RegisterInput{
		Username:  conf.AdminUsername,
		Email:     conf.AdminEmail,
		Password:  conf.AdminPassword,
		Firstname: "Clinic",
		Lastname:  "Administrator",
	})
	if err != nil {
		return nil, fmt.Errorf("seed administrator: %w", err)
	}
	if created {
		log.Info("created the initial administrator", "username", conf.AdminUsername)
	}

	api := httpapi.NewServer(authSvc, log,
		httpapi.NewHealthModule(pool),
		httpapi.NewAccountsModule(accountsSvc, tokens),
		httpapi.NewAuthModule(authSvc),
		httpapi.NewBookingModule(bookingSvc, conf.ClinicTZ),
		httpapi.NewDoctorsModule(accountsSvc),
		httpapi.NewScheduleModule(schedulingSvc),
	)
	return api.Handler(), nil
}
