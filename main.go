package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/edgegap/nakama-edgegap/pkg/fleetmanager"
	"github.com/heroiclabs/nakama-common/runtime"
)

func InitModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	initStart := time.Now()

	cfg, err := edgegapConfigFromEnv(ctx)
	if err != nil {
		logger.WithField("error", err).Error("failed to read Edgegap configuration")
		return err
	}

	// Register the Fleet Manager
	efm, err := fleetmanager.NewEdgegapFleetManager(ctx, logger, db, nk, cfg)
	if err != nil {
		logger.WithField("error", err).Error("failed to create Edgegap fleet manager")
		return err
	}

	if err = initializer.RegisterFleetManager(efm); err != nil {
		logger.WithField("error", err).Error("failed to register Edgegap fleet manager")
		return err
	}

	// Register Authentication Methods
	if err := initializer.RegisterAfterAuthenticateCustom(fleetmanager.OnAuthenticateUpdateCustom); err != nil {
		logger.WithField("error", err).Error("failed to register AfterAuthenticateCustom")
		return err
	}

	logger.Info("Edgegap Plugin loaded in '%s'", time.Now().Sub(initStart).String())

	return nil
}

// edgegapConfigFromEnv reads the Edgegap Fleet Manager configuration from the Nakama runtime environment.
func edgegapConfigFromEnv(ctx context.Context) (fleetmanager.EdgegapConfig, error) {
	env, ok := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)
	if !ok {
		return fleetmanager.EdgegapConfig{}, runtime.NewError("expects env ctx value to be a map[string]string", 3)
	}

	required := make(map[string]string, 5)
	for _, key := range []string{"EDGEGAP_API_URL", "EDGEGAP_API_TOKEN", "EDGEGAP_APPLICATION", "EDGEGAP_PORT_NAME", "NAKAMA_ACCESS_URL"} {
		value, ok := env[key]
		if !ok {
			return fleetmanager.EdgegapConfig{}, runtime.NewError(key+" not found in environment", 3)
		}
		required[key] = value
	}

	cfg := fleetmanager.NewEdgegapConfig(required["EDGEGAP_API_URL"], required["EDGEGAP_API_TOKEN"], required["EDGEGAP_APPLICATION"], required["EDGEGAP_PORT_NAME"], required["NAKAMA_ACCESS_URL"])

	// Initial version is optional, EDGEGAP_VERSION is read for backward compatibility
	cfg.InitialVersion = env["INITIAL_EDGEGAP_VERSION"]
	if cfg.InitialVersion == "" {
		cfg.InitialVersion = env["EDGEGAP_VERSION"]
	}

	// Durations keep their default when unset, an empty interval disables the worker
	durations := []struct {
		key        string
		value      *time.Duration
		emptyValue bool
	}{
		{"EDGEGAP_POLLING_INTERVAL", &cfg.PollingInterval, true},
		{"NAKAMA_CLEANUP_INTERVAL", &cfg.CleanupInterval, true},
		{"NAKAMA_RESERVATION_MAX_DURATION", &cfg.ReservationMaxDuration, false},
	}
	for _, d := range durations {
		value, ok := env[d.key]
		if !ok {
			continue
		}
		if d.emptyValue && strings.TrimSpace(value) == "" {
			*d.value = 0
			continue
		}
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return fleetmanager.EdgegapConfig{}, fmt.Errorf("invalid %s: %s", d.key, value)
		}
		*d.value = parsed
	}

	return cfg, nil
}
