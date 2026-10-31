package appliance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const (
	backendControlHeader       = "X-AMDL-Control-Token"
	backendMaintenanceDrainURL = "/_amdl/internal/maintenance/drain"
	backendMaintenanceLeaveURL = "/_amdl/internal/maintenance/leave"
	backendReadyURL            = "/_amdl/internal/health/ready"
)

var errBackendActiveJobs = errors.New("backend has active jobs")

func enterBackendMaintenance(ctx context.Context, host string, port int, token string) error {
	return callBackendMaintenance(ctx, host, port, token, backendMaintenanceDrainURL)
}

func leaveBackendMaintenance(ctx context.Context, host string, port int, token string) error {
	return callBackendMaintenance(ctx, host, port, token, backendMaintenanceLeaveURL)
}

func callBackendMaintenance(ctx context.Context, host string, port int, token, endpoint string) error {
	if err := validateApplianceBackendHost(host); err != nil {
		return err
	}
	if len(token) != 64 {
		return errors.New("backend control token is invalid")
	}
	url, err := backendURL(host, port, endpoint)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return errors.New("create backend maintenance request failed")
	}
	request.Header.Set(backendControlHeader, token)
	client, err := newBackendHTTPClient(host)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("backend maintenance request failed")
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	_ = response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return nil
	}
	if endpoint == backendMaintenanceDrainURL && response.StatusCode == http.StatusConflict {
		return errBackendActiveJobs
	}
	return fmt.Errorf("backend maintenance request returned HTTP %d", response.StatusCode)
}
