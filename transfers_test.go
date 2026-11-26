package bcadmin_test

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

const (
	sourceTenant = "00000000-0000-0000-0000-000000000001"
	destTenant   = "00000000-0000-0000-0000-000000000002"
)

func TestTransfers(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	requested := map[string]any{
		"id": "00000000-0000-0000-0000-000000000a01", "type": "requestTransferToAnotherEntraTenant", "status": "queued",
		"parameters": map[string]any{"destinationEntraTenantId": destTenant, "isAcceptedByDestinationEntraTenant": false},
	}
	srv.HandleJSON(http.MethodPost, "transfers/outgoing/applications/{family}/environments/{env}", http.StatusOK, requested)
	srv.HandleJSON(http.MethodDelete, "transfers/outgoing/applications/{family}/environments/{env}", http.StatusOK,
		map[string]any{"id": "00000000-0000-0000-0000-000000000a01", "status": "canceled", "completedOn": "2026-11-20T11:00:00Z"})
	serveFixture(t, srv, http.MethodGet, "transfers/outgoing", "testdata/transfers/outgoing.json")
	serveFixture(t, srv, http.MethodGet, "transfers/incoming/{tenant}", "testdata/transfers/outgoing.json")
	serveFixture(t, srv, http.MethodPost, "transfers/incoming", "testdata/transfers/accept.json")
	tr := srv.NewClient(t).Transfers()
	ctx := context.Background()

	runAt := time.Date(2026, 12, 1, 3, 0, 0, 123456789, time.FixedZone("CET", 3600))
	op, err := tr.RequestOutgoing(ctx, bc, "sb", bcadmin.OutgoingTransferOptions{DestinationEntraTenantID: destTenant, RunAt: runAt})
	if err != nil || op.ID != "00000000-0000-0000-0000-000000000a01" || !op.Type.Is(bcadmin.OperationTypeRequestTransfer) || !op.Status.Is(bcadmin.OperationStatusQueued) {
		t.Fatalf("RequestOutgoing() = %+v, %v", op, err)
	}
	if got, want := lastBody(t, srv), map[string]any{"DestinationEntraTenantId": destTenant, "RunAt": "2026-12-01T02:00:00Z"}; !reflect.DeepEqual(got, want) {
		t.Errorf("request body = %v, want %v", got, want)
	}
	findRequest(t, srv, http.MethodPost, "/admin/v2.29/transfers/outgoing/applications/BusinessCentral/environments/sb")

	op, err = tr.CancelOutgoing(ctx, bc, "sb")
	if err != nil || op == nil || op.ID != "00000000-0000-0000-0000-000000000a01" || !op.Status.Is(bcadmin.OperationStatusCanceled) || op.CompletedOn == nil {
		t.Errorf("CancelOutgoing() = %+v, %v", op, err)
	}
	if r := findRequest(t, srv, http.MethodDelete, "/admin/v2.29/transfers/outgoing/applications/BusinessCentral/environments/sb"); len(r.Body) != 0 {
		t.Errorf("cancel body = %q, want none", r.Body)
	}

	out, err := tr.Outgoing(ctx)
	if err != nil || len(out) != 1 || out[0].EnvironmentName != "sb" || out[0].Parameters["destinationEntraTenantId"] != destTenant ||
		out[0].Parameters["isAcceptedByDestinationEntraTenant"] != false {
		t.Errorf("Outgoing() = %+v, %v", out, err)
	}
	in, err := tr.Incoming(ctx, sourceTenant)
	if err != nil || len(in) != 1 || in[0].ID != "00000000-0000-0000-0000-000000000a01" || in[0].EnvironmentName != "sb" ||
		in[0].Parameters["sourceEntraTenantId"] != sourceTenant {
		t.Errorf("Incoming() = %+v, %v", in, err)
	}
	findRequest(t, srv, http.MethodGet, "/admin/v2.29/transfers/incoming/"+sourceTenant)

	accepted, err := tr.AcceptIncoming(ctx, bcadmin.IncomingTransfer{
		SourceEntraTenantID: sourceTenant, ApplicationFamily: bc, SourceEnvironmentName: "sb", DestinationEnvironmentName: "sb-moved",
	})
	if err != nil || !accepted.Type.Is(bcadmin.OperationTypeMoveToAnotherAadTenant) || accepted.Parameters["destinationEnvironmentName"] != "sb-moved" {
		t.Fatalf("AcceptIncoming() = %+v, %v", accepted, err)
	}
	want := map[string]any{"SourceEntraTenantId": sourceTenant, "ApplicationFamily": "BusinessCentral", "SourceEnvironmentName": "sb", "DestinationEnvironmentName": "sb-moved"}
	if got := lastBody(t, srv); !reflect.DeepEqual(got, want) {
		t.Errorf("accept body = %v, want %v", got, want)
	}
	findRequest(t, srv, http.MethodPost, "/admin/v2.29/transfers/incoming")
}

