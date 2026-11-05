package fleetmanager

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/edgegap/nakama-edgegap/internal/helpers"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	// Metadata key for per-deployment Edgegap version override
	MetadataEdgegapVersionKey = "edgegap_version"
	// Metadata key for per-deployment Edgegap application override
	MetadataEdgegapApplicationKey = "edgegap_application"

	// Error messages
	ErrorMessageNoVersionFound = "no Edgegap version found - please set version using %s RPC or provide INITIAL_EDGEGAP_VERSION"

	// Log messages
	LogMessageUsingVersionFromStorage = "Using Edgegap version from storage: %s"
)

type EdgegapManager struct {
	configuration  *EdgegapConfig
	apiHelper      *helpers.APIClient
	logger         runtime.Logger
	storageManager *StorageManager
	versionManager *dynamicVersionManager
	namespace      string
}

// NewEdgegapManager initializes a new EdgegapManager instance with a validated configuration and sets up API access.
func NewEdgegapManager(logger runtime.Logger, configuration *EdgegapConfig, sm *StorageManager) *EdgegapManager {
	return &EdgegapManager{
		configuration:  configuration,
		apiHelper:      helpers.NewAPIClient(configuration.ApiUrl, configuration.ApiToken),
		logger:         logger,
		storageManager: sm,
		versionManager: newDynamicVersionManager(configuration, sm, logger),
	}
}

// init registers the RPC functions of the Fleet Manager under its namespace.
func (em *EdgegapManager) init(ctx context.Context, initializer runtime.Initializer, efm *EdgegapFleetManager, namespace string) error {
	em.namespace = namespace

	// Retrieve Nakama configuration
	config, err := initializer.GetConfig()
	if err != nil {
		return err
	}
	em.configuration.nakamaHttpKey = config.GetRuntime().GetHTTPKey()

	em.versionManager.init(ctx, namespace)

	eem := &EdgegapEventManager{efm: efm}

	// Register RPC functions for handling various events
	rpcToRegisters := map[string]func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error){
		RpcIdEventDeploymentReady(namespace):      eem.handleDeploymentReadyEvent,
		RpcIdEventDeploymentError(namespace):      eem.handleDeploymentErrorEvent,
		RpcIdEventDeploymentTerminated(namespace): eem.handleDeploymentTerminatedEvent,
		RpcIdEventConnection(namespace):           eem.handleConnectionEvent,
		RpcIdEventInstance(namespace):             eem.handleInstanceEvent,
		RpcIdInstanceSessionCreate(namespace):     efm.createInstanceSession,
		RpcIdInstanceSessionGet(namespace):        efm.getInstanceSession,
		RpcIdInstanceSessionJoin(namespace):       efm.joinInstanceSession,
		RpcIdInstanceSessionList(namespace):       efm.listInstanceSession,
		// S2S RPCs for managing Edgegap version
		RpcIdUpdateEdgegapVersion(namespace): em.versionManager.UpdateEdgegapVersion,
		RpcIdGetEdgegapVersion(namespace):    em.versionManager.GetEdgegapVersion,
	}

	// Register each RPC function with the Nakama runtime
	for rpcId, function := range rpcToRegisters {
		if err := initializer.RegisterRpc(rpcId, function); err != nil {
			return err
		}
	}

	return nil
}

// getFormattedUrl constructs a formatted URL for Nakama API callbacks.
func (em *EdgegapManager) getFormattedUrl(path string) string {
	return fmt.Sprintf("%s/v2/rpc/%s?http_key=%s&unwrap", em.configuration.NakamaAccessUrl, path, em.configuration.nakamaHttpKey)
}

// CreateDeployment initiates a new deployment on Edgegap using the given users' IP addresses and metadata.
func (em *EdgegapManager) CreateDeployment(usersIP []string, metadata map[string]any) (*EdgegapDeploymentResponse, error) {
	// Prepare deployment data
	deployment, err := em.getDeploymentCreation(usersIP, metadata)
	if err != nil {
		return nil, err
	}

	// Send deployment request to Edgegap API
	reply, err := em.apiHelper.Post("/v2/deployments", deployment)
	if err != nil {
		return nil, err
	}
	defer reply.Body.Close()

	// Check if request was accepted
	if reply.StatusCode != http.StatusAccepted {
		body, err := io.ReadAll(reply.Body)
		if err != nil {
			return nil, err
		}
		var msg EdgegapApiMessage
		if jsonErr := json.Unmarshal(body, &msg); jsonErr != nil || msg.Message == "" {
			return nil, fmt.Errorf("could not create deployment: status %d, body: %s", reply.StatusCode, string(body))
		}
		return nil, fmt.Errorf("could not create deployment: status %d: %s", reply.StatusCode, msg.Message)
	}

	// Parse the response body
	body, err := io.ReadAll(reply.Body)
	if err != nil {
		return nil, err
	}

	var response EdgegapDeploymentResponse
	if err = json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal deployment response: %w", err)
	}

	return &response, nil
}

