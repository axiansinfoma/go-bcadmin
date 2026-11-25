package bcadmin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/axiansinfoma/go-bcadmin/internal/multipartbody"
)

// MaxPTESize is the largest .app file the API accepts for a per-tenant
// extension upload.
const MaxPTESize = 50 << 20

// DeploymentSchedule tells when an uploaded per-tenant extension is
// installed.
type DeploymentSchedule string

// Deployment schedules. New per-tenant extensions can only use Immediate or
// UpdateWindow; updates of installed ones can use all four.
const (
	DeploymentImmediate       DeploymentSchedule = "Immediate"
	DeploymentUpdateWindow    DeploymentSchedule = "UpdateWindow"
	DeploymentNextMinorUpdate DeploymentSchedule = "NextMinorUpdate"
	DeploymentNextMajorUpdate DeploymentSchedule = "NextMajorUpdate"
)

// SyncMode is the schema synchronization mode of a per-tenant extension
// installation.
type SyncMode string

// Sync modes.
const (
	SyncModeAdd       SyncMode = "Add"
	SyncModeForceSync SyncMode = "ForceSync"
)

// InstallPTEOptions configures [AppsService.InstallPTE].
type InstallPTEOptions struct {
	// FileName is the name of the .app file sent to the service, for
	// example "Contoso_MyApp_1.0.0.0.app". Only the base name is sent.
	// Required.
	FileName string
	// DeploymentSchedule tells when the extension is installed. The service
	// default is Immediate.
	DeploymentSchedule DeploymentSchedule
	// SyncMode is the schema sync mode. The service default is Add.
	SyncMode SyncMode
	// LanguageID is the language for the installation, for example
	// "en-US". The service default is "en-US".
	LanguageID string
	// AcceptISVEULA must be true for the installation to proceed. Setting it
	// accepts the license terms described in the Admin Center API
	// documentation.
	AcceptISVEULA bool
	// InstallOrUpdateNeededDependencies installs or updates dependencies;
	// otherwise the installation fails when dependencies are missing.
	InstallOrUpdateNeededDependencies bool
}

// InstallPTE uploads a per-tenant extension (.app file) and schedules its
// installation; it returns a poller for the install operation. file is read
// from its start; it must not change until the request is done. The file is
// streamed, not loaded into memory, and may be up to [MaxPTESize] bytes.
// Available since API v2.29.
//
// With a deployment schedule other than Immediate, the operation stays
// Scheduled until the update window or update; use
// WaitOptions.TreatScheduledAsDone to return when it is scheduled.
//
// POST /applications/{applicationFamily}/environments/{environmentName}/apps/pteInstall.
func (s *AppsService) InstallPTE(ctx context.Context, file io.ReadSeeker, opts InstallPTEOptions) (*Poller[*Operation], error) {
	if s.err != nil {
		return nil, s.err
	}
	if !s.c.apiVersionAtLeast(2, 29) {
		return nil, fmt.Errorf("bcadmin: pteInstall needs API version v2.29 or later, client uses %s", s.c.apiVersion)
	}
	if file == nil {
		return nil, errors.New("bcadmin: file is required")
	}
	name := filepath.Base(strings.ReplaceAll(opts.FileName, `\`, "/"))
	if opts.FileName == "" || name == "." || name == "/" {
		return nil, errors.New("bcadmin: InstallPTEOptions.FileName is required")
	}
	if !strings.EqualFold(filepath.Ext(name), ".app") {
		return nil, fmt.Errorf("bcadmin: file name %q must end with .app", clean(name))
	}

	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("bcadmin: determining the size of %s: %w", clean(name), err)
	}
	if size > MaxPTESize {
		return nil, fmt.Errorf("bcadmin: %s has %d bytes; the API accepts at most %d", clean(name), size, MaxPTESize)
	}

	fields := []multipartbody.Field{
		{Name: "acceptIsvEula", Value: strconv.FormatBool(opts.AcceptISVEULA)},
		{Name: "installOrUpdateNeededDependencies", Value: strconv.FormatBool(opts.InstallOrUpdateNeededDependencies)},
	}
	if opts.DeploymentSchedule != "" {
		fields = append(fields, multipartbody.Field{Name: "deploymentSchedule", Value: string(opts.DeploymentSchedule)})
	}
	if opts.SyncMode != "" {
		fields = append(fields, multipartbody.Field{Name: "syncMode", Value: string(opts.SyncMode)})
	}
	if opts.LanguageID != "" {
		fields = append(fields, multipartbody.Field{Name: "languageId", Value: opts.LanguageID})
	}
	for _, f := range fields {
		if strings.ContainsAny(f.Value, "\r\n") {
			return nil, fmt.Errorf("bcadmin: invalid value for %s", f.Name)
		}
	}
	body, err := multipartbody.New(fields, "extensionFile", name, file)
	if err != nil {
		return nil, fmt.Errorf("bcadmin: building the upload: %w", err)
	}

	var op Operation
	if err := s.c.call(ctx, http.MethodPost, "applications/{}/environments/{}/apps/pteInstall",
		[]string{s.family, s.env}, &rawBody{r: body, contentType: body.ContentType}, &op); err != nil {
		return nil, err
	}
	if op.ID == "" || op.AppID == "" {
		return nil, errors.New("bcadmin: pteInstall was accepted but the response contains no operation or app ID to track; check the scheduled operations before trying again")
	}
	return newPoller(s.c, &op, s.fetcher(op.AppID, op.ID), operationResult), nil
}

// RemoveScheduledPTEVersionOptions selects the scheduled installation to
// remove.
type RemoveScheduledPTEVersionOptions struct {
	// TargetVersion is the scheduled version, for example "2.0.0.0".
	TargetVersion string `json:"targetVersion"`
	// ScheduleKind is UpdateWindow, NextMinorUpdate or NextMajorUpdate.
	ScheduleKind DeploymentSchedule `json:"scheduleKind"`
}

// ScheduledPTEOperations returns the scheduled installations and updates of
// per-tenant extensions. Available since API v2.29.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/apps/scheduledPteOperations.
func (s *AppsService) ScheduledPTEOperations(ctx context.Context) ([]Operation, error) {
	if s.err != nil {
		return nil, s.err
	}
	return listAll[Operation](ctx, s.c, "applications/{}/environments/{}/apps/scheduledPteOperations", s.family, s.env)
}

// RemoveScheduledPTEVersion cancels a scheduled installation or update of a
// per-tenant extension and returns the canceled operation. The service
// deletes the uploaded .app file. Available since API v2.29.
//
// POST /applications/{applicationFamily}/environments/{environmentName}/apps/{appId}/removeScheduledPteVersion.
func (s *AppsService) RemoveScheduledPTEVersion(ctx context.Context, appID string, opts RemoveScheduledPTEVersionOptions) (*Operation, error) {
	if s.err != nil {
		return nil, s.err
	}
	if opts.TargetVersion == "" || opts.ScheduleKind == "" {
		return nil, errors.New("bcadmin: RemoveScheduledPTEVersionOptions.TargetVersion and ScheduleKind are required")
	}
	var op Operation
	if err := s.c.call(ctx, http.MethodPost, "applications/{}/environments/{}/apps/{}/removeScheduledPteVersion",
		[]string{s.family, s.env, appID}, opts, &op); err != nil {
		return nil, err
	}
	return &op, nil
}
