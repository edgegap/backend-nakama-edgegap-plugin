# Nakama <> Edgegap Integration

Deploy Dedicated Game Servers for popular engines (e.g., Unity, Unreal, etc.) or custom engine servers, fully integrated with Nakama's open-source player data and game services for a convenient turnkey solution.

## Edgegap Setup

To prepare your dedicated game server build for deployment on Edgegap, see:
- [Getting Started with Servers (Unity)](https://docs.edgegap.com/learn/unity-games/getting-started-with-servers)
- [Getting Started with Servers (Unreal Engine)](https://docs.edgegap.com/learn/unreal-engine-games/getting-started-with-servers)

## Installation 

To add the Edgegap Fleet Manager to your Nakama go plugin project, use the following command:
```shell
go get github.com/edgegap/nakama-edgegap
```

This will add it to your project's `go.mod` dependencies.

## Configuration

The Fleet Manager is configured with an `EdgegapConfig` struct. Create it with `NewEdgegapConfig()`, which takes the required values and sets the defaults of the optional ones:

```go
cfg := fleetmanager.NewEdgegapConfig(apiUrl, apiToken, application, portName, nakamaAccessUrl)
cfg.InitialVersion = "v1.0.0"            // optional
cfg.ReservationMaxDuration = time.Minute // optional
```

| Field | Description | Default |
|---|---|---|
| `ApiUrl` | Edgegap API URL, e.g. `https://api.edgegap.com` | required |
| `ApiToken` | Edgegap API token, keep the `token` prefix | required |
| `Application` | Edgegap application to deploy | required |
| `PortName` | Edgegap application port name to send to game clients | required |
| `NakamaAccessUrl` | Nakama API URL that deployments call back to, must be prefixed with `https://`. For Heroic Cloud, it is provided when you create your instance | required |
| `InitialVersion` | Version to store when no version exists in storage, see [Version Management](#version-management) | empty |
| `PollingInterval` | Interval of the sync with the Edgegap API, in case of mismatch | `15m` |
| `CleanupInterval` | Interval of the reservation expiry check | `1m` |
| `ReservationMaxDuration` | Duration of each seat reservation before it expires, see [Seat Reservations](#seat-reservations) | `30s` |

Setting `PollingInterval` or `CleanupInterval` to 0 disables the corresponding background worker entirely. This can be useful for testing purposes but is not recommended for production setting.

`NewEdgegapFleetManager` validates the configuration, including that the application exists on Edgegap, and returns an error if it is invalid.

### Environment Variables

Reading the configuration is up to your plugin. The `main.go` of this project reads it from these environment variables of your Nakama cluster:

```shell
EDGEGAP_API_URL=https://api.edgegap.com
EDGEGAP_API_TOKEN=<The Edgegap's API Token (keep the 'token' in the API Token)>
EDGEGAP_APPLICATION=<The Edgegap's Application Name to use to deploy>
EDGEGAP_PORT_NAME=<The Edgegap's Application Port Name to send to game client>
NAKAMA_ACCESS_URL=<Nakama API Url, for Heroic Cloud, it will be provided when you create your instance>
```

Optional values:
```shell
INITIAL_EDGEGAP_VERSION=<Initial version to use when no version exists in storage, falls back to EDGEGAP_VERSION>
EDGEGAP_POLLING_INTERVAL=<PollingInterval, empty to disable (default: 15m)>
NAKAMA_CLEANUP_INTERVAL=<CleanupInterval, empty to disable (default: 1m)>
NAKAMA_RESERVATION_MAX_DURATION=<ReservationMaxDuration (default: 30s)>
```

You can copy the `local.yml.example` to `local.yml` and fill it out to start with your local cluster.

### Version Management

The plugin stores deployment versions in Nakama's storage, allowing you to update the Edgegap deployment version at runtime without restarting services. The version can be updated via Server-to-Server (S2S) RPCs.

The version is stored in the `system` collection under the key `edgegap_version`, or `edgegap_<namespace>_version` for a [namespaced Fleet Manager](#namespace).

On startup, if no version exists in storage and `INITIAL_EDGEGAP_VERSION` is set, the plugin will automatically store this initial version for immediate use

#### Update Version (S2S only)
Updates the Edgegap deployment version after validating it exists in the Edgegap application.

```bash
curl -X POST http://localhost:7350/v2/rpc/update_edgegap_version?http_key=<http-key>&unwrap \
  -H "Content-Type: application/json" \
  -d '{"version": "your-version-here"}'
```

Success Response:
```json
{
  "success": true,
  "version": "your-version-here",
  "message": "Edgegap version updated successfully. Will be used for new deployments immediately."
}
```

Error Response (invalid version):
```json
{
  "code": 5,
  "message": "version 'invalid-version' does not exist for application 'YourApp'"
}
```

#### Get Current Version (S2S only)
```bash
curl -X POST http://localhost:7350/v2/rpc/get_edgegap_version?http_key=<http-key>&unwrap \
  -H "Content-Type: application/json" \
  -d '{}'
```

Response:
```json
{
  "version": "your-version-here",
  "source": "dynamic",
  "updated_at": 1735844393
}
```

Response (no version set):
```json
{
  "error": "No Edgegap version configured",
  "message": "Please set version using update_edgegap_version RPC"
}
```

**Note**: Both RPCs require HTTP key authentication and cannot be called by game clients.

### Per-Deployment Overrides

The `metadata` passed to `Create` can override the global configuration for a single deployment:

- `edgegap_version` - the application version to deploy instead of the version in storage,
- `edgegap_application` - the Edgegap application to deploy instead of the configured `Application`.

## Usage

From your `main.go` where the `InitModule` global function is, you need to register the Fleet Manager

```go
// Register the Fleet Manager
cfg := fleetmanager.NewEdgegapConfig(apiUrl, apiToken, application, portName, nakamaAccessUrl)
efm, err := fleetmanager.NewEdgegapFleetManager(ctx, logger, db, nk, cfg)
if err != nil {
    return err
}

if err = initializer.RegisterFleetManager(efm); err != nil {
    logger.WithField("error", err).Error("failed to register Edgegap fleet manager")
    return err
}
```

Once registered, it can be retrieved throughout your plugin code with `nk.GetFleetManager()`.

The plugin must be built with the same `nakama-common` version as the Nakama server that loads it. This version of the Fleet Manager requires Nakama 3.42.0 or later.

### Multiple Fleet Managers

To use more than one Edgegap Fleet Manager from the same Nakama deployment, create each one with its own [configuration](#configuration), e.g. a different Edgegap application, and register it under a distinct name using `RegisterFleetManagerByName()`. The name is used as the Fleet Manager's [namespace](#namespace): it scopes its RPC IDs, storage collection, storage index and deployment version.

```go
efmEu, err := fleetmanager.NewEdgegapFleetManager(ctx, logger, db, nk, fleetmanager.NewEdgegapConfig(apiUrl, apiToken, "my-game-eu", portName, nakamaAccessUrl))
if err != nil {
    return err
}
efmUs, err := fleetmanager.NewEdgegapFleetManager(ctx, logger, db, nk, fleetmanager.NewEdgegapConfig(apiUrl, apiToken, "my-game-us", portName, nakamaAccessUrl))
if err != nil {
    return err
}

if err = initializer.RegisterFleetManagerByName("eu", efmEu); err != nil {
    return err
}
if err = initializer.RegisterFleetManagerByName("us", efmUs); err != nil {
    return err
}
```

A Fleet Manager registered by name is retrieved with `nk.GetFleetManagerByName("eu")`.

### Namespace

A Fleet Manager registered with `RegisterFleetManager()` has an empty namespace and uses the names below unprefixed. A Fleet Manager registered with `RegisterFleetManagerByName()` prefixes them with `edgegap_<namespace>_`, where `<namespace>` is the name it was registered with. Namespaced names are always lowercase, so a Fleet Manager registered as `EU` exposes `edgegap_eu_instance_create`.

| Name | Default namespace | Namespace `eu` |
|---|---|---|
| Storage collection | `_edgegap_instances` | `edgegap_eu_instances` |
| Storage index | `_edgegap_instances_idx` | `edgegap_eu_instances_idx` |
| Version storage key | `edgegap_version` | `edgegap_eu_version` |
| [Client RPCs](#game-client---nakama-optional-rpc) | `instance_create`, `instance_get`, `instance_list`, `instance_join` | `edgegap_eu_instance_create`, `edgegap_eu_instance_get`, ... |
| [Version RPCs](#version-management) | `update_edgegap_version`, `get_edgegap_version` | `edgegap_eu_update_version`, `edgegap_eu_get_version` |
| Edgegap webhooks | `edgegap_deployment_ready`, `edgegap_deployment_error`, `edgegap_deployment_terminated` | `edgegap_eu_deployment_ready`, ... |
| [Game server events](#dedicated-game-server---nakama-instance) | `edgegap_connection`, `edgegap_instance` | `edgegap_eu_connection`, `edgegap_eu_instance` |

The Edgegap webhook and game server event URLs are passed to each deployment, so they don't need to be configured.

### Upgrading from Nakama 3.41 or earlier

`NewEdgegapFleetManager` no longer takes the `initializer` and no longer reads environment variables: it takes an [`EdgegapConfig`](#configuration) instead. To keep using the same environment variables, copy `edgegapConfigFromEnv` from this project's `main.go`. The RPC IDs and storage names of a Fleet Manager registered with `RegisterFleetManager()` are unchanged.

You can use the `main.go` from this project and also copy the `local.yml.example` to start a local Nakama using docker compose.

copy `docker-compose.yml` and `Dockerfile` to the root of your project and run the following command to start a local cluster:
```shell
docker compose up --build -d
```

Run the following command to stop it
```shell
docker compose down
```

## Instances and Seat Reservations

Every Edgegap deployment is tracked as a Nakama Instance, stored in the `_edgegap_instances` collection ([namespaced](#namespace)) and indexed so it can be searched with `List`.

### Instance Status

| Status | Set when |
|---|---|
| `REQUESTED` | the deployment was requested from Edgegap |
| `RUNNING` | Edgegap reports the deployment is ready; the connection info (IP, FQDN, port) is now available |
| `READY` | the Dedicated Game Server sent the `READY` instance event; the create callback fires |
| `STOPPING` | the Dedicated Game Server sent the `STOP` instance event |
| `ERROR` | Edgegap reported a deployment error, or the Dedicated Game Server sent the `ERROR` instance event |
| `TERMINATED` | Edgegap reports the deployment was terminated; the instance is removed on the next sync |

Edgegap's ready webhook only moves a `REQUESTED` instance to `RUNNING`, so a late webhook can't move a `READY` instance back to `RUNNING`.

When Edgegap's ready webhook includes the deployment location, it is stored in the instance metadata as `site`:

```json
{ "site": { "city": "Montreal", "latitude": 45.5, "longitude": -73.6 } }
```

### Seat Reservations

The Edgegap state of an instance lives under `metadata.edgegap`:

| Field | Description |
|---|---|
| `max_players` | seat capacity; `0` or less means unlimited |
| `reservations` | user IDs holding a seat who have not connected yet |
| `reservation_times` | when each user's seat was reserved |
| `reservations_updated_at` | time of the oldest outstanding reservation |
| `reservations_count` | number of reservations |
| `connections` | user IDs connected to the Dedicated Game Server, as last reported |
| `available_seats` | `max_players - reservations - connections`, or `-1` when unlimited |

`Create` reserves a seat for each user it is given, and `Join` reserves seats on an existing instance:

- a user who already holds a reservation can join again without taking an extra seat, and their reservation is renewed,
- `Join` fails with `max players reservation limit reached` when the new users don't fit,
- on an unlimited instance `Join` always succeeds and no reservations are tracked.

A reservation ends when:

- the user appears in a [Connection Event](#connection-events), moving them from `reservations` to `connections`,
- the reservation is older than the configured `ReservationMaxDuration`, checked every `CleanupInterval`. Connection events do not renew the remaining reservations, so a player who never connects frees their seat even while the server keeps reporting. While the instance is still `REQUESTED` or `RUNNING`, expired reservations are renewed instead of dropped, so players don't lose their seat when the server takes longer to start,
- the seat is released with [`Leave`](#additional-fleet-manager-methods).

All instance updates use versioned storage writes, so concurrent joins, connection events and cleanups can't overwrite each other.

### Additional Fleet Manager Methods

Besides the `runtime.FleetManager` interface, `EdgegapFleetManager` provides:

- `Leave(ctx, id, userIds)` - releases the reservations held by `userIds`, e.g. when a party abandons a server before connecting. Unknown instances are ignored,
- `Update(ctx, id, playerCount, metadata)` - merges `metadata` into the instance metadata; the `edgegap` key can't be overwritten. `playerCount` is not stored, because the player count always comes from the connection events,
- `Delete(ctx, id)` - stops the Edgegap deployment and removes the instance.

Use the `*EdgegapFleetManager` returned by `NewEdgegapFleetManager`, or type-assert the registered fleet manager (use `GetFleetManagerByName()` for a namespaced one):

```go
efm, ok := nk.GetFleetManager().(*fleetmanager.EdgegapFleetManager)
if !ok {
    return errors.New("edgegap fleet manager not registered")
}
if err := efm.Leave(ctx, instanceId, userIds); err != nil {
    return err
}
```

## Server Placement

Game clients only interact with Edgegap APIs through Nakama RPCs, defaulting to [Nakama authentication method of your choice](https://heroiclabs.com/docs/nakama/concepts/authentication/). [Edgegap's Server Placement utilizing Server Score strategy](https://docs.edgegap.com/learn/advanced-features/deployments#1-server-score-strategy-best-practice) uses public IP addresses of participating players to choose the optimal server location. To store the player IP address and pass it to Edgegap when looking for server, store player's public IP in their Profile's Metadata as `PlayerIp`. Users without one are left out of placement; if none of the users has one, the IP address of the caller is used.

In your `main.go`, during the Init you can add the Registration of the Authentication of the type you implemented

```go
    // Register Authentication Methods
    if err := initializer.RegisterAfterAuthenticateCustom(fleetmanager.OnAuthenticateUpdateCustom); err != nil {
        return err
    }
```

This will automatically store in Profile's Metadata the `PlayerIp`

## Dedicated Game Server -> Nakama Instance

When using this integration, every Deployment (Dedicated Game Server) made through Edgegap's platform will have many Environment Variables
injected.

It's the responsibility of the Dedicated Game Server to fire lifecycle events when specific actions are triggered to communicate
back to Nakama's cluster changes regarding the Instance (Nakama's reference to an Edgegap Deployment) and facilitate Player connections to
the Dedicated Game Server.

### Unity Server Plugin

Automate all server responsibilities (instance and connection event reporting) by using our
[Edgegap Server Nakama Plugin for Unity](https://github.com/edgegap/edgegap-server-nakama-plugin-unity).

### Injected Environment Variables

The following Environment Variables will be available in the Dedicated Game Server:

- `NAKAMA_CONNECTION_EVENT_URL` (url to send connection events of the players)
- `NAKAMA_INSTANCE_EVENT_URL` (url to send instance event actions)
- `NAKAMA_INSTANCE_METADATA` (contains create metadata JSON)
- `NAKAMA_EDGEGAP_NAMESPACE` (the [namespace](#namespace) of the Fleet Manager that created the deployment, empty by default)

### Connection Events

Using `NAKAMA_CONNECTION_EVENT_URL` you must send Player Connection events to the Nakama Instance with the following body:

```json
{
  "instance_id": "<instance_id>",
  "connections": [
    "<user_id>"
  ]
}
```

`connections` is the list of active user IDs connected to the Dedicated Game Server. We recommend collecting updates
over a short period of time (~5 seconds) and updating the full list of connections in a batch request. Contents of
this request will overwrite any existing list of connections for the specified instance, and release the seat reservations of
the listed users. The instance `player_count` is the number of connections.

### Instance Events

Using `NAKAMA_INSTANCE_EVENT_URL` you must send Instance events to the Nakama Instance with the following body:

```json
{
  "instance_id": "<instance_id>",
  "action": "[READY|ERROR|STOP]",
  "message": "",
  "metadata": {}
}
```

`action` must be one of the following:

- `READY` will mark the instance as ready and trigger Nakama callback event to notify players,
- `ERROR` will mark the instance in error and trigger Nakama callback event to notify players,
- `STOP` will call Edgegap's API to stop the running deployment, which will be removed from Nakama once Edgegap confirms termination.

`message` can be used optionally to provide extra Instance status information (e.g. to communicate Errors).

`metadata` can be used optionally to merge additional custom key-value information available in Dedicated Game Server to the metadata of the Instance.

## Game Client -> Nakama (optional rpc)

We included a Client RPC route to do basic operations on Instance - listing, creating, and joining. Consider this an optional starter code sample.
The RPC IDs below are for the default namespace; a [namespaced Fleet Manager](#namespace) registers them as `edgegap_<namespace>_instance_create`, and so on.
For production/live use cases, we recommend using a matchmaker for added security and flexibility.

### Create Instance

RPC - instance_create

```json
{
  "max_players": 2,
  "user_ids": [],
  "metadata": {}
}
```

Set `max_players` to `0` or `-1` for unlimited. Use with caution, we recommend performing a benchmark for server resource usage impact.

`metadata` can include the [per-deployment overrides](#per-deployment-overrides) `edgegap_version` and `edgegap_application`.

If `user_ids` is empty, the requesting user's ID will be used.

### Get Instance

RPC - instance_get

```json
{
  "instance_id": "<instance_id>"
}
```

### List Instance

RPC - instance_list

```json
{
  "query": "",
  "limit": 100,
  "cursor": ""
}
```

`query` can be used to search instance with available seats.

Example to list all instances READY with at least 1 seat available.

```json
{
  "query": "+value.metadata.edgegap.available_seats:>=1 +value.status:READY",
  "limit": 100,
  "cursor": ""
}

```

### Join Instance

RPC - instance_join

```json
{
  "instance_id": "<instance_id>",
  "user_ids": []
}
```

If `user_ids` is empty, the requesting user's ID will be used.

## Matchmaker

You can create your own integration using Nakama's Matchmaker, see our starter code sample:

```go
// OnMatchmakerMatched When a match is created via matchmaker, collect the Users and create a instance
func OnMatchmakerMatched(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, entries []runtime.MatchmakerEntry) (string, error) {
	maxPlayers := len(entries)
	userIds := make([]string, 0, len(entries))
	for _, entry := range entries {
		userIds = append(userIds, entry.GetPresence().GetUserId())
	}
	
	var callback runtime.FmCreateCallbackFn = func(status runtime.FmCreateStatus, instanceInfo *runtime.InstanceInfo, sessionInfo []*runtime.SessionInfo, metadata map[string]any, createErr error) {
		switch status {
		case runtime.CreateSuccess:
			logger.Info("Edgegap instance created: %s", instanceInfo.Id)

			content := map[string]interface{}{
				"IpAddress":  instanceInfo.ConnectionInfo.IpAddress,
				"DnsName":    instanceInfo.ConnectionInfo.DnsName,
				"Port":       instanceInfo.ConnectionInfo.Port,
				"InstanceId": instanceInfo.Id,
			}
			// Send connection details notifications to players
			for _, userId := range userIds {
				subject := "connection-info"

				code := notificationConnectionInfo
				err := nk.NotificationSend(ctx, userId, subject, content, code, "", false)
				if err != nil {
					logger.WithField("error", err.Error()).Error("Failed to send notification")
				}
			}
			return
		case runtime.CreateTimeout:
			logger.WithField("error", createErr.Error()).Error("Failed to create Edgegap instance, timed out")

			// Send notification to client that instance session creation timed out
			for _, userId := range userIds {
				subject := "create-timeout"
				content := map[string]interface{}{}
				code := notificationCreateTimeout
				err := nk.NotificationSend(ctx, userId, subject, content, code, "", false)
				if err != nil {
					logger.WithField("error", err.Error()).Error("Failed to send notification")
				}
			}
		default:
			logger.WithField("error", createErr.Error()).Error("Failed to create Edgegap instance")

			// Send notification to client that instance session couldn't be created
			for _, userId := range userIds {
				subject := "create-failed"
				content := map[string]interface{}{}
				code := notificationCreateFailed
				err := nk.NotificationSend(ctx, userId, subject, content, code, "", false)
				if err != nil {
					logger.WithField("error", err.Error()).Error("Failed to send notification")
				}
			}
			return
		}
	}
	
	efm := nk.GetFleetManager()
	if _, err := efm.Create(ctx, maxPlayers, userIds, nil, nil, callback); err != nil {
		logger.WithField("error", err.Error()).Error("failed to create Edgegap instance")
		return "", ErrInternalError
	}

	reply := instanceCreateReply{
		Message: "Instance Created",
		Ok:      true,
	}

	replyString, err := json.Marshal(reply)
	if err != nil {
		logger.WithField("error", err.Error()).Error("failed to marshal instance create reply")
		return "", ErrInternalError
	}

	return string(replyString), nil
}
```

Register `OnMatchmakerMatched` callback like this in the `InitModule` function in your `main.go`:

```go
err = initializer.RegisterMatchmakerMatched(OnMatchmakerMatched)
if err != nil {
    logger.WithField("error", err).Error("failed to register Matchmaker matched with fleet manager")
    return err
}
```

## Testing

### Test Scripts
The `scripts/windows/` directory contains Windows batch scripts for testing the version management feature locally.

Quick start:
```bash
# Start the environment
docker compose up -d

# Get current version
./scripts/windows/test_get_version.bat

# Update version
./scripts/windows/test_update_version.bat "v1.0.0"
```

See [scripts/README.md](scripts/README.md) for detailed usage instructions.

## Support and Troubleshooting

For Edgegap-related questions and reports, please reach out to us over our [Community Discord](http://discord.gg/MmJf8fWjnt) and include your deployment ID if possible.
