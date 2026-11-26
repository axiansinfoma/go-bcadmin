package bcadmin

import (
	"context"
	"errors"
	"net/http"
)

// NotificationsService manages the recipients of the tenant's notification
// emails about environment updates, update failures and extension
// validation. Use it through [Client.Notifications].
type NotificationsService struct {
	c *Client
}

// Notifications returns the notification endpoints of the tenant.
func (c *Client) Notifications() *NotificationsService { return &NotificationsService{c: c} }

// NotificationRecipient receives the tenant's notification emails.
type NotificationRecipient struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	// Name is the full name of the recipient.
	Name string `json:"name"`
}

// NotificationSettings are the notification settings of the tenant.
type NotificationSettings struct {
	// AADTenantID is the Microsoft Entra tenant ID of the caller.
	AADTenantID string                  `json:"aadTenantId"`
	Recipients  []NotificationRecipient `json:"recipients"`
}

// Settings returns the notification settings of the tenant.
//
// GET /settings/notification.
func (s *NotificationsService) Settings(ctx context.Context) (*NotificationSettings, error) {
	var out NotificationSettings
	if err := s.c.call(ctx, http.MethodGet, "settings/notification", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Recipients returns the notification recipients of the tenant.
//
// GET /settings/notification/recipients.
func (s *NotificationsService) Recipients(ctx context.Context) ([]NotificationRecipient, error) {
	return listAll[NotificationRecipient](ctx, s.c, "settings/notification/recipients")
}

// AddRecipient adds a notification recipient and returns it with its ID.
// The request creates a recipient, so it is not retried on server errors.
//
// PUT /settings/notification/recipients.
func (s *NotificationsService) AddRecipient(ctx context.Context, email, name string) (*NotificationRecipient, error) {
	if blank(email) || blank(name) {
		return nil, errors.New("bcadmin: recipient email and name are required")
	}
	body := struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}{email, name}
	var out NotificationRecipient
	if err := s.c.call(withNonIdempotent(ctx), http.MethodPut, "settings/notification/recipients", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveRecipient removes the notification recipient with the given ID.
//
// DELETE /settings/notification/recipients/{id}.
func (s *NotificationsService) RemoveRecipient(ctx context.Context, id string) error {
	if id == "" || id == "00000000-0000-0000-0000-000000000000" {
		return errors.New("bcadmin: recipient ID is required")
	}
	return s.c.call(ctx, http.MethodDelete, "settings/notification/recipients/{}", []string{id}, nil, nil)
}
