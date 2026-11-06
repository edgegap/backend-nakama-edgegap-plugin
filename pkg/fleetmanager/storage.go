package fleetmanager

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/edgegap/nakama-edgegap/internal/helpers"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

var (
	// ErrorNoVersionFound is returned when no Edgegap version is found in storage
	ErrorNoVersionFound        = errors.New("no Edgegap version found in storage")
	ErrInstanceNotFound        = errors.New("instance not found")
	ErrReservationLimitReached = errors.New("max players reservation limit reached")

	// errInstanceUnchanged lets a mutation skip the write; MutateInstance then returns the record as read.
	errInstanceUnchanged = errors.New("instance unchanged")
)

const StorageCollectionEdgegapVersion = "system"

// Storage names are scoped to the namespace the Fleet Manager was registered with.
var (
	StorageEdgegapIndex = func(namespace string) string {
		return namespaced(namespace, "_edgegap_instances_idx", "instances_idx")
	}
	StorageEdgegapInstancesCollection = func(namespace string) string {
		return namespaced(namespace, "_edgegap_instances", "instances")
	}
	StorageKeyEdgegapVersion = func(namespace string) string { return namespaced(namespace, "edgegap_version", "version") }
)

// namespaced returns defaultName for the default (empty) namespace, so a single Fleet Manager keeps its
// existing names, and otherwise prefixes name with "edgegap_<namespace>_". Names are lowercased to match
// how Nakama registers and resolves RPC IDs.
func namespaced(namespace, defaultName, name string) string {
	if namespace == "" {
		return defaultName
	}
	return strings.ToLower("edgegap_" + namespace + "_" + name)
}

// Constants representing different statuses of an Edgegap instance
const (
	EdgegapStatusRequested  = "REQUESTED"
	EdgegapStatusRunning    = "RUNNING"
	EdgegapStatusReady      = "READY"
	EdgegapStatusStopping   = "STOPPING"
	EdgegapStatusError      = "ERROR"
	EdgegapStatusUnknown    = "UNKNOWN"
	EdgegapStatusTerminated = "TERMINATED"
)

// StorageManager handles interactions with Nakama's storage system
type StorageManager struct {
	nk           runtime.NakamaModule
	logger       runtime.Logger
	collection   string
	index        string
	namespaceKey string
}

// NewStorageManager creates a new StorageManager instance
func NewStorageManager(nk runtime.NakamaModule, logger runtime.Logger) *StorageManager {
	sm := &StorageManager{
		nk:     nk,
		logger: logger,
	}
	sm.setNamespace("")
	return sm
}

// setNamespace scopes the storage collection, index and version key to the Fleet Manager namespace.
func (sm *StorageManager) setNamespace(namespace string) {
	sm.collection = StorageEdgegapInstancesCollection(namespace)
	sm.index = StorageEdgegapIndex(namespace)
	sm.namespaceKey = StorageKeyEdgegapVersion(namespace)
}

// ExtractEdgegapInstance extracts Edgegap-related data from an instance's metadata.
func (sm *StorageManager) ExtractEdgegapInstance(instance *runtime.InstanceInfo) (*EdgegapInstanceInfo, error) {
	// Check if metadata contains "edgegap" key
	value, ok := instance.Metadata["edgegap"]
	if !ok {
		return nil, errors.New("edgegap key not in metadata")
	}

	// Convert metadata to JSON and unmarshal into EdgegapInstanceInfo struct
	valueString, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}

	var edgegapInstance EdgegapInstanceInfo
	if err := json.Unmarshal(valueString, &edgegapInstance); err != nil {
		return nil, err
	}

	return &edgegapInstance, nil
}