func TestTransfersValidation(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	tr := srv.NewClient(t).Transfers()
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"no destination": func() error {
			_, err := tr.RequestOutgoing(ctx, bc, "sb", bcadmin.OutgoingTransferOptions{RunAt: time.Now()})
			return err
		},
		"no run time": func() error {
			_, err := tr.RequestOutgoing(ctx, bc, "sb", bcadmin.OutgoingTransferOptions{DestinationEntraTenantID: destTenant})
			return err
		},
		"incomplete accept": func() error {
			_, err := tr.AcceptIncoming(ctx, bcadmin.IncomingTransfer{SourceEntraTenantID: sourceTenant, ApplicationFamily: bc, SourceEnvironmentName: "sb"})
			return err
		},
		"empty source tenant": func() error { _, err := tr.Incoming(ctx, ""); return err },
		"source tenant path":  func() error { _, err := tr.Incoming(ctx, "../outgoing"); return err },
		"blank source tenant": func() error { _, err := tr.Incoming(ctx, " "); return err },
	} {
		if call() == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("invalid calls sent %d requests", n)
	}

	old := bcadmintest.NewServer(t, bcadmintest.WithAPIVersion("v2.19"))
	oldTr := old.NewClient(t).Transfers()
	for name, call := range map[string]func() error{
		"RequestOutgoing": func() error {
			_, err := oldTr.RequestOutgoing(ctx, bc, "sb", bcadmin.OutgoingTransferOptions{DestinationEntraTenantID: destTenant, RunAt: time.Now()})
			return err
		},
		"CancelOutgoing": func() error { _, err := oldTr.CancelOutgoing(ctx, bc, "sb"); return err },
		"Outgoing":       func() error { _, err := oldTr.Outgoing(ctx); return err },
		"Incoming":       func() error { _, err := oldTr.Incoming(ctx, sourceTenant); return err },
		"AcceptIncoming": func() error {
			_, err := oldTr.AcceptIncoming(ctx, bcadmin.IncomingTransfer{SourceEntraTenantID: sourceTenant, ApplicationFamily: bc, SourceEnvironmentName: "a", DestinationEnvironmentName: "b"})
			return err
		},
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "v2.20") {
			t.Errorf("%s on v2.19: %v", name, err)
		}
	}
	if n := len(old.Requests()); n != 0 {
		t.Errorf("v2.19 calls sent %d requests", n)
	}
}

func TestTransferRequestNotRetried(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	for _, p := range []string{"transfers/outgoing/applications/{family}/environments/{env}", "transfers/incoming"} {
		srv.Handle(http.MethodPost, p, func(w http.ResponseWriter, _ *http.Request) {
			bcadmintest.WriteError(w, http.StatusInternalServerError, "Unknown", "failed")
		})
	}
	tr := srv.NewClient(t).Transfers()
	ctx := context.Background()
	if _, err := tr.RequestOutgoing(ctx, bc, "sb", bcadmin.OutgoingTransferOptions{DestinationEntraTenantID: destTenant, RunAt: time.Now()}); bcadmin.ErrorCode(err) != "Unknown" {
		t.Errorf("RequestOutgoing error = %v", err)
	}
	if _, err := tr.AcceptIncoming(ctx, bcadmin.IncomingTransfer{SourceEntraTenantID: sourceTenant, ApplicationFamily: bc, SourceEnvironmentName: "a", DestinationEnvironmentName: "b"}); bcadmin.ErrorCode(err) != "Unknown" {
		t.Errorf("AcceptIncoming error = %v", err)
	}
	if n := len(srv.Requests()); n != 2 {
		t.Errorf("sent %d requests, want 2", n)
	}
}

