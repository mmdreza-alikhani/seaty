package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentHoldHasSingleWinner(t *testing.T) {
	s := NewStore(nil)
	const requests = 250
	seat := "C5"

	start := make(chan struct{})
	var wg sync.WaitGroup
	var wins int32
	var winnerHold, winnerSession string
	var winMu sync.Mutex

	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			session := fmt.Sprintf("concurrent-%d", i)
			h, err := s.Hold(session, []string{seat})
			if err == nil {
				atomic.AddInt32(&wins, 1)
				winMu.Lock()
				winnerHold = h.ID
				winnerSession = session
				winMu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&wins); got != 1 {
		t.Fatalf("expected exactly 1 successful hold, got %d", got)
	}
	if winnerHold == "" {
		t.Fatal("winner hold id is empty")
	}
	if err := s.Release(winnerHold, winnerSession); err != nil {
		t.Fatalf("release: %v", err)
	}
	state := s.Snapshot("observer")
	for _, view := range state.Seats {
		if view.ID == seat && view.Status != SeatAvailable {
			t.Fatalf("seat %s should be available after release, got %s", seat, view.Status)
		}
	}
}

func TestHoldIsAllOrNothing(t *testing.T) {
	s := NewStore([]string{"A2"})
	if _, err := s.Hold("sess-1", []string{"A1", "A2"}); err == nil {
		t.Fatal("expected conflict for already sold seat")
	}
	state := s.Snapshot("sess-1")
	for _, seat := range state.Seats {
		if seat.ID == "A1" && seat.Status != SeatAvailable {
			t.Fatalf("A1 leaked into %s after failed multi-seat hold", seat.Status)
		}
	}
}

func TestExpiredHoldReleasesSeat(t *testing.T) {
	s := NewStore(nil)
	now := time.Date(2026, 11, 12, 21, 0, 0, 0, time.UTC)
	s.clock = func() time.Time { return now }

	if _, err := s.Hold("sess-exp", []string{"B3"}); err != nil {
		t.Fatalf("hold: %v", err)
	}
	now = now.Add(holdTTL + time.Second)

	state := s.Snapshot("sess-exp")
	if state.Hold != nil {
		t.Fatal("expired hold should not be returned")
	}
	for _, seat := range state.Seats {
		if seat.ID == "B3" && seat.Status != SeatAvailable {
			t.Fatalf("expected B3 available after expiry, got %s", seat.Status)
		}
	}
}

func TestPaymentCommitsBookingAndIsIdempotent(t *testing.T) {
	s := NewStore(nil)
	hold, err := s.Hold("sess-pay", []string{"D4", "D5"})
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if _, err := s.BeginPayment(hold.ID, "sess-pay"); err != nil {
		t.Fatalf("begin payment: %v", err)
	}
	booking, err := s.CompletePayment(hold.ID, "sess-pay", true)
	if err != nil {
		t.Fatalf("complete payment: %v", err)
	}
	if booking.Amount != hold.Total {
		t.Fatalf("booking total = %d, want %d", booking.Amount, hold.Total)
	}

	again, err := s.CompletePayment(hold.ID, "sess-pay", true)
	if err != nil {
		t.Fatalf("idempotent complete: %v", err)
	}
	if again.ID != booking.ID {
		t.Fatalf("retry created a second booking: %s vs %s", again.ID, booking.ID)
	}

	if _, err := s.Hold("other", []string{"D4"}); err == nil {
		t.Fatal("booked seat must not be holdable")
	}
	state := s.Snapshot("sess-pay")
	for _, seat := range state.Seats {
		if seat.ID == "D4" && seat.Status != SeatBooked {
			t.Fatalf("D4 status = %s, want booked", seat.Status)
		}
	}
}

func TestFailedPaymentKeepsSeatHeld(t *testing.T) {
	s := NewStore(nil)
	hold, err := s.Hold("sess-fail", []string{"E7"})
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if _, err := s.BeginPayment(hold.ID, "sess-fail"); err != nil {
		t.Fatalf("begin payment: %v", err)
	}
	if _, err := s.CompletePayment(hold.ID, "sess-fail", false); err == nil {
		t.Fatal("expected payment_failed error")
	}
	state := s.Snapshot("sess-fail")
	if state.Hold == nil {
		t.Fatal("hold should survive a failed payment")
	}
	for _, seat := range state.Seats {
		if seat.ID == "E7" && seat.Status != SeatHeld {
			t.Fatalf("E7 status = %s, want held", seat.Status)
		}
	}
}

func TestStressExperimentHasSingleWinner(t *testing.T) {
	s := NewStore(nil)
	result, err := s.Stress("F4", 150)
	if err != nil {
		t.Fatalf("stress: %v", err)
	}
	if result.Requests != 150 {
		t.Fatalf("requests = %d", result.Requests)
	}
	if result.Success != 1 {
		t.Fatalf("expected exactly 1 success, got %d", result.Success)
	}
	if result.Conflicts != 149 {
		t.Fatalf("expected 149 conflicts, got %d", result.Conflicts)
	}
	if len(result.Attempts) != 150 {
		t.Fatalf("expected 150 attempt rows, got %d", len(result.Attempts))
	}
}