// SyncInstance synchronizes Edgegap instance metadata, including player count and available seats.
func (sm *StorageManager) SyncInstance(instance *runtime.InstanceInfo) error {
	// Extract Edgegap instance information
	edgegapInstance, err := sm.ExtractEdgegapInstance(instance)
	if err != nil {
		sm.logger.Error("Error extracting edgegap instance: %v", err)
		return err
	}

	// Get available seats in the session
	availableSeat, err := sm.GetAvailableSeat(instance)
	if err != nil {
		return err
	}

	// Keep exactly one reservation time per reserved user, and expose the oldest one as
	// reservations_updated_at so the cleanup index query finds every instance with an expired seat.
	reservationTimes := make(map[string]time.Time, len(edgegapInstance.Reservations))
	var oldest time.Time
	for _, userId := range edgegapInstance.Reservations {
		t := edgegapInstance.reservedAt(userId).UTC()
		reservationTimes[userId] = t
		if oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
	}
	edgegapInstance.ReservationTimes = reservationTimes
	if !oldest.IsZero() {
		edgegapInstance.ReservationsUpdatedAt = oldest
	}

	// Update player count and available seats
	instance.PlayerCount = len(edgegapInstance.Connections)
	edgegapInstance.AvailableSeats = availableSeat
	edgegapInstance.ReservationsCount = len(edgegapInstance.Reservations)

	// Save updated metadata back into the instance
	instance.Metadata["edgegap"] = edgegapInstance

	return nil
}

// GetAvailableSeat calculates the number of available seats in an instance.
func (sm *StorageManager) GetAvailableSeat(instance *runtime.InstanceInfo) (int, error) {
	edgegapInstance, err := sm.ExtractEdgegapInstance(instance)
	if err != nil {
		return 0, err
	}

	// Calculate available seats based on max players and reservations
	if edgegapInstance.MaxPlayers > 0 {
		return edgegapInstance.MaxPlayers - len(edgegapInstance.Reservations) - len(edgegapInstance.Connections), nil
	}

	// Return -1 if maxPlayers is not set
	return -1, nil
}

// WriteEdgegapVersion stores the Edgegap version in storage
func (sm *StorageManager) WriteEdgegapVersion(ctx context.Context, version string) error {
	versionData := map[string]interface{}{
		"version":    version,
		"updated_at": time.Now().Unix(),
	}

	versionDataBytes, err := json.Marshal(versionData)
	if err != nil {
		return err
	}

	if _, err := sm.nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      StorageCollectionEdgegapVersion,
			Key:             sm.namespaceKey,
			Value:           string(versionDataBytes),
			PermissionRead:  2, // Public read
			PermissionWrite: 0, // No write from clients
		},
	}); err != nil {
		return err
	}

	return nil
}

// ReadEdgegapVersion retrieves the Edgegap version from storage
func (sm *StorageManager) ReadEdgegapVersion(ctx context.Context) (string, int64, error) {
	objects, err := sm.nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: StorageCollectionEdgegapVersion,
			Key:        sm.namespaceKey,
		},
	})

	if err != nil {
		return "", 0, err
	}

	if len(objects) == 0 {
		return "", 0, ErrorNoVersionFound
	}

	// Parse stored version
	var storedData map[string]interface{}
	if err := json.Unmarshal([]byte(objects[0].Value), &storedData); err != nil {
		return "", 0, err
	}

	version, ok := storedData["version"].(string)
	if !ok || version == "" {
		return "", 0, errors.New("invalid Edgegap version format in storage")
	}

	var updatedAt int64
	if timestamp, ok := storedData["updated_at"].(float64); ok {
		updatedAt = int64(timestamp)
	}

	return version, updatedAt, nil
}