func TestCancelTransferEmptyResponse(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	fake := srv.EnableEnvironments()
	fake.Add(bcadmin.Environment{Name: "sb", Type: bcadmin.EnvironmentTypeSandbox})
	srv.Handle(http.MethodDelete, "transfers/outgoing/applications/{family}/environments/{env}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv.HandleJSON(http.MethodPost, "transfers/outgoing/applications/{family}/environments/{env}", http.StatusOK, map[string]any{"id": "x"})
	ctx := context.Background()
	env, err := srv.NewClient(t).Environments().Get(ctx, bc, "sb")
	if err != nil {
		t.Fatal(err)
	}
	if op, err := env.CancelTransfer(ctx); err != nil || op != nil {
		t.Errorf("CancelTransfer() = %+v, %v; want nil, nil", op, err)
	}
	if op, err := env.RequestTransfer(ctx, bcadmin.OutgoingTransferOptions{DestinationEntraTenantID: destTenant, RunAt: time.Now()}); err != nil || op.ID != "x" {
		t.Errorf("RequestTransfer() = %+v, %v", op, err)
	}
	findRequest(t, srv, http.MethodPost, "/admin/v2.29/transfers/outgoing/applications/BusinessCentral/environments/sb")

	var unbound bcadmin.Environment
	if _, err := unbound.CancelTransfer(ctx); err == nil {
		t.Error("CancelTransfer of an unbound environment: no error")
	}
}

// TestTransferAcceptedWithoutOperation checks that an accepted transfer
// request is not reported as an error when its 2xx body is not a readable
// operation.
func TestTransferAcceptedWithoutOperation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, write := range map[string]func(http.ResponseWriter){
		"empty":     func(w http.ResponseWriter) { w.WriteHeader(http.StatusAccepted) },
		"not json":  func(w http.ResponseWriter) { _, _ = w.Write([]byte("Accepted")) },
		"too large": func(w http.ResponseWriter) { _, _ = w.Write([]byte(strings.Repeat("x", 4096))) },
	} {
		srv := bcadmintest.NewServer(t)
		for _, p := range []string{"transfers/outgoing/applications/{family}/environments/{env}", "transfers/incoming"} {
			srv.Handle(http.MethodPost, p, func(w http.ResponseWriter, _ *http.Request) { write(w) })
		}
		srv.Handle(http.MethodDelete, "transfers/outgoing/applications/{family}/environments/{env}", func(w http.ResponseWriter, _ *http.Request) { write(w) })
		o := srv.ClientOptions()
		o.MaxResponseBytes = 1024
		c, err := bcadmin.NewClient(bcadmintest.Credential(), o)
		if err != nil {
			t.Fatal(err)
		}
		tr := c.Transfers()
		if op, err := tr.RequestOutgoing(ctx, bc, "sb", bcadmin.OutgoingTransferOptions{DestinationEntraTenantID: destTenant, RunAt: time.Now()}); err != nil || op != nil {
			t.Errorf("%s: RequestOutgoing() = %+v, %v", name, op, err)
		}
		if op, err := tr.AcceptIncoming(ctx, bcadmin.IncomingTransfer{SourceEntraTenantID: sourceTenant, ApplicationFamily: bc, SourceEnvironmentName: "a", DestinationEnvironmentName: "b"}); err != nil || op != nil {
			t.Errorf("%s: AcceptIncoming() = %+v, %v", name, op, err)
		}
		if op, err := tr.CancelOutgoing(ctx, bc, "sb"); err != nil || op != nil {
			t.Errorf("%s: CancelOutgoing() = %+v, %v", name, op, err)
		}
		if n := len(srv.Requests()); n != 3 {
			t.Errorf("%s: sent %d requests, want 3", name, n)
		}
	}
}
