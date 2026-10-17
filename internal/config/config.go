package config

import (
	"errors"
	"fmt"
	"os"
	"time"
)

type Config struct {
	DatabaseURL    string
	JWTSecret      []byte
	ClinicTZ       *time.Location
	Port           string
	AccessTokenTTL time.Duration
}

func Load() (Config, error) {
	var errs []error
	var config Config

	config.DatabaseURL = require("DATABASE_URL", &errs)

	if key := require("JWT_SECRET", &errs); key != "" {
		if len(key) < 32 {
			errs = append(errs, errors.New("JWT_SECRET must be at least 32 characters"))
		}
		config.JWTSecret = []byte(key)
	}

	if zone := require("CLINIC_TIMEZONE", &errs); zone != "" {
		loc, err := time.LoadLocation(zone)

		if err != nil {
			errs = append(errs, fmt.Errorf("CLINIC_TZ %q: %w", zone, err))
		}
		config.ClinicTZ = loc
	}

	config.Port = getOr("PORT", "8060")

	ttl, err := time.ParseDuration(getOr("ACCESS_TOKEN_TTL", "15m"))
	if err != nil {
		errs = append(errs, fmt.Errorf("ACCESS_TOKEN_TTL: %w", err))
	}
	config.AccessTokenTTL = ttl

	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}

	return config, nil
}

func require(key string, errs *[]error) string {
	v := os.Getenv(key)

	if v == "" {
		*errs = append(*errs, fmt.Errorf("%s is not set", key))
	}
	return v
}

func getOr(key string, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