// createDbInstance creates and stores a new instance in the database.
func (sm *StorageManager) createDbInstance(ctx context.Context, id string, maxPlayers int, userIds []string, callbackId string, metadata map[string]any) (*runtime.InstanceInfo, error) {
	// Initialize metadata if nil
	if metadata == nil {
		metadata = make(map[string]any)
	}

	now := time.Now().UTC()
	reservationTimes := make(map[string]time.Time, len(userIds))
	for _, userId := range userIds {
		reservationTimes[userId] = now
	}

	// Store Edgegap-related information in metadata
	metadata["edgegap"] = EdgegapInstanceInfo{
		MaxPlayers:            maxPlayers,
		Reservations:          userIds,
		ReservationsUpdatedAt: now,
		ReservationTimes:      reservationTimes,
		CallbackId:            callbackId,
		Connections:           []string{},
	}

	// Create a new instance session instance
	instance := &runtime.InstanceInfo{
		Id:             id,
		ConnectionInfo: nil,
		CreateTime:     time.Now(),
		PlayerCount:    0,
		Status:         EdgegapStatusRequested,
		Metadata:       metadata,
	}

	// Synchronize instance before storing
	err := sm.SyncInstance(instance)
	if err != nil {
		return nil, err
	}

	// Serialize instance to JSON and store in Nakama
	value, err := json.Marshal(instance)
	if err != nil {
		return nil, err
	}

	sw := runtime.StorageWrite{
		Collection: sm.collection,
		Key:        id,
		UserID:     "",
		Value:      string(value),
	}

	_, err = sm.nk.StorageWrite(ctx, []*runtime.StorageWrite{&sw})
	return instance, err
}

// listDbInstances retrieves all stored instance from Nakama.
func (sm *StorageManager) listDbInstances(ctx context.Context) ([]*runtime.InstanceInfo, error) {
	instances := make([]*runtime.InstanceInfo, 0)
	cursor := ""

	// Loop to fetch sessions in batches
	for {
		objects, nextCursor, err := sm.nk.StorageList(ctx, "", "", sm.collection, 1_000, cursor)
		if err != nil {
			return nil, err
		}

		// Deserialize each stored object into an instance
		for _, obj := range objects {
			var info *runtime.InstanceInfo
			if err = json.Unmarshal([]byte(obj.Value), &info); err != nil {
				return nil, err
			}
			instances = append(instances, info)
		}

		// Stop if no more results
		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	return instances, nil
}

// getDbInstance retrieves a single instance by ID from the Nakama database.
func (sm *StorageManager) getDbInstance(ctx context.Context, id string) (*runtime.InstanceInfo, error) {
	objects, err := sm.nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: sm.collection,
		Key:        id,
	}})
	if err != nil {
		return nil, err
	}

	// If no session is found, return nil
	if len(objects) == 0 {
		return nil, nil
	}

	obj := objects[0]

	// Deserialize stored JSON into an instance
	var instance *runtime.InstanceInfo
	if err = json.Unmarshal([]byte(obj.Value), &instance); err != nil {
		return nil, err
	}

	return instance, nil
}

// deleteDbInstance removes instance from Nakama storage.
func (sm *StorageManager) deleteDbInstance(ctx context.Context, ids []string) error {
	deletes := make([]*runtime.StorageDelete, 0, len(ids))

	// Prepare delete requests for each session ID
	for _, id := range ids {
		deletes = append(deletes, &runtime.StorageDelete{
			Collection: sm.collection,
			Key:        id,
		})
	}

	// Execute delete operation
	if err := sm.nk.StorageDelete(ctx, deletes); err != nil {
		return err
	}

	return nil
}

// getUserIPs retrieves player IP addresses from their metadata.
func (sm *StorageManager) getUserIPs(ctx context.Context, userIds []string) ([]string, error) {
	userIps := make([]string, 0)

	// Iterate through user IDs and fetch their metadata
	for _, userId := range userIds {
		userAccount, err := sm.nk.AccountGetId(ctx, userId)
		if err != nil {
			return nil, err
		}

		// Parse user metadata from JSON
		userMetadata := make(map[string]interface{})
		err = json.Unmarshal([]byte(userAccount.User.Metadata), &userMetadata)
		if err != nil {
			return nil, err
		}

		// Extract IP address if available
		userIp, ok := userMetadata["PlayerIp"]
		if !ok {
			sm.logger.Warn("User %s metadata does not contain PlayerIp", userId)
			continue
		}
		if userIp != "" {
			userIps = append(userIps, userIp.(string))
		}
	}

	return userIps, nil
}

