package nativeauth

import (
	"context"
	"sync"
	"time"
)

// ReserveMediaWindow reserves time for a bounded, already authorized media
// operation. All callers use the same maximum work window and must bound their
// requests by it, not by the remaining authentication lifetime. On the normal
// renewal boundary, current reservations drain before this method starts a
// shared verification. Explicit cancellation, login and shutdown may still
// revoke the returned epoch immediately; the reservation never extends it.
// The caller releases it after the upstream response is consumed or abandoned.
func (m *Manager) ReserveMediaWindow(ctx, renewalParent context.Context, window time.Duration) (uint64, func(), error) {
	if window <= 0 || window >= verificationLifetime {
		return 0, nil, ErrUnverified
	}
	for {
		m.mu.Lock()
		if ctx.Err() != nil || renewalParent.Err() != nil {
			m.mu.Unlock()
			return 0, nil, ErrUnverified
		}
		now := m.now()
		m.expireVerificationLocked(now)
		state := m.state.State
		if m.closed || state == "needs_login" || state == "needs_2fa" || state == "needs_interaction" || state == "authenticating" {
			m.mu.Unlock()
			return 0, nil, ErrAuthRequired
		}
		if m.state.Code == ErrCancelled.Error() {
			m.mu.Unlock()
			return 0, nil, ErrUnverified
		}
		if state == "authenticated" && now.Add(window).Before(m.verifiedUntil) {
			if m.mediaReservations == 0 {
				m.mediaDrained = make(chan struct{})
			}
			m.mediaReservations++
			epoch := m.generation
			m.mu.Unlock()
			return epoch, sync.OnceFunc(m.releaseMediaWindow), nil
		}

		// The check and worker registration are both under the manager lock.
		// Playback, status polling and all waiting batches see the same worker.
		wait := m.done
		if wait == nil {
			wait = m.mediaDrained
		}
		if wait == nil {
			if now.Before(m.retryAfter) {
				m.mu.Unlock()
				return 0, nil, ErrUnverified
			}
			operation, generation, err := m.operationLocked(renewalParent, "checking", 30*time.Second)
			if err != nil {
				m.mu.Unlock()
				return 0, nil, ErrUnverified
			}
			wait = m.done
			m.mu.Unlock()
			// The guest owns renewal, not whichever HTTP request reached the
			// boundary first. Its cancellation must not fail other downloads.
			go func() {
				defer m.finish(generation)
				m.restore(operation, generation)
			}()
		} else {
			m.mu.Unlock()
		}
		select {
		case <-wait:
		case <-ctx.Done():
			return 0, nil, ErrUnverified
		case <-renewalParent.Done():
			return 0, nil, ErrUnverified
		}
	}
}

func (m *Manager) releaseMediaWindow() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mediaReservations--
	if m.mediaReservations == 0 {
		close(m.mediaDrained)
		m.mediaDrained = nil
	}
}
