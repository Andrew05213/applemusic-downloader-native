package main

import (
	"github.com/Andrew05213/applemusic_downloader/internal/legacyweb"
	"github.com/Andrew05213/applemusic_downloader/internal/service"
)

type jobLookup = service.Lookup[Job]
type jobSubmitter = service.Submitter[Job, CreateJobsRequest]
type jobCanceller = service.Canceller[Job]
type jobEventReader interface {
	jobLookup
	service.Events[JobEvent]
}
type wrapperObserver = legacyweb.Observer[WrapperSnapshot]
type wrapperTwoFactor = legacyweb.TwoFactor
type wrapperHealthReader interface{ Status() WrapperStatus }

// These assertions keep adapters honest while implementation ownership and
// public JSON remain in the existing queue/Wrapper domain.
var (
	_ JobExecutor         = (*Engine)(nil)
	_ JobExecutor         = processJobExecutor{}
	_ service.Worker      = (*JobManager)(nil)
	_ jobLookup           = (*JobManager)(nil)
	_ jobSubmitter        = (*JobManager)(nil)
	_ jobCanceller        = (*JobManager)(nil)
	_ jobEventReader      = (*JobManager)(nil)
	_ wrapperObserver     = (*WrapperManager)(nil)
	_ wrapperTwoFactor    = (*WrapperManager)(nil)
	_ wrapperHealthReader = (*WrapperManager)(nil)
)
