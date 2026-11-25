package nativeauth

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

const fixtureMediaWindow = 35 * time.Second

type reservationReply struct {
	epoch   uint64
	release func()
	err     error
}

func reserveInBackground(m *Manager, ctx, parent context.Context, replies chan<- reservationReply) {
	epoch, release, err := m.ReserveMediaWindow(ctx, parent, fixtureMediaWindow)
	replies <- reservationReply{epoch: epoch, release: release, err: err}
}

func awaitReservation(t *testing.T, ctx context.Context, replies <-chan reservationReply) reservationReply {
	t.Helper()
	select {
	case reply := <-replies:
		return reply
	case <-ctx.Done():
		t.Fatal("media reservation did not return")
		return reservationReply{}
	}
}

func TestMediaWindowDrainsThreeBatchesBeforeTwoSharedRenewals(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := &fakeRunner{has: true}
	var calls atomic.Int32
	entered := make(chan int32, 3)
	proceed := make(chan struct{})
	m := New(r, func(ctx context.Context, _ Tokens) (string, error) {
		if n := calls.Add(1); n > 1 {
			select {
			case entered <- n:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			select {
			case <-proceed:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "us", nil
	})
	t.Cleanup(m.Close)
	var clock atomic.Int64
	var observeClock atomic.Bool
	readClock := make(chan struct{}, 32)
	base := time.Now()
	m.now = func() time.Time {
		if observeClock.Load() {
			readClock <- struct{}{}
		}
		return base.Add(time.Duration(clock.Load()))
	}
	if err := m.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	for round := int32(1); round <= 2; round++ {
		// Admit work just before the guard, then move into the drain window
		// without expiring grants for the already in-flight operations.
		clock.Add(int64(verificationLifetime - fixtureMediaWindow - time.Second))
		var active [3]func()
		var oldEpoch uint64
		for i := range active {
			epoch, release, err := m.ReserveMediaWindow(ctx, ctx, fixtureMediaWindow)
			if err != nil {
				t.Fatal(err)
			}
			oldEpoch, active[i] = epoch, release
			defer release()
		}
		clock.Add(int64(2 * time.Second))
		replies := make(chan reservationReply, len(active))
		observeClock.Store(true)
		for range active {
			go reserveInBackground(m, ctx, ctx, replies)
		}
		for range active {
			select {
			case <-readClock:
			case <-ctx.Done():
				t.Fatal("waiting requests did not reach admission")
			}
		}
		observeClock.Store(false)
		m.mu.Lock()
		waiting := m.mediaReservations == len(active) && m.done == nil
		m.mu.Unlock()
		if !waiting || calls.Load() != round {
			t.Fatal("renewal started or new work admitted before the old batch drained")
		}
		if state := m.Refresh(ctx); state.State != "authenticated" {
			t.Fatal("status polling invalidated a healthy in-flight grant early")
		}
		for i := range active {
			if epoch, verified := m.VerifiedMediaEpoch(); !verified || epoch != oldEpoch {
				t.Fatal("an already-dispatched batch lost its original window")
			}
			active[i]()
		}
		select {
		case call := <-entered:
			if call != round+1 {
				t.Fatal("duplicate verification")
			}
		case <-ctx.Done():
			t.Fatal("draining did not start verification")
		}
		select {
		case reply := <-replies:
			t.Fatalf("admitted work during verification: %v", reply.err)
		default:
		}
		select {
		case proceed <- struct{}{}:
		case <-ctx.Done():
			t.Fatal("verification did not finish")
		}
		var newEpoch uint64
		for i := range active {
			reply := awaitReservation(t, ctx, replies)
			if reply.err != nil || reply.epoch == oldEpoch || (i > 0 && reply.epoch != newEpoch) {
				t.Fatalf("requests did not resume on the same renewed epoch: %v", reply.err)
			}
			newEpoch = reply.epoch
			reply.release()
		}
		if calls.Load() != round+1 {
			t.Fatal("parallel batches renewed more than once")
		}
	}
	if r.logins.Load() != 0 {
		t.Fatal("renewal tried to log in again")
	}
}

func TestMediaWindowDistinguishesTimeoutFromRejection(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail error
		want error
	}{
		{"timeout", context.DeadlineExceeded, ErrUnverified},
		{"denied", ErrAuthRequired, ErrAuthRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var calls atomic.Int32
			m := New(&fakeRunner{has: true}, func(context.Context, Tokens) (string, error) {
				if calls.Add(1) == 1 {
					return "us", nil
				}
				return "", tc.fail
			})
			t.Cleanup(m.Close)
			base := time.Now()
			var clock atomic.Int64
			m.now = func() time.Time { return base.Add(time.Duration(clock.Load())) }
			if err := m.Restore(ctx); err != nil {
				t.Fatal(err)
			}
			clock.Store(int64(verificationLifetime - fixtureMediaWindow))
			for i := 0; i < 2; i++ {
				if _, release, err := m.ReserveMediaWindow(ctx, ctx, fixtureMediaWindow); !errors.Is(err, tc.want) || release != nil {
					t.Fatalf("expected %v, got %v", tc.want, err)
				}
			}
			if calls.Load() != 2 {
				t.Fatal("new requests bypassed rejection or verification backoff")
			}
		})
	}
}

func TestMediaWindowCancelledRequesterDoesNotCancelRenewal(t *testing.T) {
	parent, closeGuest := context.WithTimeout(context.Background(), 3*time.Second)
	defer closeGuest()
	request, cancel := context.WithCancel(parent)
	defer cancel()
	var calls atomic.Int32
	entered, keepGoing := make(chan struct{}), make(chan struct{})
	m := New(&fakeRunner{has: true}, func(ctx context.Context, _ Tokens) (string, error) {
		if calls.Add(1) == 2 {
			close(entered)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-keepGoing:
			}
		}
		return "us", nil
	})
	t.Cleanup(m.Close)
	base := time.Now()
	var clock atomic.Int64
	m.now = func() time.Time { return base.Add(time.Duration(clock.Load())) }
	if err := m.Restore(parent); err != nil {
		t.Fatal(err)
	}
	clock.Store(int64(verificationLifetime - fixtureMediaWindow))
	replies := make(chan reservationReply, 1)
	go reserveInBackground(m, request, parent, replies)
	select {
	case <-entered:
	case <-parent.Done():
		t.Fatal("renewal did not start")
	}
	cancel()
	if reply := awaitReservation(t, parent, replies); !errors.Is(reply.err, ErrUnverified) || reply.release != nil {
		t.Fatalf("cancelled caller got %v", reply.err)
	}
	go reserveInBackground(m, parent, parent, replies)
	close(keepGoing)
	if reply := awaitReservation(t, parent, replies); reply.err != nil {
		t.Fatalf("renewal stopped with the first requester: %v", reply.err)
	} else {
		reply.release()
	}
	if calls.Load() != 2 {
		t.Fatal("waiting requests restarted renewal")
	}
}

func TestMediaReservationNeverExtendsOrOverridesRevocation(t *testing.T) {
	for _, mode := range []string{"expiry", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			m := New(&fakeRunner{has: true}, verified)
			t.Cleanup(m.Close)
			base := time.Now()
			var clock atomic.Int64
			m.now = func() time.Time { return base.Add(time.Duration(clock.Load())) }
			if err := m.Restore(ctx); err != nil {
				t.Fatal(err)
			}
			epoch, release, err := m.ReserveMediaWindow(ctx, ctx, fixtureMediaWindow)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if mode == "expiry" {
				clock.Store(int64(verificationLifetime))
			} else {
				m.Cancel()
			}
			if current, valid := m.VerifiedMediaEpoch(); valid && current == epoch {
				t.Fatal("holding a reservation kept a revoked grant valid")
			}
		})
	}
}
