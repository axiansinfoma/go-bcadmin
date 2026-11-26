package bcadmin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

func TestSupport(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	contact := map[string]any{"name": "Contoso Support", "email": "support@contoso.com", "url": "https://support.contoso.com"}
	srv.HandleJSON(http.MethodGet, "support/applications/{family}/environments/{env}/supportcontact", http.StatusOK, contact)
	srv.HandleJSON(http.MethodPut, "support/applications/{family}/environments/{env}/supportcontact", http.StatusOK, contact)
	srv.HandleJSON(http.MethodGet, "support/outageTypes", http.StatusOK, map[string]any{"value": []map[string]any{
		{"outageType": "EnvironmentInaccessible", "name": "Environment inaccessible", "description": "The environment can't be opened."},
	}})
	serveFixture(t, srv, http.MethodGet, "support/outageTypes/{type}/outageQuestions", "testdata/support/outagequestions.json")
	serveFixture(t, srv, http.MethodGet, "support/reportedoutages", "testdata/support/reportedoutages.json")
	srv.HandleJSON(http.MethodPost, "support/applications/{family}/environments/{env}/reportoutage", http.StatusOK,
		map[string]any{"creationStatus": "Created", "msaasCaseNumber": "2611200010000002"})
	sup := srv.NewClient(t).Support()
	ctx := context.Background()

	c, err := sup.Contact(ctx, bc, "sb")
	if err != nil || c.Name != "Contoso Support" || c.URL != "https://support.contoso.com" {
		t.Errorf("Contact() = %+v, %v", c, err)
	}
	if _, err := sup.SetContact(ctx, bc, "sb", bcadmin.SupportContact{Name: "Contoso Support", Email: "support@contoso.com"}); err != nil {
		t.Fatal(err)
	}
	if got, want := lastBody(t, srv), map[string]any{"name": "Contoso Support", "email": "support@contoso.com", "url": ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("contact body = %v, want %v", got, want)
	}

	types, err := sup.OutageTypes(ctx)
	if err != nil || len(types) != 1 || types[0].OutageType != "EnvironmentInaccessible" {
		t.Errorf("OutageTypes() = %+v, %v", types, err)
	}
	qs, err := sup.OutageQuestions(ctx, "EnvironmentInaccessible")
	if err != nil || len(qs) != 3 {
		t.Fatalf("OutageQuestions() = %+v, %v", qs, err)
	}
	if qs[0].ParentID != "" || qs[0].DefaultValue != false || !qs[0].QuestionType.Is(bcadmin.OutageQuestionToggle) || !qs[0].Required ||
		qs[1].ParentID != "isProduction" || !qs[1].Multiline || qs[2].ParentID != "1" || qs[2].DefaultValue != nil {
		t.Errorf("questions = %+v", qs)
	}
	findRequest(t, srv, http.MethodGet, "/admin/v2.29/support/outageTypes/EnvironmentInaccessible/outageQuestions")

	reported, err := sup.ReportedOutages(ctx)
	if err != nil || len(reported) != 1 || reported[0].MSaaSTicketID != "2611200010000001" || reported[0].CreatedDate == nil || reported[0].OutageType != "EnvironmentInaccessible" {
		t.Errorf("ReportedOutages() = %+v, %v", reported, err)
	}

	res, err := sup.ReportOutage(ctx, bc, "sb", bcadmin.OutageReport{OutageType: "EnvironmentInaccessible", Phone: "0049301234567"})
	if err != nil || !res.CreationStatus.Is(bcadmin.OutageReportCreated) || res.MSaaSCaseNumber != "2611200010000002" {
		t.Fatalf("ReportOutage() = %+v, %v", res, err)
	}
	want := map[string]any{"outageType": "EnvironmentInaccessible", "outageQuestionAnswers": []any{}, "phone": "0049301234567"}
	if got := lastBody(t, srv); !reflect.DeepEqual(got, want) {
		t.Errorf("report body = %v, want %v", got, want)
	}
	count := len(srv.Requests())
	if _, err := sup.ReportOutage(ctx, bc, "sb", bcadmin.OutageReport{}); err == nil {
		t.Error("no outage type: no error")
	}
	if _, err := sup.ReportOutage(ctx, bc, "sb", bcadmin.OutageReport{OutageType: "x", Phone: "+49 30"}); err == nil {
		t.Error("phone with +: no error")
	}
	if got := len(srv.Requests()); got != count {
		t.Errorf("invalid reports sent %d requests", got-count)
	}
}

