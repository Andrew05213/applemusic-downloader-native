package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// Only a public developer token is retained, in memory. No user token or
// account response is cached. Waiting callers may cancel without blocking.
type catalogTokenCache struct {
	mu       sync.Mutex
	provider func(context.Context) (string, error)
	now      func() time.Time
	token    string
	expires  time.Time
	flight   chan struct{}
}

func (c *catalogTokenCache) get(ctx context.Context) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		c.mu.Lock()
		if c.token != "" && c.now().Before(c.expires) {
			token := c.token
			c.mu.Unlock()
			return token, nil
		}
		if done := c.flight; done != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-done:
				continue
			}
		}
		c.flight = make(chan struct{})
		done := c.flight
		c.mu.Unlock()
		token, err := c.provider(ctx)
		if err == nil && strings.TrimSpace(token) == "" {
			err = errors.New("catalog_identity_unavailable")
		}
		c.mu.Lock()
		if err == nil && token != "" && ctx.Err() == nil {
			now := c.now()
			expiry := now.Add(5 * time.Minute)
			parts := strings.Split(token, ".")
			if len(parts) == 3 {
				if payload, e := base64.RawURLEncoding.DecodeString(parts[1]); e == nil {
					var claims struct {
						Exp int64 `json:"exp"`
					}
					if json.Unmarshal(payload, &claims) == nil && claims.Exp > 0 {
						if jwtExpiry := time.Unix(claims.Exp, 0).Add(-30 * time.Second); jwtExpiry.Before(expiry) {
							expiry = jwtExpiry
						}
					}
				}
			}
			if expiry.After(now) {
				c.token, c.expires = token, expiry
			}
		}
		c.flight = nil
		close(done)
		c.mu.Unlock()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return token, err
	}
}

func (c *catalogTokenCache) invalidate(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == token {
		c.token = ""
		c.expires = time.Time{}
	}
}
