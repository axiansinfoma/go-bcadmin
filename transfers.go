package bcadmin

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// minorTransfers is the API version (v2.20) from which the environment
// transfer endpoints are supported.
const minorTransfers = 20

// TransfersService moves environments between Microsoft Entra tenants. Use
// it through [Client.Transfers]. Available since API v2.20.
//
// The documentation states that only internal administrators can call
// these endpoints: calls authenticated as a delegated administrator or as a
// Microsoft Entra app are not possible. A transfer is requested on the
// source tenant ([TransfersService.RequestOutgoing]) and accepted on the
// destination tenant ([TransfersService.AcceptIncoming]), with a client
// for each tenant (see [Client.ForTenant]).
//
// The endpoints return operations; the documentation does not describe how
// to track them after they were created.
type TransfersService struct {
	c *Client
}

// Transfers returns the environment transfer endpoints.
func (c *Client) Transfers() *TransfersService { return &TransfersService{c: c} }

// OutgoingTransferOptions describe a requested environment transfer.
type OutgoingTransferOptions struct {
	// DestinationEntraTenantID is the Microsoft Entra tenant ID of the
	// destination tenant. Required.
	DestinationEntraTenantID string
	// RunAt is when the transfer runs if it is accepted on the destination
	// tenant. Required; it is sent as UTC.
	RunAt time.Time
}

// RequestOutgoing requests the transfer of the environment
// family/environment to another Microsoft Entra tenant and returns the
// operation of the request (type RequestTransferToAnotherEntraTenant). The
// call must be authenticated on the source tenant. The destination tenant
// must accept the transfer (see [TransfersService.AcceptIncoming]) before
// the operation's expiresOn parameter. The request is not retried on
// server errors (unless ClientOptions.RetryNonIdempotent is set).
// Available since API v2.20.
//
// POST /transfers/outgoing/applications/{applicationFamily}/environments/{environmentName}.
func (s *TransfersService) RequestOutgoing(ctx context.Context, family, environment string, opts OutgoingTransferOptions) (*Operation, error) {
	if blank(opts.DestinationEntraTenantID) {
		return nil, errors.New("bcadmin: OutgoingTransferOptions.DestinationEntraTenantID is required")
	}
	if opts.RunAt.IsZero() {
		return nil, errors.New("bcadmin: OutgoingTransferOptions.RunAt is required")
	}
	if err := s.c.needVersion("environment transfers", minorTransfers); err != nil {
		return nil, err
	}
	body := struct {
		DestinationEntraTenantID string    `json:"DestinationEntraTenantId"`
		RunAt                    time.Time `json:"RunAt"`
	}{opts.DestinationEntraTenantID, opts.RunAt.UTC()}
	var op Operation
	if err := s.c.call(ctx, http.MethodPost, "transfers/outgoing/applications/{}/environments/{}",
		[]string{family, environment}, body, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

// CancelOutgoing cancels the pending transfer of the environment
// family/environment and returns the canceled operation, or nil if the
// response has none. The call must be authenticated on the source tenant.
// Available since API v2.20.
//
// DELETE /transfers/outgoing/applications/{applicationFamily}/environments/{environmentName}.
func (s *TransfersService) CancelOutgoing(ctx context.Context, family, environment string) (*Operation, error) {
	if err := s.c.needVersion("environment transfers", minorTransfers); err != nil {
		return nil, err
	}
	var op Operation
	if err := s.c.call(ctx, http.MethodDelete, "transfers/outgoing/applications/{}/environments/{}",
		[]string{family, environment}, nil, &op); err != nil {
		return nil, err
	}
	if op.ID == "" {
		return nil, nil
	}
	return &op, nil
}

// Outgoing returns the transfer requests of the tenant that have not been
// completed. The call must be authenticated on the source tenant.
// Available since API v2.20.
//
// GET /transfers/outgoing.
func (s *TransfersService) Outgoing(ctx context.Context) ([]Operation, error) {
	if err := s.c.needVersion("environment transfers", minorTransfers); err != nil {
		return nil, err
	}
	return listAll[Operation](ctx, s.c, "transfers/outgoing")
}

// Incoming returns the transfers from the source tenant
// sourceEntraTenantID to this tenant that have not been completed. The call
// must be authenticated on the destination tenant. Available since API
// v2.20.
//
// GET /transfers/incoming/{sourceEntraTenantId}.
func (s *TransfersService) Incoming(ctx context.Context, sourceEntraTenantID string) ([]Operation, error) {
	if err := s.c.needVersion("environment transfers", minorTransfers); err != nil {
		return nil, err
	}
	return listAll[Operation](ctx, s.c, "transfers/incoming/{}", sourceEntraTenantID)
}

// IncomingTransfer selects an incoming environment transfer to accept.
type IncomingTransfer struct {
	// SourceEntraTenantID is the Microsoft Entra tenant ID of the source
	// tenant. Required.
	SourceEntraTenantID string `json:"SourceEntraTenantId"`
	// ApplicationFamily is the family of the environment, for example
	// "BusinessCentral". Required.
	ApplicationFamily string `json:"ApplicationFamily"`
	// SourceEnvironmentName is the name of the environment in the source
	// tenant. Required.
	SourceEnvironmentName string `json:"SourceEnvironmentName"`
	// DestinationEnvironmentName is the name the environment gets in this
	// tenant. Required.
	DestinationEnvironmentName string `json:"DestinationEnvironmentName"`
}

// AcceptIncoming accepts a transfer requested by another tenant and returns
// the operation of the transfer (type MoveToAnotherAadTenant), which runs
// at the time set in the request. The call must be authenticated on the
// destination tenant. The request is not retried on server errors (unless
// ClientOptions.RetryNonIdempotent is set). Available since API v2.20.
//
// POST /transfers/incoming.
func (s *TransfersService) AcceptIncoming(ctx context.Context, transfer IncomingTransfer) (*Operation, error) {
	if blank(transfer.SourceEntraTenantID) || blank(transfer.ApplicationFamily) ||
		blank(transfer.SourceEnvironmentName) || blank(transfer.DestinationEnvironmentName) {
		return nil, errors.New("bcadmin: IncomingTransfer.SourceEntraTenantID, ApplicationFamily, SourceEnvironmentName and DestinationEnvironmentName are required")
	}
	if err := s.c.needVersion("environment transfers", minorTransfers); err != nil {
		return nil, err
	}
	var op Operation
	if err := s.c.call(ctx, http.MethodPost, "transfers/incoming", nil, transfer, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

// RequestTransfer requests the transfer of the environment to another
// Microsoft Entra tenant; see [TransfersService.RequestOutgoing].
func (e *Environment) RequestTransfer(ctx context.Context, opts OutgoingTransferOptions) (*Operation, error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.c.Transfers().RequestOutgoing(ctx, e.ApplicationFamily, e.Name, opts)
}

// CancelTransfer cancels the pending transfer of the environment; see
// [TransfersService.CancelOutgoing].
func (e *Environment) CancelTransfer(ctx context.Context) (*Operation, error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.c.Transfers().CancelOutgoing(ctx, e.ApplicationFamily, e.Name)
}
