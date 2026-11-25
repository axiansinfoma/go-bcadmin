package bcadmin

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// WaitOptions configures [Poller.Wait]. A nil *WaitOptions selects the
// defaults.
type WaitOptions struct {
	// Frequency is the interval between two status requests. The default is
	// ClientOptions.PollFrequency.
	Frequency time.Duration

	// TreatScheduledAsDone makes Wait return [ErrOperationScheduled] as soon
	// as the operation has the status Scheduled, instead of waiting until the
	// scheduled operation has run (which can take until the next update
	// window or update).
	TreatScheduledAsDone bool

	// UntilEnvironmentActive makes Wait, for pollers whose result is an
	// environment (create, copy, recover, rename, restore), keep polling the
	// environment after the operation succeeded until its status is Active.
	// It fails when the environment reaches a failed status. Pollers of other
	// operations ignore it. The environment may stay in a non-final status
	// for a long time, so give the context a deadline.
	UntilEnvironmentActive bool
}

// Poller tracks a long-running operation. T is the type of the result, for
// example *Environment for an environment creation or *Operation for app
// operations. A Poller is not safe for concurrent use.
type Poller[T any] struct {
	c      *Client
	op     Operation
	fetch  func(ctx context.Context) (*Operation, error)
	result func(ctx context.Context, op *Operation, opts *WaitOptions) (T, error)

	resultDone   bool
	resultActive bool // the cached result was built with UntilEnvironmentActive
	res          T
}

// newPoller returns a poller for op. fetch returns the current state of the
// operation; result builds the result of a succeeded operation.
func newPoller[T any](c *Client, op *Operation,
	fetch func(ctx context.Context) (*Operation, error),
	result func(ctx context.Context, op *Operation, opts *WaitOptions) (T, error),
) *Poller[T] {
	p := &Poller[T]{c: c, fetch: fetch, result: result}
	if op != nil {
		p.op = *op
	}
	return p
}

// ID returns the operation ID.
func (p *Poller[T]) ID() string { return p.op.ID }

// Operation returns a copy of the operation as last returned by the API.
func (p *Poller[T]) Operation() *Operation {
	op := p.op
	return &op
}

// Done reports whether the operation has reached a final status.
func (p *Poller[T]) Done() bool { return p.op.Status.Terminal() }

// Scheduled reports whether the operation is scheduled to run later.
func (p *Poller[T]) Scheduled() bool { return p.op.Status.Is(OperationStatusScheduled) }

// Poll requests the current state of the operation once and returns it.
// After the operation is done, Poll returns the final state without sending
// a request.
func (p *Poller[T]) Poll(ctx context.Context) (*Operation, error) {
	if p.Done() {
		return p.Operation(), nil
	}
	op, err := p.fetch(ctx)
	if err != nil {
		return nil, err
	}
	if op.ID == "" {
		op.ID = p.op.ID
	}
	p.op = *op
	return p.Operation(), nil
}

// Result returns the result of a finished operation. It returns an
// [*OperationError] when the operation failed or was canceled, and an error
// when the operation is not done yet. A Skipped operation returns its result
// without an error; check Operation().Status to tell it from Succeeded.
func (p *Poller[T]) Result(ctx context.Context) (T, error) {
	return p.resultWith(ctx, nil)
}

func (p *Poller[T]) resultWith(ctx context.Context, opts *WaitOptions) (T, error) {
	var zero T
	if !p.Done() {
		return zero, fmt.Errorf("bcadmin: operation %s has not finished, status %q", clean(p.op.ID), p.op.Status)
	}
	if p.op.Status.Is(OperationStatusFailed) || p.op.Status.Is(OperationStatusCanceled) {
		return zero, &OperationError{Operation: p.Operation()}
	}
	wantActive := opts != nil && opts.UntilEnvironmentActive
	if p.resultDone && (p.resultActive || !wantActive) {
		return p.res, nil
	}
	res, err := p.result(ctx, p.Operation(), opts)
	if err != nil {
		// Do not cache errors of the result request; it can be retried.
		return zero, err
	}
	p.res, p.resultDone, p.resultActive = res, true, wantActive
	return res, nil
}

// Wait polls the operation until it is done and returns its result.
//
// It returns an [*OperationError] when the operation failed or was canceled
// (a Skipped operation is not an error, see [Poller.Result]),
// [ErrOperationScheduled] when opts.TreatScheduledAsDone is set and the
// operation is scheduled, and an error wrapping ctx.Err() (and the context's
// cause, if one was set) when ctx is done first.
//
// Wait keeps polling as long as the operation reports a non-final or unknown
// status, so give ctx a deadline. When ctx ends first, the operation keeps
// running in Business Central; it can be resumed later with its ID.
func (p *Poller[T]) Wait(ctx context.Context, opts *WaitOptions) (T, error) {
	var zero T
	freq := p.c.pollFrequency
	if opts != nil && opts.Frequency > 0 {
		freq = opts.Frequency
	}
	scheduledIsDone := opts != nil && opts.TreatScheduledAsDone

	// A resumed poller does not know the status yet; poll right away.
	if p.op.Status == "" {
		if err := p.pollOnce(ctx); err != nil {
			return zero, err
		}
	}
	timer := time.NewTimer(freq)
	defer timer.Stop()
	for {
		if p.Done() {
			return p.resultWith(ctx, opts)
		}
		if scheduledIsDone && p.Scheduled() {
			return zero, fmt.Errorf("%w: operation %s", ErrOperationScheduled, clean(p.op.ID))
		}
		timer.Reset(freq)
		select {
		case <-ctx.Done():
			return zero, p.ctxError(ctx)
		case <-timer.C:
		}
		if err := p.pollOnce(ctx); err != nil {
			return zero, err
		}
	}
}

// pollOnce polls and maps an error caused by ctx to the error of ctxError.
func (p *Poller[T]) pollOnce(ctx context.Context) error {
	_, err := p.Poll(ctx)
	if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return p.ctxError(ctx)
	}
	return err
}

// ctxError returns the error for a done ctx. It wraps ctx.Err() and, when it
// differs, the context's cause.
func (p *Poller[T]) ctxError(ctx context.Context) error {
	return waitError(ctx, "operation "+p.op.ID)
}

// waitError returns the error for a done ctx while waiting for what. It wraps
// ctx.Err() and, when it differs, the context's cause.
func waitError(ctx context.Context, what string) error {
	err, cause := ctx.Err(), context.Cause(ctx)
	if cause != nil && !errors.Is(cause, err) {
		return fmt.Errorf("bcadmin: waiting for %s: %w (cause: %w)", clean(what), err, cause)
	}
	return fmt.Errorf("bcadmin: waiting for %s: %w", clean(what), err)
}
