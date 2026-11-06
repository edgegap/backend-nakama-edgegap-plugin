package fleetmanager

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/edgegap/nakama-edgegap/internal/helpers"
	"github.com/heroiclabs/nakama-common/runtime"
)

// Event RPC IDs are scoped to the namespace the Fleet Manager was registered with.
var (
	RpcIdEventDeploymentReady = func(namespace string) string {
		return namespaced(namespace, "edgegap_deployment_ready", "deployment_ready")
	}
	RpcIdEventDeploymentError = func(namespace string) string {
		return namespaced(namespace, "edgegap_deployment_error", "deployment_error")
	}
	RpcIdEventDeploymentTerminated = func(namespace string) string {
		return namespaced(namespace, "edgegap_deployment_terminated", "deployment_terminated")
	}
	RpcIdEventConnection = func(namespace string) string { return namespaced(namespace, "edgegap_connection", "connection") }
	RpcIdEventInstance   = func(namespace string) string { return namespaced(namespace, "edgegap_instance", "instance") }
)

var (
	ErrInvalidInput  = runtime.NewError("input is invalid", 3)       // INVALID_ARGUMENT
	ErrInternalError = runtime.NewError("internal server error", 13) // INTERNAL
)

type EventMessage struct {
	payload string
	headers map[string][]string
	params  map[string][]string
}

type EdgegapEventManager struct {
	efm *EdgegapFleetManager
}

// unpack extracts headers and query parameters from the context
// and returns an EventMessage struct containing them along with the payload.
func (eem *EdgegapEventManager) unpack(ctx context.Context, payload string) (*EventMessage, error) {
	headers, ok := ctx.Value(runtime.RUNTIME_CTX_HEADERS).(map[string][]string)
	if !ok {
		return nil, ErrInternalError
	}

	params, ok := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string][]string)
	if !ok {
		return nil, ErrInternalError
	}

	return &EventMessage{
		payload: payload,
		headers: headers,
		params:  params,
	}, nil
}

// handleDeploymentReadyEvent processes the deployment "ready" webhook from Edgegap.
// It marks the instance as running and stores the connection info (IP, FQDN, external port).
func (eem *EdgegapEventManager) handleDeploymentReadyEvent(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	msg, err := eem.unpack(ctx, payload)
	if err != nil {
		return "", err
	}

	var deployment EdgegapDeploymentStatus
	if err := json.Unmarshal([]byte(msg.payload), &deployment); err != nil {
		return "", err
	}

	logger.Info("Edgegap deployment ready #%s", deployment.RequestId)

	_, err = eem.efm.storageManager.MutateInstance(ctx, deployment.RequestId, func(instance *runtime.InstanceInfo) error {
		return eem.efm.storageManager.ApplyDeploymentReady(instance, &deployment, eem.efm.edgegapManager.configuration.PortName, logger)
	})
	if errors.Is(err, ErrInstanceNotFound) {
		return "", errors.New("no instance found with requestId " + deployment.RequestId)
	}
	if err != nil {
		return "", err
	}
	return "ok", nil
}

// handleDeploymentErrorEvent processes the deployment "error" webhook from Edgegap.
// It marks the instance as errored and invokes the CreateError callback so the
// caller that requested the deployment is notified of the failure.
func (eem *EdgegapEventManager) handleDeploymentErrorEvent(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	msg, err := eem.unpack(ctx, payload)
	if err != nil {
		return "", err
	}

	var deployment EdgegapDeploymentStatus
	if err := json.Unmarshal([]byte(msg.payload), &deployment); err != nil {
		return "", err
	}

	logger.Warn("Edgegap deployment error #%s : %s", deployment.RequestId, deployment.ErrorDetail)

	callbackId := ""
	_, err = eem.efm.storageManager.MutateInstance(ctx, deployment.RequestId, func(instance *runtime.InstanceInfo) error {
		instance.Status = EdgegapStatusError

		ei, err := eem.efm.storageManager.ExtractEdgegapInstance(instance)
		if err != nil {
			logger.Error("failed to extract edgegap instance for error callback #%s: %v", deployment.RequestId, err)
			return err
		}
		callbackId = ei.CallbackId
		return nil
	})
	if errors.Is(err, ErrInstanceNotFound) {
		return "", errors.New("no instance found with requestId " + deployment.RequestId)
	}
	if err != nil {
		eem.efm.logger.Error("failed to mark instance as error for deployment %s: %v", deployment.RequestId, err)
		return "", err
	}
	eem.efm.callbackHandler.InvokeCallback(callbackId, runtime.CreateError, nil, nil, nil, errors.New("an error occurred with edgegap deployment"))

	return "ok", nil
}

