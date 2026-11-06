package fleetmanager

import "time"

type EdgegapInstanceInfo struct {
	MaxPlayers        int      `json:"max_players"`
	AvailableSeats    int      `json:"available_seats"`
	CallbackId        string   `json:"callback_id"`
	Reservations      []string `json:"reservations"`
	ReservationsCount int      `json:"reservations_count"`
	// ReservationsUpdatedAt is the time of the oldest outstanding reservation, kept in sync with ReservationTimes.
	ReservationsUpdatedAt time.Time            `json:"reservations_updated_at"`
	ReservationTimes      map[string]time.Time `json:"reservation_times,omitempty"`
	Connections           []string             `json:"connections"`
}

// reservedAt returns when userId's seat was reserved. Reservations stored before per-user times
// existed fall back to the instance-wide timestamp.
func (ei *EdgegapInstanceInfo) reservedAt(userId string) time.Time {
	if t, ok := ei.ReservationTimes[userId]; ok {
		return t
	}
	return ei.ReservationsUpdatedAt
}

type EdgegapUserData struct {
	IpAddress string `json:"ip_address"`
}

type EdgegapDeploymentUser struct {
	UserType string          `json:"user_type"`
	UserData EdgegapUserData `json:"user_data"`
}

type EdgegapEnvironmentVariable struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	IsHidden bool   `json:"is_hidden"`
}

type EdgegapWebhook struct {
	Url string `json:"url"`
}

type EdgegapDeploymentCreation struct {
	Application          string                       `json:"application"`
	Version              string                       `json:"version"`
	Users                []EdgegapDeploymentUser      `json:"users"`
	EnvironmentVariables []EdgegapEnvironmentVariable `json:"environment_variables"`
	Tags                 []string                     `json:"tags"`
	WebhookOnReady       EdgegapWebhook               `json:"webhook_on_ready"`
	WebhookOnError       EdgegapWebhook               `json:"webhook_on_error"`
	WebhookOnTerminated  EdgegapWebhook               `json:"webhook_on_terminated"`
}

type EdgegapDeploymentPort struct {
	External int    `json:"external"`
	Internal int    `json:"internal"`
	Protocol string `json:"protocol"`
	Name     string `json:"name"`
	Link     string `json:"link"`
}

type EdgegapDeploymentLocation struct {
	City      string  `json:"city"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type EdgegapDeploymentStatus struct {
	RequestId     string                           `json:"request_id"`
	Fqdn          string                           `json:"fqdn"`
	PublicIp      string                           `json:"public_ip"`
	CurrentStatus string                           `json:"current_status"`
	Running       bool                             `json:"running"`
	Error         bool                             `json:"error"`
	ErrorDetail   string                           `json:"error_detail"`
	Ports         map[string]EdgegapDeploymentPort `json:"ports"`
	Location      EdgegapDeploymentLocation        `json:"location"`
}

type EdgegapDeploymentResponse struct {
	RequestId string `json:"request_id"`
}

type EdgegapApiMessage struct {
	Message string `json:"message"`
}

const DeploymentStatusError = "Status.ERROR"

type EdgegapDeploymentSummary struct {
	RequestId string `json:"request_id"`
	Ready     bool   `json:"ready"`
	Status    string `json:"status"`
}

type EdgegapPagination struct {
	Number         int  `json:"number"`
	NextPageNumber int  `json:"next_page_number"`
	HasNext        bool `json:"has_next"`
}

type EdgegapDeploymentList struct {
	Data       []EdgegapDeploymentSummary `json:"data"`
	TotalCount int                        `json:"total_count"`
	Pagination EdgegapPagination          `json:"pagination"`
}

type ConnectionEventMessage struct {
	InstanceId  string   `json:"instance_id"`
	Connections []string `json:"connections"`
}

const (
	InstanceEventStateReady = "READY"
	InstanceEventStateError = "ERROR"
	InstanceEventStateStop  = "STOP"
)

type InstanceEventMessage struct {
	InstanceId string         `json:"instance_id"`
	Action     string         `json:"action"`
	Message    string         `json:"message"`
	Metadata   map[string]any `json:"metadata"`
}
