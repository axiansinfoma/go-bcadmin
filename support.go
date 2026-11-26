package bcadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// SupportService manages the support contact of environments and reports
// outages. Use it through [Client.Support].
type SupportService struct {
	c *Client
}

// Support returns the support and outage endpoints.
func (c *Client) Support() *SupportService { return &SupportService{c: c} }

// SupportContact is the contact shown to users of an environment for
// support.
type SupportContact struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	// URL is a free-form URL with more support information, such as a
	// support website.
	URL string `json:"url"`
}

// Contact returns the support contact of the environment
// family/environment.
//
// GET /support/applications/{applicationFamily}/environments/{environmentName}/supportcontact.
func (s *SupportService) Contact(ctx context.Context, family, environment string) (*SupportContact, error) {
	var out SupportContact
	if err := s.c.call(ctx, http.MethodGet, "support/applications/{}/environments/{}/supportcontact",
		[]string{family, environment}, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetContact sets the support contact of the environment family/environment
// and returns the stored contact. All fields are sent, including empty ones;
// the documentation does not say how the service treats empty values.
//
// PUT /support/applications/{applicationFamily}/environments/{environmentName}/supportcontact.
func (s *SupportService) SetContact(ctx context.Context, family, environment string, contact SupportContact) (*SupportContact, error) {
	var out SupportContact
	if err := s.c.call(ctx, http.MethodPut, "support/applications/{}/environments/{}/supportcontact",
		[]string{family, environment}, contact, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// OutageType is a kind of outage that can be reported.
type OutageType struct {
	// OutageType is the identifier to use in [OutageReport.OutageType] and
	// [SupportService.OutageQuestions].
	OutageType  string `json:"outageType"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// OutageTypes returns the kinds of outages that can be reported.
//
// GET /support/outageTypes.
func (s *SupportService) OutageTypes(ctx context.Context) ([]OutageType, error) {
	return listAll[OutageType](ctx, s.c, "support/outageTypes")
}

// OutageQuestionType is the input type of an outage question. Compare with
// [OutageQuestionType.Is].
type OutageQuestionType string

// Outage question types.
const (
	OutageQuestionNone      OutageQuestionType = "None"
	OutageQuestionToggle    OutageQuestionType = "Toggle"
	OutageQuestionTextField OutageQuestionType = "TextField"
	OutageQuestionDateTime  OutageQuestionType = "DateTime"
)

// Is reports whether t and other are the same type, ignoring case.
func (t OutageQuestionType) Is(other OutageQuestionType) bool {
	return strings.EqualFold(string(t), string(other))
}

// OutageQuestion is a question to answer when reporting an outage of a
// type.
type OutageQuestion struct {
	// Sequence is the order in which the question is asked.
	Sequence int `json:"sequence"`
	// ParentID is the ID of a toggle question; this question is only asked
	// when that toggle is true. The documentation types it as a string;
	// other JSON values are kept as their JSON text.
	ParentID string `json:"parentId"`
	ID       string `json:"id"`
	// DefaultValue is the value if the question is not answered.
	DefaultValue any                `json:"defaultValue"`
	QuestionType OutageQuestionType `json:"questionType"`
	QuestionText string             `json:"questionText"`
	Required     bool               `json:"required"`
	// OnText and OffText are the texts of a toggle for true and false.
	OnText  string `json:"onText"`
	OffText string `json:"offText"`
	// Multiline tells whether the answer may span several lines.
	Multiline bool `json:"multiline"`
}

// UnmarshalJSON decodes a question; a parentId that is not a string is
// kept as its JSON text, so one odd question does not fail the list.
func (q *OutageQuestion) UnmarshalJSON(data []byte) error {
	type plain OutageQuestion
	var v struct {
		plain
		ParentID json.RawMessage `json:"parentId"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*q = OutageQuestion(v.plain)
	q.ParentID = ""
	p := bytes.TrimSpace(v.ParentID)
	switch {
	case len(p) == 0 || bytes.Equal(p, []byte("null")):
	case p[0] == '"':
		return json.Unmarshal(p, &q.ParentID)
	default:
		q.ParentID = string(p) // undocumented type: keep the JSON text
	}
	return nil
}

// OutageQuestions returns the questions to answer when reporting an outage
// of the given type (see [SupportService.OutageTypes]).
//
// GET /support/outageTypes/{outageType}/outageQuestions.
func (s *SupportService) OutageQuestions(ctx context.Context, outageType string) ([]OutageQuestion, error) {
	return listAll[OutageQuestion](ctx, s.c, "support/outageTypes/{}/outageQuestions", outageType)
}

// ReportedOutage is an outage reported by the tenant.
type ReportedOutage struct {
	TenantID      string `json:"TenantId"`
	EnvironmentID string `json:"EnvironmentId"`
	CreatedDate   *Time  `json:"CreatedDate,omitempty"`
	// State is the state of the report.
	State string `json:"State"`
	// MSaaSTicketID and IcMTicketID are the support tickets of the report.
	MSaaSTicketID   string `json:"MsaasTicketId"`
	IcMTicketID     string `json:"IcmTicketId"`
	AppVersion      string `json:"AppVersion"`
	PlatformVersion string `json:"PlatformVersion"`
	OutageType      string `json:"OutageType"`
}

// ReportedOutages returns the outages reported for all environments of the
// tenant.
//
// GET /support/reportedoutages.
func (s *SupportService) ReportedOutages(ctx context.Context) ([]ReportedOutage, error) {
	return listAll[ReportedOutage](ctx, s.c, "support/reportedoutages")
}

// OutageAnswer answers an outage question.
type OutageAnswer struct {
	// ID is the ID of the question.
	ID     string `json:"id"`
	Answer string `json:"answer"`
}

// OutageReport reports that an environment is not accessible.
type OutageReport struct {
	// OutageType is the kind of outage (see [SupportService.OutageTypes]).
	// Required.
	OutageType string `json:"outageType"`
	// Answers answer the questions of the outage type.
	Answers []OutageAnswer `json:"outageQuestionAnswers"`
	// FirstName, LastName, Email and Phone are the contact for updates on
	// the report. Phone contains only digits; use 00 instead of + for the
	// international prefix.
	FirstName string `json:"firstName,omitempty"`
	LastName  string `json:"lastName,omitempty"`
	Email     string `json:"email,omitempty"`
	Phone     string `json:"phone,omitempty"`
	// AppVersion and PlatformVersion are the environment's versions, if
	// known.
	AppVersion      string `json:"appVersion,omitempty"`
	PlatformVersion string `json:"platformVersion,omitempty"`
}

// OutageReportStatus is the result of reporting an outage. Compare with
// [OutageReportStatus.Is].
type OutageReportStatus string

// Outage report statuses.
const (
	OutageReportUnknown         OutageReportStatus = "Unknown"
	OutageReportCreated         OutageReportStatus = "Created"
	OutageReportUpdatedExisting OutageReportStatus = "UpdatedExisting"
	OutageReportError           OutageReportStatus = "Error"
)

// Is reports whether s and other are the same status, ignoring case.
func (s OutageReportStatus) Is(other OutageReportStatus) bool {
	return strings.EqualFold(string(s), string(other))
}

// OutageReportResult is the response to an outage report.
type OutageReportResult struct {
	// CreationStatus tells whether a report was created, an existing one
	// updated, or the request failed (Error).
	CreationStatus OutageReportStatus `json:"creationStatus"`
	// MSaaSCaseNumber is the support case of the report.
	MSaaSCaseNumber string `json:"msaasCaseNumber"`
}

// ReportOutage reports that the environment family/environment is not
// accessible. CreationStatus is documented with the value Error; check it
// as well as the returned error. The request is not
// retried on server errors (unless ClientOptions.RetryNonIdempotent is set),
// so a failure does not report twice.
//
// POST /support/applications/{applicationFamily}/environments/{environmentName}/reportoutage.
func (s *SupportService) ReportOutage(ctx context.Context, family, environment string, report OutageReport) (*OutageReportResult, error) {
	if blank(report.OutageType) {
		return nil, errors.New("bcadmin: OutageReport.OutageType is required")
	}
	for _, r := range report.Phone {
		if r < '0' || r > '9' {
			return nil, errors.New("bcadmin: OutageReport.Phone must contain only digits (use 00 instead of +)")
		}
	}
	if report.Answers == nil {
		report.Answers = []OutageAnswer{}
	}
	var out OutageReportResult
	if err := s.c.call(ctx, http.MethodPost, "support/applications/{}/environments/{}/reportoutage",
		[]string{family, environment}, report, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SupportContact returns the environment's support contact; see
// [SupportService.Contact].
func (e *Environment) SupportContact(ctx context.Context) (*SupportContact, error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.c.Support().Contact(ctx, e.ApplicationFamily, e.Name)
}

// SetSupportContact sets the environment's support contact; see
// [SupportService.SetContact].
func (e *Environment) SetSupportContact(ctx context.Context, contact SupportContact) (*SupportContact, error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.c.Support().SetContact(ctx, e.ApplicationFamily, e.Name, contact)
}

// ReportOutage reports that the environment is not accessible; see
// [SupportService.ReportOutage].
func (e *Environment) ReportOutage(ctx context.Context, report OutageReport) (*OutageReportResult, error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.c.Support().ReportOutage(ctx, e.ApplicationFamily, e.Name, report)
}
