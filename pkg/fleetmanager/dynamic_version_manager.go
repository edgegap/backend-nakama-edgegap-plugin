package fleetmanager

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"net/http"

	"github.com/edgegap/nakama-edgegap/internal/helpers"
	"github.com/heroiclabs/nakama-common/runtime"
)

// Version RPC IDs are scoped to the namespace the Fleet Manager was registered with.
var (
	RpcIdUpdateEdgegapVersion = func(namespace string) string {
		return namespaced(namespace, "update_edgegap_version", "update_version")
	}
	RpcIdGetEdgegapVersion = func(namespace string) string { return namespaced(namespace, "get_edgegap_version", "get_version") }
)

const (
	// Error messages
	ErrorMessageUnauthorized        = "unauthorized: this RPC requires server authentication"
	ErrorMessageNoVersionConfigured = "No Edgegap version configured"
	ErrorMessageSetVersionRPC       = "Please set version using %s RPC"

	// Log messages
	LogMessageStoringInitialVersion = "No version found in storage, storing initial version: %s"
	LogMessageFailedStoreInitial    = "Failed to store initial version during startup: %v"
	LogMessageFailedCheckVersion    = "Failed to check for existing version during startup: %v"
	LogMessageVersionUpdated        = "Edgegap version updated to: %s"
	LogMessageClientAttemptedS2S    = "Client attempted to call server-to-server RPC"

	// Response fields
	ResponseFieldSource   = "source"
	ResponseSourceDynamic = "dynamic"
)

type UpdateEdgegapVersionRequest struct {
	Version string `json:"version"`
}

// dynamicVersionManager manages dynamic versioning for Edgegap deployments
type dynamicVersionManager struct {
	config    *EdgegapConfig
	sm        *StorageManager
	logger    runtime.Logger
	namespace string
}

// newDynamicVersionManager creates a new dynamicVersionManager instance
func newDynamicVersionManager(config *EdgegapConfig, sm *StorageManager, logger runtime.Logger) *dynamicVersionManager {
	return &dynamicVersionManager{
		config: config,
		sm:     sm,
		logger: logger,
	}
}

// init scopes the version to the Fleet Manager namespace and stores the initial version if none is stored yet.
func (dvm *dynamicVersionManager) init(ctx context.Context, namespace string) {
	dvm.namespace = namespace

	if dvm.config.InitialVersion == "" {
		return
	}

	// Check if a version is already stored
	_, _, err := dvm.sm.ReadEdgegapVersion(ctx)
	if err != nil {
		if errors.Is(err, ErrorNoVersionFound) {
			// No version in storage, store the initial version
			dvm.logger.Info(LogMessageStoringInitialVersion, dvm.config.InitialVersion)
			if err := dvm.sm.WriteEdgegapVersion(ctx, dvm.config.InitialVersion); err != nil {
				dvm.logger.Warn(LogMessageFailedStoreInitial, err)
			}
		} else {
			dvm.logger.Warn(LogMessageFailedCheckVersion, err)
		}
	}
}

// ValidateVersionWithEdgegap validates that a version exists in Edgegap
func (dvm *dynamicVersionManager) ValidateVersionWithEdgegap(version string) error {
	apiHelper := helpers.NewAPIClient(dvm.config.ApiUrl, dvm.config.ApiToken)
	reply, err := apiHelper.Get(fmt.Sprintf("/v1/app/%s/version/%s", dvm.config.Application, version))
	if err != nil {
		return fmt.Errorf("failed to validate version with Edgegap API: %w", err)
	}

	if reply.StatusCode != http.StatusOK {
		if reply.StatusCode == http.StatusNotFound {
			return runtime.NewError(fmt.Sprintf("version '%s' does not exist for application '%s'", version, dvm.config.Application), 5) // NOT_FOUND
		}
		return runtime.NewError(fmt.Sprintf("failed to validate version with Edgegap API, status: %s", reply.Status), 13) // INTERNAL
	}

	return nil
}

// UpdateEdgegapVersion updates the Edgegap deployment version in storage (S2S only)
// Error codes used map to HTTP status codes via Nakama:
// - 3 (INVALID_ARGUMENT) → 400 Bad Request
// - 5 (NOT_FOUND) → 404 Not Found
// - 7 (PERMISSION_DENIED) → 403 Forbidden
// - 9 (FAILED_PRECONDITION) → 400 Bad Request
// - 13 (INTERNAL) → 500 Internal Server Error
func (dvm *dynamicVersionManager) UpdateEdgegapVersion(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// This RPC should only be called by servers with HTTP key authentication, not by game clients
	// Nakama automatically validates the HTTP key when the Authorization header is provided
	// If we reach this point with a user ID, it means a client is trying to call this RPC
	if _, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string); ok {
		logger.Warn(LogMessageClientAttemptedS2S + " for Edgegap version update")
		return "", runtime.NewError(ErrorMessageUnauthorized, 7) // PERMISSION_DENIED
	}

	request := &UpdateEdgegapVersionRequest{}
	if err := json.Unmarshal([]byte(payload), request); err != nil {
		return "", runtime.NewError("invalid payload format", 3) // INVALID_ARGUMENT
	}

	// Validate version is not empty
	if request.Version == "" {
		return "", runtime.NewError("version cannot be empty", 3) // INVALID_ARGUMENT
	}

	// Validate the version exists in Edgegap before storing
	if err := dvm.ValidateVersionWithEdgegap(request.Version); err != nil {
		logger.Error("Failed to validate version with Edgegap: %v", err)
		return "", err
	}

	// Store the Edgegap version using StorageManager
	if err := dvm.sm.WriteEdgegapVersion(ctx, request.Version); err != nil {
		logger.Error("Failed to store Edgegap version: %v", err)
		return "", runtime.NewError("failed to store version", 13) // INTERNAL
	}

	logger.Info(LogMessageVersionUpdated, request.Version)

	// Return success response
	response := map[string]interface{}{
		"success": true,
		"version": request.Version,
		"message": "Edgegap version updated successfully. Will be used for new deployments immediately.",
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		return "", runtime.NewError("failed to marshal response", 13) // INTERNAL
	}

	return string(responseBytes), nil
}

// GetEdgegapVersion retrieves the current Edgegap version configuration (S2S only)
func (dvm *dynamicVersionManager) GetEdgegapVersion(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// This RPC can be called by servers with HTTP key authentication
	// If we reach this point with a user ID, it means a client is trying to call this RPC
	if _, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string); ok {
		logger.Warn(LogMessageClientAttemptedS2S + " for getting Edgegap version")
		return "", runtime.NewError(ErrorMessageUnauthorized, 7) // PERMISSION_DENIED
	}

	response := map[string]interface{}{}

	// Try to read version from storage using StorageManager
	version, updatedAt, err := dvm.sm.ReadEdgegapVersion(ctx)
	if err != nil {
		if errors.Is(err, ErrorNoVersionFound) {
			// No version set yet
			response["error"] = ErrorMessageNoVersionConfigured
			response["message"] = fmt.Sprintf(ErrorMessageSetVersionRPC, RpcIdUpdateEdgegapVersion(dvm.namespace))
		} else {
			logger.Error("Failed to read Edgegap version from storage: %v", err)
			return "", runtime.NewError(fmt.Sprintf("failed to read Edgegap version: %v", err), 13) // INTERNAL
		}
	} else {
		response["version"] = version
		response[ResponseFieldSource] = ResponseSourceDynamic
		if updatedAt > 0 {
			response["updated_at"] = updatedAt
		}
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		return "", runtime.NewError("failed to marshal response", 13) // INTERNAL
	}

	return string(responseBytes), nil
}
