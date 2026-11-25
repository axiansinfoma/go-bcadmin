package bcadmin_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

// serveOperation registers a handler for GET environments/operations/{id}
// that returns the statuses in order and repeats the last one.
func serveOperation(srv *bcadmintest.Server, id string, statuses ...string) *atomic.Int32 {
	var calls atomic.Int32
	srv.Handle(http.MethodGet, "environments/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != id {
			bcadmintest.WriteError(w, http.StatusNotFound, "OperationNotFound", "no such operation")
			return
		}
		n := int(calls.Add(1)) - 1
		status := statuses[min(n, len(statuses)-1)]
		op := map[string]any{"id": id, "type": "copy", "status": status}
		if strings.EqualFold(status, "failed") {
			op["errorMessage"] = "The source environment is being updated."
		}
		bcadmintest.WriteJSON(w, http.StatusOK, op)
	})
	return &calls
}

func TestPollerWaitSucceeds(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	calls := serveOperation(srv, "op-1", "queued", "Running", "SUCCEEDED")
	p, err := srv.NewClient(t).Tenant().ResumeOperation("op-1")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID() != "op-1" || p.Done() {
		t.Fatalf("new poller: ID %q, Done %v", p.ID(), p.Done())
	}
	if _, err := p.Result(context.Background()); err == nil {
		t.Error("Result() before done did not fail")
	}
	op, err := p.Wait(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !op.Status.Is(bcadmin.OperationStatusSucceeded) || !op.Type.Is(bcadmin.OperationTypeCopy) {
		t.Errorf("op = %+v", op)
	}
	if calls.Load() != 3 {
		t.Errorf("polls = %d, want 3", calls.Load())
	}
	// Done pollers do not send more requests.
	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res, err := p.Result(context.Background()); err != nil || res.ID != "op-1" {
		t.Errorf("Result() = %v, %v", res, err)
	}
	if calls.Load() != 3 {
		t.Errorf("polls after done = %d, want 3", calls.Load())
	}
}

func TestPollerWaitFails(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"Failed", "canceled", "Cancelled"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			srv := bcadmintest.NewServer(t)
			serveOperation(srv, "op-2", "Running", status)
			p, err := srv.NewClient(t).Tenant().ResumeOperation("op-2")
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Wait(context.Background(), &bcadmin.WaitOptions{Frequency: time.Millisecond})
			oe, ok := errors.AsType[*bcadmin.OperationError](err)
			if !ok {
				t.Fatalf("error = %v, want *OperationError", err)
			}
			if oe.Operation.ID != "op-2" {
				t.Errorf("Operation = %+v", oe.Operation)
			}
			if status == "Failed" {
				if oe.Canceled() || !strings.Contains(err.Error(), "The source environment is being updated.") {
					t.Errorf("error = %v", err)
				}
			} else if !oe.Canceled() {
				t.Errorf("Canceled() = false for %s", status)
			}
		})
	}
}

func TestPollerScheduled(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	serveOperation(srv, "op-3", "Queued", "Scheduled")
	p, err := srv.NewClient(t).Tenant().ResumeOperation("op-3")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Wait(context.Background(), &bcadmin.WaitOptions{TreatScheduledAsDone: true})
	if !errors.Is(err, bcadmin.ErrOperationScheduled) || !p.Scheduled() {
		t.Errorf("Wait() error = %v, Scheduled() = %v", err, p.Scheduled())
	}
}

func TestPollerContext(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	calls := serveOperation(srv, "op-4", "Running")
	p, err := srv.NewClient(t).Tenant().ResumeOperation("op-4")
	if err != nil {
		t.Fatal(err)
	}
	// Cancel with a cause once the operation was polled twice, independent of
	// how long a request takes on the test machine.
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	stop := errors.New("test stop")
	go func() {
		for calls.Load() < 2 && ctx.Err() == nil {
			time.Sleep(time.Millisecond)
		}
		cancel(stop)
	}()
	_, err = p.Wait(ctx, &bcadmin.WaitOptions{Frequency: time.Millisecond})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, stop) || !strings.Contains(err.Error(), "op-4") {
		t.Errorf("Wait() error = %v, want context.Canceled and the cause", err)
	}
	if p.Done() || !p.Operation().Status.Is(bcadmin.OperationStatusRunning) {
		t.Errorf("poller state = %+v", p.Operation())
	}

	// A context that is already done fails the first poll of a resumed poller
	// with the same error.
	p2, err := srv.NewClient(t).Tenant().ResumeOperation("op-4")
	if err != nil {
		t.Fatal(err)
	}
	done, cancel2 := context.WithTimeout(context.Background(), -time.Second)
	defer cancel2()
	if _, err := p2.Wait(done, nil); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "waiting for operation op-4") {
		t.Errorf("Wait() error = %v, want DeadlineExceeded", err)
	}
}

func TestPollerPollError(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	p, err := srv.NewClient(t).Tenant().ResumeOperation("missing")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Wait(context.Background(), nil)
	if !bcadmin.IsNotFound(err) {
		t.Errorf("Wait() error = %v, want not found", err)
	}
	if _, err := srv.NewClient(t).Tenant().ResumeOperation("../x"); err == nil {
		t.Error("ResumeOperation with invalid ID did not fail")
	}
}

func TestOperationStatus(t *testing.T) {
	t.Parallel()
	terminal := []bcadmin.OperationStatus{"succeeded", "Failed", "CANCELED", "cancelled", "Skipped"}
	for _, s := range terminal {
		if !s.Terminal() {
			t.Errorf("%q.Terminal() = false", s)
		}
	}
	for _, s := range []bcadmin.OperationStatus{"", "queued", "Running", "Scheduled", "SomethingNew"} {
		if s.Terminal() {
			t.Errorf("%q.Terminal() = true", s)
		}
	}
}
