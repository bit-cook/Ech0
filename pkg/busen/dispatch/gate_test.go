// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package dispatch_test

import (
	"context"
	"testing"

	"github.com/lin-snow/ech0/pkg/busen/dispatch"
)

func TestGateEnterClosedTransitions(t *testing.T) {
	g := dispatch.NewGate()

	if g.Closed() {
		t.Fatalf("new gate should be open")
	}
	if !g.Enter() {
		t.Fatalf("Enter on open gate should succeed")
	}
	g.Leave()

	g.Close()
	if !g.Closed() {
		t.Fatalf("gate should report closed after Close")
	}
	if g.Enter() {
		t.Fatalf("Enter after Close must return false")
	}
}

func TestGateWaitImmediateWhenIdle(t *testing.T) {
	g := dispatch.NewGate()

	if err := g.Wait(context.Background()); err != nil {
		t.Fatalf("Wait on idle gate = %v, want nil", err)
	}
}

func TestGateWaitUnblocksWhenActiveReachesZero(t *testing.T) {
	g := dispatch.NewGate()
	if !g.Enter() {
		t.Fatalf("Enter should succeed")
	}

	done := make(chan error, 1)
	go func() {
		done <- g.Wait(context.Background())
	}()

	// Releasing the only in-flight op drives active to zero and closes idle,
	// which unblocks the waiter regardless of goroutine scheduling order.
	g.Leave()

	if err := <-done; err != nil {
		t.Fatalf("Wait after Leave = %v, want nil", err)
	}
}

func TestGateWaitReturnsCtxErrOnCancel(t *testing.T) {
	g := dispatch.NewGate()
	if !g.Enter() {
		t.Fatalf("Enter should succeed")
	}
	// Intentionally never Leave: idle stays open so only ctx cancellation can
	// release Wait.

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- g.Wait(ctx)
	}()

	cancel()

	if err := <-done; err != context.Canceled {
		t.Fatalf("Wait after cancel = %v, want context.Canceled", err)
	}
}

func TestGateActiveCountGatesIdle(t *testing.T) {
	g := dispatch.NewGate()

	if !g.Enter() {
		t.Fatalf("first Enter should succeed")
	}
	if !g.Enter() {
		t.Fatalf("second Enter should succeed")
	}

	// With active == 2 the gate is non-idle: an already-canceled context wins
	// deterministically because idle is not yet closed.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.Wait(canceled); err != context.Canceled {
		t.Fatalf("Wait while active = %v, want context.Canceled", err)
	}

	g.Leave() // active 2 -> 1, still non-idle
	if err := g.Wait(canceled); err != context.Canceled {
		t.Fatalf("Wait with one op still in flight = %v, want context.Canceled", err)
	}

	g.Leave() // active 1 -> 0, idle closes
	if err := g.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after all ops drained = %v, want nil", err)
	}
}

// TestGateLeaveUnderflowIsSafeNoop verifies that an unmatched / surplus Leave is a
// safe no-op: it neither drives active negative nor double-closes the already-closed
// idle channel (which previously panicked with "close of closed channel"). It also
// confirms the gate is still usable afterwards — the idle channel must not be left
// in a broken state.
func TestGateLeaveUnderflowIsSafeNoop(t *testing.T) {
	g := dispatch.NewGate()

	// Several surplus Leave calls on a fresh (idle) gate must not panic.
	for range 3 {
		g.Leave()
	}

	// Still idle: Wait returns immediately.
	if err := g.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after no-op Leave = %v, want nil", err)
	}

	// A subsequent normal Enter/Leave cycle still works (idle was not corrupted).
	if !g.Enter() {
		t.Fatalf("Enter after no-op Leave should succeed")
	}
	g.Leave()
	if err := g.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after Enter/Leave cycle = %v, want nil", err)
	}

	// A surplus Leave after a completed cycle is also a no-op.
	g.Leave()
	if err := g.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after post-cycle surplus Leave = %v, want nil", err)
	}
}
