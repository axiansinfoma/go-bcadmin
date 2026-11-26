package bcadmin

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// AuthorizedAppsService manages the Microsoft Entra apps authorized to call
// the Admin Center API for the tenant. Use it through
// [Client.AuthorizedApps].
type AuthorizedAppsService struct {
	c *Client
}

// AuthorizedApps returns the authorized Microsoft Entra app endpoints.
func (c *Client) AuthorizedApps() *AuthorizedAppsService { return &AuthorizedAppsService{c: c} }

// AuthorizedApp is a Microsoft Entra app authorized to call the Admin
// Center API.
type AuthorizedApp struct {
	// AppID is the application (client) ID of the app.
	AppID string `json:"appId"`
	// IsAdminConsentGranted tells whether admin consent is granted for the
	// app in Microsoft Entra ID.
	IsAdminConsentGranted bool `json:"isAdminConsentGranted"`
}

// ManageableTenant is a tenant in which the calling app is authorized.
type ManageableTenant struct {
	// EntraTenantID is the Microsoft Entra tenant ID.
	EntraTenantID string `json:"entraTenantId"`
}

// List returns the apps authorized for the tenant. The documentation states
// that the endpoint cannot be used when authenticated as an app.
//
// GET /authorizedAadApps.
func (s *AuthorizedAppsService) List(ctx context.Context) ([]AuthorizedApp, error) {
	return listAll[AuthorizedApp](ctx, s.c, "authorizedAadApps")
}

// Authorize authorizes the app with the given application (client) ID to
// call the Admin Center API for the tenant and returns it. It does not grant
// admin consent or assign permission sets in environments. The
// documentation states that the endpoint cannot be used when authenticated
// as an app.
//
// PUT /authorizedAadApps/{appClientId}.
func (s *AuthorizedAppsService) Authorize(ctx context.Context, appClientID string) (*AuthorizedApp, error) {
	if err := checkAppClientID(appClientID); err != nil {
		return nil, err
	}
	var out AuthorizedApp
	if err := s.c.call(ctx, http.MethodPut, "authorizedAadApps/{}", []string{appClientID}, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Remove removes the authorization of the app with the given application
// (client) ID. It does not revoke admin consent in Microsoft Entra ID or
// remove permission sets assigned to the app in environments.
//
// DELETE /authorizedAadApps/{appClientId}.
func (s *AuthorizedAppsService) Remove(ctx context.Context, appClientID string) error {
	if err := checkAppClientID(appClientID); err != nil {
		return err
	}
	return s.c.call(ctx, http.MethodDelete, "authorizedAadApps/{}", []string{appClientID}, nil, nil)
}

// ManageableTenants returns the tenants in which the calling app is
// authorized in the Business Central administration center; it does not
// tell whether admin consent is granted there. The documentation states
// that the endpoint can only be used when authenticated as an app.
// Available since API v2.24.
//
// GET /authorizedAadApps/manageableTenants.
func (s *AuthorizedAppsService) ManageableTenants(ctx context.Context) ([]ManageableTenant, error) {
	if err := s.c.needVersion("manageableTenants", 24); err != nil {
		return nil, err
	}
	return listAll[ManageableTenant](ctx, s.c, "authorizedAadApps/manageableTenants")
}

// checkAppClientID refuses an empty or all-zero application ID, and
// "manageableTenants", which would address another endpoint.
func checkAppClientID(id string) error {
	switch {
	case blank(id), id == "00000000-0000-0000-0000-000000000000":
		return errors.New("bcadmin: app client ID is required")
	case strings.EqualFold(strings.TrimSpace(id), "manageableTenants"):
		return errors.New("bcadmin: invalid app client ID")
	}
	return nil
}