// getDeploymentCreation prepares the deployment payload, including metadata and environment variables.
func (em *EdgegapManager) getDeploymentCreation(usersIP []string, metadata map[string]any) (*EdgegapDeploymentCreation, error) {
	var users []EdgegapDeploymentUser

	// Convert user IPs into EdgegapDeploymentUser objects
	for _, ip := range usersIP {
		users = append(users, EdgegapDeploymentUser{
			UserType: "ip_address",
			UserData: EdgegapUserData{IpAddress: ip},
		})
	}

	// Marshal metadata into JSON format
	metadataValue, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}

	// Use per-deployment version override from metadata if provided,
	// otherwise fall back to the global version from storage.
	var version string
	if v, ok := metadata[MetadataEdgegapVersionKey].(string); ok && v != "" {
		version = v
		em.logger.Debug("Using per-deployment Edgegap version from metadata: %s", version)
	} else {
		version, err = em.getEdgegapVersion()
		if err != nil {
			return nil, fmt.Errorf("failed to get Edgegap version: %w", err)
		}
	}

	application := em.configuration.Application
	if a, ok := metadata[MetadataEdgegapApplicationKey].(string); ok && a != "" {
		application = a
	}

	// Construct deployment request payload
	return &EdgegapDeploymentCreation{
		Application: application,
		Version:     version,
		Users:       users,
		EnvironmentVariables: []EdgegapEnvironmentVariable{
			{
				Key:      "NAKAMA_CONNECTION_EVENT_URL",
				Value:    em.getFormattedUrl(RpcIdEventConnection(em.namespace)),
				IsHidden: true,
			},
			{
				Key:      "NAKAMA_INSTANCE_EVENT_URL",
				Value:    em.getFormattedUrl(RpcIdEventInstance(em.namespace)),
				IsHidden: true,
			},
			{
				Key:      "NAKAMA_EDGEGAP_NAMESPACE",
				Value:    em.namespace,
				IsHidden: false,
			},
			{
				Key:      "NAKAMA_INSTANCE_METADATA",
				Value:    string(metadataValue),
				IsHidden: false,
			},
		},
		Tags: []string{
			"nakama",
		},
		WebhookOnReady:      EdgegapWebhook{Url: em.getFormattedUrl(RpcIdEventDeploymentReady(em.namespace))},
		WebhookOnError:      EdgegapWebhook{Url: em.getFormattedUrl(RpcIdEventDeploymentError(em.namespace))},
		WebhookOnTerminated: EdgegapWebhook{Url: em.getFormattedUrl(RpcIdEventDeploymentTerminated(em.namespace))},
	}, nil
}

// StopDeployment sends a request to stop an active deployment on Edgegap.
func (em *EdgegapManager) StopDeployment(requestID string) (*EdgegapApiMessage, error) {
	// Send stop request to Edgegap API
	reply, err := em.apiHelper.Delete("/v1/stop/" + requestID)
	if err != nil {
		return nil, err
	}
	defer reply.Body.Close()

	// Check if request was successful
	if reply.StatusCode == http.StatusOK || reply.StatusCode == http.StatusAccepted {
		body, err := io.ReadAll(reply.Body)
		if err != nil {
			return nil, err
		}
		var message EdgegapApiMessage
		err = json.Unmarshal(body, &message)

		return &message, err
	}

	return nil, errors.New("Error stopping edgegap deployment " + requestID)
}

// ListAllDeployments retrieves all deployment summaries from the Edgegap API by paginating until no more pages exist.
func (em *EdgegapManager) ListAllDeployments() ([]EdgegapDeploymentSummary, error) {
	var allDeployments []EdgegapDeploymentSummary
	page := 1

	for {
		reply, err := em.apiHelper.Get("/v1/deployments?page=" + strconv.Itoa(page))
		if err != nil {
			return nil, err
		}
		defer reply.Body.Close()

		if reply.StatusCode != http.StatusOK {
			return nil, errors.New("error listing all Edgegap deployments")
		}

		body, err := io.ReadAll(reply.Body)
		if err != nil {
			return nil, err
		}

		var response EdgegapDeploymentList
		err = json.Unmarshal(body, &response)
		if err != nil {
			return nil, err
		}

		allDeployments = append(allDeployments, response.Data...)

		// Check if there's another page
		if !response.Pagination.HasNext {
			break
		}

		page = response.Pagination.NextPageNumber
	}

	return allDeployments, nil
}

// getEdgegapVersion retrieves the Edgegap version from storage
func (em *EdgegapManager) getEdgegapVersion() (string, error) {
	ctx := context.Background()

	// Read version from storage (initial version is already stored at startup if configured)
	version, _, err := em.storageManager.ReadEdgegapVersion(ctx)
	if err != nil {
		if errors.Is(err, ErrorNoVersionFound) {
			return "", fmt.Errorf(ErrorMessageNoVersionFound, RpcIdUpdateEdgegapVersion(em.namespace))
		}
		return "", fmt.Errorf("failed to read Edgegap version from storage: %w", err)
	}

	em.logger.Debug(LogMessageUsingVersionFromStorage, version)
	return version, nil
}
