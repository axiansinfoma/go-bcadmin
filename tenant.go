package bcadmin

import (
	"context"
	"iter"
	"net/http"
)

// TenantService provides the tenant-wide endpoints: quotas, storage and the
// operations of all environments. Use it through [Client.Tenant].
type TenantService struct {
	c *Client
}

// Tenant returns the tenant-wide endpoints.
func (c *Client) Tenant() *TenantService { return &TenantService{c: c} }

// EnvironmentQuotas holds the environment and storage limits of a tenant.
type EnvironmentQuotas struct {
	EnvironmentsCount  EnvironmentsCountQuota `json:"environmentsCount"`
	StorageInKilobytes StorageQuota           `json:"storageInKilobytes"`
}

// EnvironmentsCountQuota is the number of environments a tenant may have.
type EnvironmentsCountQuota struct {
	Production int `json:"production"`
	Sandbox    int `json:"sandbox"`
}

// StorageQuota is the database storage a tenant may use, in kilobytes.
type StorageQuota struct {
	Default            int64 `json:"default"`
	UserLicenses       int64 `json:"userLicenses"`
	AdditionalCapacity int64 `json:"additionalCapacity"`
	Total              int64 `json:"total"`
}

// UsedStorage is the database storage used by one environment.
type UsedStorage struct {
	EnvironmentType            EnvironmentType `json:"environmentType"`
	EnvironmentName            string          `json:"environmentName"`
	ApplicationFamily          string          `json:"applicationFamily"`
	DatabaseStorageInKilobytes int64           `json:"databaseStorageInKilobytes"`
}

// Quotas returns the tenant's environment and storage quotas.
//
// GET /environments/quotas.
func (s *TenantService) Quotas(ctx context.Context) (*EnvironmentQuotas, error) {
	var q EnvironmentQuotas
	if err := s.c.call(ctx, http.MethodGet, "environments/quotas", nil, nil, &q); err != nil {
		return nil, err
	}
	return &q, nil
}

// UsedStorage returns the database storage used by each environment of the
// tenant. Before API version v2.26 the API returns -1 for databases larger
// than 2 TB.
//
// GET /environments/usedstorage.
func (s *TenantService) UsedStorage(ctx context.Context) ([]UsedStorage, error) {
	return listAll[UsedStorage](ctx, s.c, "environments/usedstorage")
}

// Operations returns the operations of all environments of the tenant.
// Available since API version v2.7.
//
// GET /environments/operations.
func (s *TenantService) Operations(ctx context.Context) ([]Operation, error) {
	return listAll[Operation](ctx, s.c, "environments/operations")
}

// AllOperations iterates over the operations of all environments of the
// tenant, requesting further pages as needed.
func (s *TenantService) AllOperations(ctx context.Context) iter.Seq2[Operation, error] {
	return list[Operation](ctx, s.c, "environments/operations")
}

// GetOperation returns one operation of any environment of the tenant.
// Available since API version v2.28.
//
// GET /environments/operations/{operationId}.
func (s *TenantService) GetOperation(ctx context.Context, operationID string) (*Operation, error) {
	var op Operation
	if err := s.c.call(ctx, http.MethodGet, "environments/operations/{}", []string{operationID}, nil, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

// ResumeOperation returns a poller for an operation that was started earlier,
// for example by another process. The poller requests the operation's status
// with [TenantService.GetOperation] and returns the final operation as its
// result. Available since API version v2.28.
func (s *TenantService) ResumeOperation(operationID string) (*Poller[*Operation], error) {
	if _, err := path("environments/operations/{}", operationID); err != nil {
		return nil, err
	}
	fetch := func(ctx context.Context) (*Operation, error) { return s.GetOperation(ctx, operationID) }
	return newPoller(s.c, &Operation{ID: operationID}, fetch, operationResult), nil
}

// operationResult is the result function of pollers whose result is the
// operation itself.
func operationResult(_ context.Context, op *Operation, _ *WaitOptions) (*Operation, error) {
	return op, nil
}