func TestEnvironmentExportAndSupportMethods(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var unbound bcadmin.Environment
	if _, err := unbound.ExportMetrics(ctx); err == nil {
		t.Error("ExportMetrics of an unbound environment: no error")
	}
	if _, err := unbound.ReportOutage(ctx, bcadmin.OutageReport{OutageType: "x"}); err == nil {
		t.Error("ReportOutage of an unbound environment: no error")
	}

	srv := bcadmintest.NewServer(t)
	fake := srv.EnableEnvironments()
	fake.Add(bcadmin.Environment{Name: "sb", Type: bcadmin.EnvironmentTypeSandbox})
	srv.HandleJSON(http.MethodGet, "exports/applications/{family}/environments/{env}/metrics", http.StatusOK, map[string]any{"exportsPerMonth": 10})
	srv.HandleJSON(http.MethodGet, "support/applications/{family}/environments/{env}/supportcontact", http.StatusOK, map[string]any{"name": "N"})
	env, err := srv.NewClient(t).Environments().Get(ctx, bc, "sb")
	if err != nil {
		t.Fatal(err)
	}
	if m, err := env.ExportMetrics(ctx); err != nil || m.ExportsPerMonth != 10 {
		t.Errorf("ExportMetrics() = %+v, %v", m, err)
	}
	if c, err := env.SupportContact(ctx); err != nil || c.Name != "N" {
		t.Errorf("SupportContact() = %+v, %v", c, err)
	}
}

func TestOutageQuestionUnexpectedParentID(t *testing.T) {
	t.Parallel()
	var qs []bcadmin.OutageQuestion
	if err := json.Unmarshal([]byte(`[{"id":"a","parentId":true},{"id":"b","parentId":{"x":1}},{"id":"c","parentId":-1.5e3}]`), &qs); err != nil {
		t.Fatal(err)
	}
	if qs[0].ParentID != "true" || qs[1].ParentID != `{"x":1}` || qs[2].ParentID != "-1.5e3" {
		t.Errorf("parent IDs = %q, %q, %q", qs[0].ParentID, qs[1].ParentID, qs[2].ParentID)
	}
}

func TestEnvironmentShortcutsSendToTheEnvironment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := bcadmintest.NewServer(t)
	fake := srv.EnableEnvironments()
	fake.Add(bcadmin.Environment{Name: "uat", Type: bcadmin.EnvironmentTypeSandbox})
	srv.HandleJSON(http.MethodPost, "exports/applications/{family}/environments/{env}", http.StatusOK, nil)
	srv.HandleJSON(http.MethodPut, "support/applications/{family}/environments/{env}/supportcontact", http.StatusOK, map[string]any{"name": "N"})
	srv.HandleJSON(http.MethodPost, "support/applications/{family}/environments/{env}/reportoutage", http.StatusOK, map[string]any{"creationStatus": "Created"})
	env, err := srv.NewClient(t).Environments().Get(ctx, bc, "uat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.ExportDatabase(ctx, bcadmin.ExportOptions{StorageAccountSASURI: sasURI, Container: "c", Blob: "b"}); err != nil {
		t.Fatal(err)
	}
	findRequest(t, srv, http.MethodPost, "/admin/v2.29/exports/applications/BusinessCentral/environments/uat")
	if _, err := env.SetSupportContact(ctx, bcadmin.SupportContact{Name: "N"}); err != nil {
		t.Fatal(err)
	}
	findRequest(t, srv, http.MethodPut, "/admin/v2.29/support/applications/BusinessCentral/environments/uat/supportcontact")
	if _, err := env.ReportOutage(ctx, bcadmin.OutageReport{OutageType: "x"}); err != nil {
		t.Fatal(err)
	}
	findRequest(t, srv, http.MethodPost, "/admin/v2.29/support/applications/BusinessCentral/environments/uat/reportoutage")
}
