// Package service defines transport-independent application ports. Type
// parameters retain the existing domain types without importing package main
// or moving file-identity/security data across package boundaries prematurely.
package service

import "context"

// Executor is the queue-to-downloader boundary. Sink is a named event function
// in the host domain; it must not be replaced with untyped JSON or credentials.
type Executor[Job, Result, Sink any] interface {
	Execute(context.Context, Job, Sink) (Result, error)
}

type Lookup[Job any] interface {
	Get(context.Context, string) (Job, error)
}

type Submitter[Job, Request any] interface {
	Create(context.Context, Request) ([]Job, error)
}

type Canceller[Job any] interface {
	Cancel(context.Context, string) (Job, error)
}

// Events does not grant job creation or credential access. Legacy GET/SSE
// adapters receive only Lookup and Events, preserving their read-only role.
type Events[Event any] interface {
	EventsAfter(context.Context, string, int64) ([]Event, error)
	JobIDForEvent(context.Context, int64) (string, error)
}

type Starter interface{ Start(context.Context) error }
type Waiter interface{ Wait(context.Context) error }
type Worker interface {
	Starter
	Waiter
}