// instanceWriteMaxRetries bounds the retries of MutateInstance on concurrent writes.
const instanceWriteMaxRetries = 5

// MutateInstance mutates the stored record and writes it back with the version it was read
// at. On a concurrent write the record is re-read and mutate runs again on the fresh copy.
func (sm *StorageManager) MutateInstance(ctx context.Context, id string, mutate func(instance *runtime.InstanceInfo) error) (*runtime.InstanceInfo, error) {
	var result *runtime.InstanceInfo
	reads := []*runtime.StorageRead{{Collection: sm.collection, Key: id}}
	_, err := sm.nk.StorageWriteRetry(ctx, reads, func(objects []*api.StorageObject) ([]*runtime.StorageWrite, error) {
		if len(objects) == 0 {
			return nil, ErrInstanceNotFound
		}

		var instance *runtime.InstanceInfo
		if err := json.Unmarshal([]byte(objects[0].Value), &instance); err != nil {
			return nil, err
		}
		result = instance

		if err := mutate(instance); err != nil {
			return nil, err
		}
		if err := sm.SyncInstance(instance); err != nil {
			return nil, err
		}

		value, err := json.Marshal(instance)
		if err != nil {
			return nil, err
		}
		return []*runtime.StorageWrite{{
			Collection: sm.collection,
			Key:        id,
			UserID:     "",
			Value:      string(value),
			Version:    objects[0].Version,
		}}, nil
	}, instanceWriteMaxRetries)
	if errors.Is(err, errInstanceUnchanged) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ReserveSeats adds userIds to the reservations when they fit, and renews the reservation of users
// already holding one. An unlimited instance (max_players <= 0) seats everyone without a write.
func (sm *StorageManager) ReserveSeats(instance *runtime.InstanceInfo, userIds []string) error {
	edgegapInstance, err := sm.ExtractEdgegapInstance(instance)
	if err != nil {
		return errors.New("error extracting Edgegap instance")
	}

	if edgegapInstance.MaxPlayers <= 0 {
		return errInstanceUnchanged
	}

	// Only users without a reservation take up a new seat.
	newSeats := make([]string, 0, len(userIds))
	for _, userId := range userIds {
		if !slices.Contains(edgegapInstance.Reservations, userId) {
			newSeats = helpers.AppendIfNotExists(newSeats, userId)
		}
	}
	if instance.PlayerCount+len(edgegapInstance.Reservations)+len(newSeats) > edgegapInstance.MaxPlayers {
		return ErrReservationLimitReached
	}

	now := time.Now().UTC()
	if edgegapInstance.ReservationTimes == nil {
		edgegapInstance.ReservationTimes = make(map[string]time.Time, len(userIds))
	}
	for _, userId := range userIds {
		edgegapInstance.Reservations = helpers.AppendIfNotExists(edgegapInstance.Reservations, userId)
		edgegapInstance.ReservationTimes[userId] = now
	}
	instance.Metadata["edgegap"] = edgegapInstance
	return nil
}

// ReleaseSeats drops the reservations held for userIds. Connected players are no longer reserved, so
// passing them is harmless.
func (sm *StorageManager) ReleaseSeats(instance *runtime.InstanceInfo, userIds []string) error {
	edgegapInstance, err := sm.ExtractEdgegapInstance(instance)
	if err != nil {
		return errors.New("error extracting Edgegap instance")
	}

	remaining := helpers.RemoveElements(edgegapInstance.Reservations, userIds)
	if len(remaining) == len(edgegapInstance.Reservations) {
		return errInstanceUnchanged
	}
	edgegapInstance.Reservations = remaining
	instance.Metadata["edgegap"] = edgegapInstance
	return nil
}

// MergeMetadata merges metadata into the instance metadata. The edgegap key is owned by the fleet
// manager and is never overwritten.
func (sm *StorageManager) MergeMetadata(instance *runtime.InstanceInfo, metadata map[string]any) error {
	if len(metadata) == 0 {
		return errInstanceUnchanged
	}
	merged := helpers.MergeMaps(instance.Metadata, metadata)
	if edgegapInstance, ok := instance.Metadata["edgegap"]; ok {
		merged["edgegap"] = edgegapInstance
	} else {
		delete(merged, "edgegap")
	}
	instance.Metadata = merged
	return nil
}

// ApplyConnectionReport is the game server's heartbeat: reported users move from the reservations to
// the connections.
func (sm *StorageManager) ApplyConnectionReport(instance *runtime.InstanceInfo, connections []string) error {
	edgegapInstance, err := sm.ExtractEdgegapInstance(instance)
	if err != nil {
		return err
	}

	// The report does not renew the remaining reservations: a no-show still expires on its own reservation time.
	edgegapInstance.Reservations = helpers.RemoveElements(edgegapInstance.Reservations, connections)
	edgegapInstance.Connections = connections
	instance.Metadata["edgegap"] = edgegapInstance
	return nil
}

// ExpireReservations drops the reservations made before cutoff. While the instance is still Requested
// or Running the seats are kept and renewed instead, so they aren't removed if the instance takes
// longer than the reservation duration to be Ready, and players get time to join once it is.
func (sm *StorageManager) ExpireReservations(instance *runtime.InstanceInfo, cutoff time.Time) error {
	edgegapInstance, err := sm.ExtractEdgegapInstance(instance)
	if err != nil {
		return err
	}

	expired := make([]string, 0)
	for _, userId := range edgegapInstance.Reservations {
		if edgegapInstance.reservedAt(userId).Before(cutoff) {
			expired = append(expired, userId)
		}
	}
	if len(expired) == 0 {
		return errInstanceUnchanged
	}

	if instance.Status == EdgegapStatusRequested || instance.Status == EdgegapStatusRunning {
		now := time.Now().UTC()
		if edgegapInstance.ReservationTimes == nil {
			edgegapInstance.ReservationTimes = make(map[string]time.Time, len(expired))
		}
		for _, userId := range expired {
			edgegapInstance.ReservationTimes[userId] = now
		}
	} else {
		edgegapInstance.Reservations = helpers.RemoveElements(edgegapInstance.Reservations, expired)
	}
	instance.Metadata["edgegap"] = edgegapInstance
	return nil
}

// ApplyDeploymentReady is Edgegap's ready webhook: the deployment is RUNNING at the reported address,
// and its location replaces the site the create wrote.
func (sm *StorageManager) ApplyDeploymentReady(instance *runtime.InstanceInfo, deployment *EdgegapDeploymentStatus, portName string, logger runtime.Logger) error {
	// A late webhook must not move an instance the game server already reported on back to RUNNING.
	if instance.Status == EdgegapStatusRequested {
		instance.Status = EdgegapStatusRunning
	}
	instance.ConnectionInfo = &runtime.ConnectionInfo{
		IpAddress: deployment.PublicIp,
		DnsName:   deployment.Fqdn,
		Port:      deployment.Ports[portName].External,
	}

	if instance.Metadata == nil {
		instance.Metadata = map[string]any{}
	}
	if deployment.Location.Latitude != 0 || deployment.Location.Longitude != 0 {
		instance.Metadata["site"] = map[string]any{
			"city":      deployment.Location.City,
			"latitude":  deployment.Location.Latitude,
			"longitude": deployment.Location.Longitude,
		}
	} else {
		logger.Warn("Edgegap deployment ready #%s without a location, keeping the existing site metadata", deployment.RequestId)
	}
	return nil
}
