package fleetmanager

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	DeploymentIdKey = "deployment_id"
)

var _ runtime.FleetManagerInitializer = (*EdgegapFleetManager)(nil)

// EdgegapFleetManager handles fleet management operations, interacting with the database,
// Nakama runtime, and Edgegap API.
type EdgegapFleetManager struct {
	ctx             context.Context
	logger          runtime.Logger
	nk              runtime.NakamaModule
	db              *sql.DB
	callbackHandler runtime.FmCallbackHandler
	edgegapManager  *EdgegapManager
	storageManager  *StorageManager
	namespace       string
}

// NewEdgegapFleetManager initializes a new fleet manager instance with dependencies.
func NewEdgegapFleetManager(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, conf EdgegapConfig) (*EdgegapFleetManager, error) {
	if err := conf.Validate(); err != nil {
		logger.WithField("error", err).Error("edgegap fleet manager configuration invalid")
		return nil, err
	}

	// Initialize Storage Manager
	sm := NewStorageManager(nk, logger)

	// Initialize Edgegap Manager
	em := NewEdgegapManager(logger, &conf, sm)

	return &EdgegapFleetManager{
		ctx:             ctx,
		logger:          logger,
		nk:              nk,
		db:              db,
		callbackHandler: nil,
		edgegapManager:  em,
		storageManager:  sm,
	}, nil
}

// Init sets up the Nakama module and callback handler for the fleet manager. The storage collection,
// storage index and RPC IDs are scoped to the namespace the fleet manager was registered with.
func (efm *EdgegapFleetManager) Init(initializer runtime.Initializer, nk runtime.NakamaModule, callbackHandler runtime.FmCallbackHandler, namespace string) error {
	efm.nk = nk
	efm.callbackHandler = callbackHandler
	efm.namespace = namespace
	efm.storageManager.setNamespace(namespace)

	// Register Storage Index for tracking Edgegap instances
	if err := initializer.RegisterStorageIndex(
		StorageEdgegapIndex(namespace),
		StorageEdgegapInstancesCollection(namespace),
		"",
		[]string{"id", "create_time", "status", "player_count", "metadata"},
		[]string{"create_time", "player_count"},
		1_000_000,
		false,
	); err != nil {
		return err
	}

	if err := efm.edgegapManager.init(efm.ctx, initializer, efm, namespace); err != nil {
		return err
	}

	// Background worker to sync deployment info from Edgegap.
	go efm.syncInstancesWorker()
	go efm.runCleanupScheduler()

	efm.logger.WithField("namespace", namespace).Info("Edgegap Fleet Manager initialized")

	return nil
}

// Create provisions a new Edgegap deployment based on the given players.
func (efm *EdgegapFleetManager) Create(ctx context.Context, maxPlayers int, userIds []string, latencies []runtime.FleetUserLatencies, metadata map[string]any, callback runtime.FmCreateCallbackFn) (map[string]string, error) {
	efm.logger.Info("Requesting a new Deployment")
	callbackId := efm.callbackHandler.GenerateCallbackId()
	efm.callbackHandler.SetCallback(callbackId, callback)

	// Fetch IP addresses of users
	userIps, err := efm.storageManager.getUserIPs(ctx, userIds)
	if err != nil {
		efm.callbackHandler.InvokeCallback(callbackId, runtime.CreateError, nil, nil, nil, errors.New("unexpected Error while parsing Users Data"))
		return nil, err
	}

	// Use caller IP if user IPs are unavailable
	if len(userIps) == 0 {
		callerIP, ok := ctx.Value(runtime.RUNTIME_CTX_CLIENT_IP).(string)
		if !ok {
			efm.logger.Error("failed to extract client IP from context")
			efm.callbackHandler.InvokeCallback(callbackId, runtime.CreateError, nil, nil, nil, errors.New("an unexpected error occurred"))
			return nil, ErrInternalError
		}
		userIps = append(userIps, callerIP)
	}

	// Request Edgegap deployment
	deployment, err := efm.edgegapManager.CreateDeployment(userIps, metadata)
	if err != nil {
		efm.logger.WithField("error", err).Error("failed to create Edgegap instance")
		efm.callbackHandler.InvokeCallback(callbackId, runtime.CreateError, nil, nil, nil, errors.New("error while communicating with Edgegap"))
		return nil, err
	}

	// Validate Edgegap response
	if deployment.RequestId == "" {
		efm.logger.Error("failed to create Edgegap instance: empty request_id in response")
		efm.callbackHandler.InvokeCallback(callbackId, runtime.CreateError, nil, nil, nil, errors.New("error while creating Edgegap Deployment"))
		return nil, errors.New("failed to create deployment")
	}

	// Store the new instance session in the database
	_, err = efm.storageManager.createDbInstance(ctx, deployment.RequestId, maxPlayers, userIds, callbackId, metadata)
	if err != nil {
		efm.logger.WithField("error", err).Error("failed to create Storage Instance Session")
		efm.callbackHandler.InvokeCallback(callbackId, runtime.CreateError, nil, nil, nil, errors.New("error while creating Instance Session"))
		return nil, err
	}

	return map[string]string{DeploymentIdKey: deployment.RequestId}, nil
}

