package bcadmin

import (
	"strings"
	"time"
)

// OperationStatus is the status of a long-running operation. The API is not
// consistent about the casing of status values, so compare them with
// [OperationStatus.Is] rather than ==.
type OperationStatus string

// Operation statuses.
const (
	OperationStatusQueued    OperationStatus = "Queued"
	OperationStatusScheduled OperationStatus = "Scheduled"
	OperationStatusRunning   OperationStatus = "Running"
	OperationStatusSucceeded OperationStatus = "Succeeded"
	OperationStatusFailed    OperationStatus = "Failed"
	OperationStatusCanceled  OperationStatus = "Canceled"
	OperationStatusSkipped   OperationStatus = "Skipped"
)

// Is reports whether s and other are the same status, ignoring case and
// treating "Cancelled" and "Canceled" as equal.
func (s OperationStatus) Is(other OperationStatus) bool {
	return strings.EqualFold(normalizeStatus(s), normalizeStatus(other))
}

func normalizeStatus(s OperationStatus) string {
	if strings.EqualFold(string(s), "cancelled") {
		return string(OperationStatusCanceled)
	}
	return string(s)
}

// Terminal reports whether s is a final status: Succeeded, Failed, Canceled or
// Skipped. Unknown statuses are not terminal.
func (s OperationStatus) Terminal() bool {
	return s.Is(OperationStatusSucceeded) || s.Is(OperationStatusFailed) ||
		s.Is(OperationStatusCanceled) || s.Is(OperationStatusSkipped)
}

// OperationType is the type of a long-running operation, for example "create"
// or "EnvironmentAppInstall". The API is not consistent about casing; compare
// with [OperationType.Is].
type OperationType string

// Operation types documented for the Admin Center API.
const (
	OperationTypeCopy                     OperationType = "Copy"
	OperationTypeCreate                   OperationType = "Create"
	OperationTypeDelete                   OperationType = "Delete"
	OperationTypeSoftDelete               OperationType = "SoftDelete"
	OperationTypeRecover                  OperationType = "Recover"
	OperationTypeEnvironmentAppHotfix     OperationType = "EnvironmentAppHotfix"
	OperationTypeEnvironmentAppInstall    OperationType = "EnvironmentAppInstall"
	OperationTypeEnvironmentAppUninstall  OperationType = "EnvironmentAppUninstall"
	OperationTypeEnvironmentAppUpdate     OperationType = "EnvironmentAppUpdate"
	OperationTypeEnvironmentRename        OperationType = "EnvironmentRename"
	OperationTypeModify                   OperationType = "Modify"
	OperationTypeMoveToAnotherAadTenant   OperationType = "MoveToAnotherAadTenant"
	OperationTypePitRestore               OperationType = "PitRestore"
	OperationTypeRestart                  OperationType = "Restart"
	OperationTypeUpdate                   OperationType = "Update"
	OperationTypeRequestTransfer          OperationType = "RequestTransferToAnotherEntraTenant"
	OperationTypeMoveToProductionDBConfig OperationType = "MoveToProductionDatabaseConfiguration"
	OperationTypeMoveToSandboxDBConfig    OperationType = "MoveToSandboxDatabaseConfiguration"
)

// Is reports whether t and other are the same type, ignoring case.
func (t OperationType) Is(other OperationType) bool {
	return strings.EqualFold(string(t), string(other))
}

// Operation is a long-running operation as returned by the API, either for an
// environment (create, copy, delete, rename, restore, update, ...) or for an
// app (install, update, uninstall). Fields that do not apply to an operation
// are empty.
type Operation struct {
	// ID is the operation ID.
	ID string `json:"id"`
	// Type is the operation type.
	Type OperationType `json:"type,omitempty"`
	// Status is the current status.
	Status OperationStatus `json:"status,omitempty"`
	// AadTenantID is the Microsoft Entra tenant of the environment.
	AadTenantID string `json:"aadTenantId,omitempty"`
	// CreatedOn is when the operation was created.
	CreatedOn *time.Time `json:"createdOn,omitempty"`
	// StartedOn is when the operation started running.
	StartedOn *time.Time `json:"startedOn,omitempty"`
	// CompletedOn is when the operation finished.
	CompletedOn *time.Time `json:"completedOn,omitempty"`
	// CreatedBy is the user or app that started the operation.
	CreatedBy string `json:"createdBy,omitempty"`
	// CreatorPrincipalType is the type of principal that started the
	// operation, for example "User" or "Application".
	CreatorPrincipalType string `json:"creatorPrincipalType,omitempty"`
	// CanceledBy is the user or app that canceled the operation.
	CanceledBy string `json:"canceledBy,omitempty"`
	// ErrorMessage is the API's message for a failed operation.
	ErrorMessage string `json:"errorMessage,omitempty"`
	// Parameters holds the type-specific parameters of the operation.
	Parameters map[string]any `json:"parameters,omitempty"`
	// EnvironmentName is the name of the environment.
	EnvironmentName string `json:"environmentName,omitempty"`
	// EnvironmentType is the type of the environment (production or sandbox).
	EnvironmentType string `json:"environmentType,omitempty"`
	// ProductFamily is the application family, for example "BusinessCentral".
	ProductFamily string `json:"productFamily,omitempty"`

	// AppID is the app of an app operation.
	AppID string `json:"appId,omitempty"`
	// SourceAppVersion is the app version before an app operation.
	SourceAppVersion string `json:"sourceAppVersion,omitempty"`
	// TargetAppVersion is the app version after an app operation.
	TargetAppVersion string `json:"targetAppVersion,omitempty"`
	// SourceVersion is the platform/application version before the operation.
	SourceVersion string `json:"sourceVersion,omitempty"`
	// TargetVersion is the platform/application version after the operation.
	TargetVersion string `json:"targetVersion,omitempty"`
	// ScheduleKind tells when a scheduled operation runs, for example
	// "UpdateWindow" or "NextMinorUpdate".
	ScheduleKind string `json:"scheduleKind,omitempty"`
}