// handleDeploymentTerminatedEvent processes the deployment "terminated" webhook from Edgegap.
// It marks the instance as terminated so the sync worker can clean it from storage.
func (eem *EdgegapEventManager) handleDeploymentTerminatedEvent(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	msg, err := eem.unpack(ctx, payload)
	if err != nil {
		return "", err
	}

	var deployment EdgegapDeploymentStatus
	if err := json.Unmarshal([]byte(msg.payload), &deployment); err != nil {
		return "", err
	}

	logger.Info("Edgegap deployment terminated #%s", deployment.RequestId)

	_, err = eem.efm.storageManager.MutateInstance(ctx, deployment.RequestId, func(instance *runtime.InstanceInfo) error {
		instance.Status = EdgegapStatusTerminated
		return nil
	})
	if errors.Is(err, ErrInstanceNotFound) {
		return "", errors.New("no instance found with requestId " + deployment.RequestId)
	}
	if err != nil {
		return "", err
	}
	return "ok", nil
}

// handleConnectionEvent processes connection-related events.
// It updates the instance session's connection and reservation metadata.
func (eem *EdgegapEventManager) handleConnectionEvent(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	msg, err := eem.unpack(ctx, payload)
	if err != nil {
		return "", err
	}

	var connectionEvent ConnectionEventMessage
	if err := json.Unmarshal([]byte(msg.payload), &connectionEvent); err != nil {
		return "", err
	}

	_, err = eem.efm.storageManager.MutateInstance(ctx, connectionEvent.InstanceId, func(instance *runtime.InstanceInfo) error {
		return eem.efm.storageManager.ApplyConnectionReport(instance, connectionEvent.Connections)
	})
	if errors.Is(err, ErrInstanceNotFound) {
		return "", errors.New("no instance found with instanceId " + connectionEvent.InstanceId)
	}
	if err != nil {
		return "", err
	}

	return "ok", nil
}

// handleInstanceEvent processes instance state change events.
// It updates the instance session's status based on the event action.
func (eem *EdgegapEventManager) handleInstanceEvent(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	msg, err := eem.unpack(ctx, payload)
	if err != nil {
		return "", err
	}

	var instanceEvent InstanceEventMessage
	if err := json.Unmarshal([]byte(msg.payload), &instanceEvent); err != nil {
		return "", err
	}

	// Log and resolve the status outside the mutation, which may be retried on version conflicts.
	action := strings.ToUpper(instanceEvent.Action)
	var status string
	switch action {
	case InstanceEventStateReady:
		logger.Info("Edgegap instance ready id=%s : %s", instanceEvent.InstanceId, instanceEvent.Message)
		status = EdgegapStatusReady
	case InstanceEventStateStop:
		logger.Info("Edgegap instance stop #%s: %s", instanceEvent.InstanceId, instanceEvent.Message)
		status = EdgegapStatusStopping
	case InstanceEventStateError:
		logger.Error("Edgegap instance state error #%s: %s", instanceEvent.InstanceId, instanceEvent.Message)
		status = EdgegapStatusError
	default:
		logger.Error("Unknown action #%s: %s", instanceEvent.Action, instanceEvent.Message)
		status = EdgegapStatusUnknown
	}

	readyCallbackId := ""
	instance, err := eem.efm.storageManager.MutateInstance(ctx, instanceEvent.InstanceId, func(instance *runtime.InstanceInfo) error {
		instance.Status = status
		if action == InstanceEventStateReady {
			// Extract new Metadata coming from the Instance Server and merge it with current
			instance.Metadata = helpers.MergeMaps(instance.Metadata, instanceEvent.Metadata)

			ei, err := eem.efm.storageManager.ExtractEdgegapInstance(instance)
			if err != nil {
				return err
			}
			readyCallbackId = ei.CallbackId
		}
		return nil
	})
	if errors.Is(err, ErrInstanceNotFound) {
		return "", errors.New("no instance found with instanceId " + instanceEvent.InstanceId)
	}
	if err != nil {
		return "", err
	}

	// Invoke the ready callback only after the updated instance (including the
	// merged game_server metadata) is persisted. Otherwise clients notified by
	// the callback may query instance_list and read a stale record that is
	// still missing the game_server fields, causing empty connection info.
	if readyCallbackId != "" {
		eem.efm.callbackHandler.InvokeCallback(readyCallbackId, runtime.CreateSuccess, instance, nil, nil, nil)
	}

	if action == InstanceEventStateStop {
		_, err := eem.efm.edgegapManager.StopDeployment(instanceEvent.InstanceId)
		if err != nil {
			return "", err
		}
	}

	return "ok", nil
}