// Get retrieves an instance session instance by its ID.
func (efm *EdgegapFleetManager) Get(ctx context.Context, id string) (*runtime.InstanceInfo, error) {
	return efm.storageManager.getDbInstance(ctx, id)
}

// List retrieves instance session instances based on a query, sorted by player count and creation time.
func (efm *EdgegapFleetManager) List(ctx context.Context, query string, limit int, cursor string) ([]*runtime.InstanceInfo, string, error) {
	entries, newCursor, err := efm.nk.StorageIndexList(ctx, "", efm.storageManager.index, query, limit, []string{"player_count", "-create_time"}, cursor)
	if err != nil {
		return nil, "", err
	}

	results := make([]*runtime.InstanceInfo, 0)
	for _, so := range entries.GetObjects() {
		var info *runtime.InstanceInfo
		if err = json.Unmarshal([]byte(so.Value), &info); err != nil {
			return nil, "", err
		}
		results = append(results, info)
	}

	return results, newCursor, nil
}

// Join allows users to join an existing instance session.
func (efm *EdgegapFleetManager) Join(ctx context.Context, id string, userIds []string, metadata map[string]string) (*runtime.JoinInfo, error) {
	if id == "" {
		return nil, errors.New("expects id to be a valid InstanceSessionId")
	}

	if len(userIds) < 1 {
		return nil, errors.New("expects userIds to have at least one valid user id")
	}

	instance, err := efm.storageManager.MutateInstance(ctx, id, func(instance *runtime.InstanceInfo) error {
		return efm.storageManager.ReserveSeats(instance, userIds)
	})
	if errors.Is(err, ErrInstanceNotFound) {
		return nil, errors.New("instance not found")
	}
	if err != nil {
		return nil, err
	}

	return &runtime.JoinInfo{
		InstanceInfo: instance,
		SessionInfo:  nil,
	}, nil
}

// Leave drops the reservations Join or Create made for userIds.
func (efm *EdgegapFleetManager) Leave(ctx context.Context, id string, userIds []string) error {
	_, err := efm.storageManager.MutateInstance(ctx, id, func(instance *runtime.InstanceInfo) error {
		return efm.storageManager.ReleaseSeats(instance, userIds)
	})
	if errors.Is(err, ErrInstanceNotFound) {
		return nil
	}
	return err
}

// Update merges metadata into an instance session. The player count is derived from the connections
// the Instance Server reports, so playerCount is not stored.
func (efm *EdgegapFleetManager) Update(ctx context.Context, id string, playerCount int, metadata map[string]any) error {
	_, err := efm.storageManager.MutateInstance(ctx, id, func(instance *runtime.InstanceInfo) error {
		if playerCount != instance.PlayerCount {
			efm.logger.Warn("Player Count should not be updated manually and only from the Instance Server SDK")
		}
		return efm.storageManager.MergeMetadata(instance, metadata)
	})
	if err != nil {
		return fmt.Errorf("failed to update instance info in db: %s", err.Error())
	}
	return nil
}

// Delete removes an instance session from the database.
func (efm *EdgegapFleetManager) Delete(ctx context.Context, id string) error {
	_, err := efm.edgegapManager.StopDeployment(id)
	if err != nil {
		return err
	}
	return efm.storageManager.deleteDbInstance(ctx, []string{id})
}

