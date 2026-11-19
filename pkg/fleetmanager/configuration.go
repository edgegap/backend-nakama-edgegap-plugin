package fleetmanager

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/edgegap/nakama-edgegap/internal/helpers"
)

// EdgegapConfig configures an Edgegap Fleet Manager. Create it with NewEdgegapConfig, which sets the
// defaults of the optional fields, and override them as needed before calling NewEdgegapFleetManager.
type EdgegapConfig struct {
	ApiUrl                 string        // Edgegap API URL, e.g. https://api.edgegap.com
	ApiToken               string        // Edgegap API token, including the "token" prefix
	Application            string        // Edgegap application to deploy
	PortName               string        // Edgegap application port name sent to game clients
	NakamaAccessUrl        string        // Nakama API URL that deployments call back to, must use https://
	InitialVersion         string        // Optional: version stored when no version exists in storage
	PollingInterval        time.Duration // Interval of the sync with the Edgegap API, 0 disables it
	CleanupInterval        time.Duration // Interval of the reservation expiry check, 0 disables it
	ReservationMaxDuration time.Duration // Duration of a seat reservation before it expires

	nakamaHttpKey string // Nakama runtime HTTP key, read from the Nakama config on Init
}

// NewEdgegapConfig creates an EdgegapConfig with the required values and the defaults of the optional ones.
func NewEdgegapConfig(apiUrl, apiToken, application, portName, nakamaAccessUrl string) EdgegapConfig {
	return EdgegapConfig{
		ApiUrl:                 apiUrl,
		ApiToken:               apiToken,
		Application:            application,
		PortName:               portName,
		NakamaAccessUrl:        nakamaAccessUrl,
		PollingInterval:        15 * time.Minute,
		CleanupInterval:        1 * time.Minute,
		ReservationMaxDuration: 30 * time.Second,
	}
}

// Validate Will check if the configuration is valid
func (ec *EdgegapConfig) Validate() error {
	errs := make([]error, 0)

	if ec.ApiUrl == "" {
		errs = append(errs, errors.New("edgegap url must be set"))
	}

	if ec.ApiToken == "" {
		errs = append(errs, errors.New("edgegap token must be set"))
	}

	if ec.Application == "" {
		errs = append(errs, errors.New("edgegap application must be set"))
	}

	// Initial version is optional - only used when no version exists in storage

	if ec.PortName == "" {
		errs = append(errs, errors.New("edgegap application port name must be set"))
	}

	if ec.NakamaAccessUrl == "" {
		errs = append(errs, errors.New("nakama access url must be set"))
	}

	if ec.PollingInterval < 0 {
		errs = append(errs, errors.New("polling interval must be >= 0"))
	}

	if ec.CleanupInterval < 0 {
		errs = append(errs, errors.New("cleanup interval must be >= 0"))
	}

	if ec.ReservationMaxDuration <= 0 {
		errs = append(errs, errors.New("reservation max duration must be > 0"))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	// Validate Edgegap API connection
	apiHelper := helpers.NewAPIClient(ec.ApiUrl, ec.ApiToken)
	// Test API connection by checking the application exists
	reply, err := apiHelper.Get(fmt.Sprintf("/v1/app/%s", ec.Application))
	if err != nil {
		return fmt.Errorf("failed to connect to Edgegap API, check URL: %s", err.Error())
	}
	defer reply.Body.Close()
	if reply.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to validate application with Edgegap API, check token and application name - Status Code=%s", reply.Status)
	}

	return nil
}