func (efm *EdgegapFleetManager) syncInstancesWorker() {
	deleteTerminatedInstancesFn := func() {
		deployments, err := efm.edgegapManager.ListAllDeployments()
		if err != nil {
			efm.logger.WithField("error", err.Error()).Error("failed to list edgegap deployments")
			return
		}
		efm.logger.WithField("active_deployments", len(deployments)).Debug("fetched active deployment instances list")
		efm.nk.MetricsGaugeSet("edgegap_deployment_count", nil, float64(len(deployments)))

		dbInstances, err := efm.storageManager.listDbInstances(efm.ctx)
		if err != nil {
			efm.logger.WithField("error", err.Error()).Error("failed to read instances from db")
			return
		}

		activeInstancesMap := make(map[string]struct{}, len(deployments))
		for _, i := range deployments {
			if i.Status != DeploymentStatusError {
				activeInstancesMap[i.RequestId] = struct{}{}
			}
		}

		instancesToRemove := make([]string, 0)
		for _, dbInfo := range dbInstances {
			if _, ok := activeInstancesMap[dbInfo.Id]; !ok {
				instancesToRemove = append(instancesToRemove, dbInfo.Id)
			}
		}

		if len(instancesToRemove) > 0 {
			efm.logger.Debug("Found %d instances to remove", len(instancesToRemove))

			if err = efm.storageManager.deleteDbInstance(efm.ctx, instancesToRemove); err != nil {
				efm.logger.WithField("error", err.Error()).Error("failed to delete a game instances")
				return
			}
		}
	}

	duration := efm.edgegapManager.configuration.PollingInterval
	if duration <= 0 {
		efm.logger.WithField("duration", duration).Info("skipping polling scheduler: polling_interval set to 0")
		return
	}

	deleteTerminatedInstancesFn()

	t := time.NewTicker(duration)
	defer t.Stop()
	efm.logger.Info("Starting Instance Worker for Sync every %s", duration.String())
	for {
		select {
		case <-efm.ctx.Done():
			return
		case <-t.C:
			deleteTerminatedInstancesFn()
		}
	}
}

func (efm *EdgegapFleetManager) runCleanupScheduler() {
	reservationMaxDuration := efm.edgegapManager.configuration.ReservationMaxDuration

	cleanupFn := func() {
		// Remove the Max Duration to get the expired timestamp of reservations
		searchTime := time.Now().UTC().Add(-reservationMaxDuration)
		query := fmt.Sprintf(`+value.metadata.edgegap.reservations_count:>0 +value.metadata.edgegap.reservations_updated_at:<"%s"`, searchTime.Format(time.RFC3339))
		cursor := ""
		entries, _, err := efm.nk.StorageIndexList(efm.ctx, "", efm.storageManager.index, query, 1_000, nil, cursor)
		if err != nil {
			efm.logger.WithField("error", err.Error()).Error("failed to list expired reservations instance")
			return
		}

		if objects := entries.GetObjects(); len(objects) > 0 {
			efm.logger.Debug("Found %d Reservations Instance to cleanup", len(objects))
			for _, so := range objects {
				// Expiry is re-checked on the record as written, so a concurrent Join is not lost.
				_, err := efm.storageManager.MutateInstance(efm.ctx, so.Key, func(info *runtime.InstanceInfo) error {
					return efm.storageManager.ExpireReservations(info, searchTime)
				})
				if err != nil {
					efm.logger.WithFields(map[string]any{"instance_id": so.Key, "error": err.Error()}).Error("failed to update expired reservations instance")
				}
			}
		}
	}

	duration := efm.edgegapManager.configuration.CleanupInterval
	if duration <= 0 {
		efm.logger.WithField("duration", duration).Info("Skipping cleanup scheduler: cleanup_interval set to 0")
		return
	}

	cleanupFn()

	t := time.NewTicker(duration)
	defer t.Stop()

	efm.logger.Info("Starting cleanup scheduler every %s", duration.String())
	for {
		select {
		case <-efm.ctx.Done():
			return
		case <-t.C:
			cleanupFn()
		}
	}
}
