package nativeauth

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"time"
)

// Only fixed stage/error vocabulary crosses this boundary. Never pass error
// text, URLs, headers, accounts, phone IDs, delivery codes or child output.
var DiagnosticStages = []string{"restore_session", "login_start", "stop_lite", "device_login", "device_code", "gsa_provider", "gsa_prepare", "adi_helper_start", "adi_initialize", "adi_status", "adi_lookup", "adi_provision_start", "adi_provision_sdk_start", "adi_provision_finish", "adi_provision_sdk_finish", "adi_state_save", "adi_otp", "gsa_authenticate", "phone_list", "sms_wait_phone", "sms_send", "sms_wait_code", "sms_verify", "gsa_resume", "store_credentials", "lite_bind", "music_service_start", "music_tokens", "music_storefront", "login_complete", "login_cancel", "desktop_login_submit", "desktop_sms_send", "desktop_sms_code", "desktop_service", "desktop_recheck", "desktop_code", "diagnostics"}
var DiagnosticExtraCodes = []string{"native_auth_required", "native_session_unverified", "native_login_failed", "native_auth_busy", "native_auth_input_invalid", "native_auth_cancelled", "native_adi_provider_unavailable", "native_adi_provisioning_failed", "native_adi_state_invalid", "native_adi_cancelled", "native_adi_not_provisioned", "native_network_dns_failed", "native_network_timeout", "native_tls_certificate_invalid", "native_network_connection_failed", "native_adi_http_failed", "native_adi_response_invalid", "native_agent_unavailable", "native_agent_protocol_invalid", "native_service_unavailable", "native_service_timeout", "native_service_closed", "native_api_unavailable", "native_api_failed", "native_api_response_invalid", "native_login_rejected", "native_code_rejected", "native_sms_rejected", "native_operation_failed", "native_runtime_unavailable", "native_diagnostics_read_failed", "native_diagnostics_write_failed"}

type StageEvent struct {
	TraceID        string `json:"traceId"`
	Seq            uint64 `json:"seq"`
	Attempt        uint64 `json:"attempt"`
	At             int64  `json:"at"`
	Stage          string `json:"stage"`
	Outcome        string `json:"outcome"`
	Code           string `json:"code,omitempty"`
	HTTPStatus     int    `json:"httpStatus,omitempty"`
	AppleErrorCode string `json:"appleErrorCode,omitempty"`
}
type Diagnostics struct {
	Events      []StageEvent `json:"events"`
	LastFailure *StageEvent  `json:"lastFailure,omitempty"`
}
type reporterKey struct{}

func TransportErrorCode(err error) string {
	var dns *net.DNSError
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var host x509.HostnameError
	var network net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "native_network_timeout"
	case errors.As(err, &dns):
		return "native_network_dns_failed"
	case errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &host):
		return "native_tls_certificate_invalid"
	case errors.As(err, &network) && network.Timeout():
		return "native_network_timeout"
	}
	return "native_network_connection_failed"
}

func KnownStage(stage string) bool {
	for _, s := range DiagnosticStages {
		if stage == s {
			return true
		}
	}
	return false
}
func KnownCode(code string) bool {
	if musicVerificationCode(code) {
		return true
	}
	for _, s := range DiagnosticExtraCodes {
		if code == s {
			return true
		}
	}
	for _, s := range ADIResponseCodes {
		if code == s {
			return true
		}
	}
	return knownLegacyAuthCode(code)
}
func validEventFields(e StageEvent) bool {
	if !KnownStage(e.Stage) || (e.Code != "" && !KnownCode(e.Code)) || (e.HTTPStatus != 0 && (e.HTTPStatus < 100 || e.HTTPStatus > 599)) || (e.AppleErrorCode != "" && !KnownAppleErrorCode(e.AppleErrorCode)) {
		return false
	}
	switch e.Outcome {
	case "begin", "ok", "waiting", "cancelled":
		return true
	case "failed":
		return e.Code != ""
	}
	return false
}
func ValidDiagnostics(d *Diagnostics) bool {
	if d == nil {
		return true
	}
	if len(d.Events) > 128 {
		return false
	}
	valid := func(e StageEvent) bool {
		if !validEventFields(e) || len(e.TraceID) != 32 || e.Seq == 0 || e.Seq > 9007199254740991 || e.Attempt == 0 || e.Attempt > 9007199254740991 || e.At <= 0 || e.At > 4102444800000 {
			return false
		}
		for _, c := range e.TraceID {
			if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
				return false
			}
		}
		return true
	}
	for _, e := range d.Events {
		if !valid(e) {
			return false
		}
	}
	return d.LastFailure == nil || valid(*d.LastFailure) && d.LastFailure.Outcome == "failed"
}
func cloneDiagnostics(d *Diagnostics) *Diagnostics {
	if d == nil {
		return nil
	}
	c := *d
	c.Events = append([]StageEvent{}, d.Events...)
	if d.LastFailure != nil {
		e := *d.LastFailure
		c.LastFailure = &e
	}
	return &c
}
func WithReporter(ctx context.Context, report func(StageEvent)) context.Context {
	return context.WithValue(ctx, reporterKey{}, report)
}
func Report(ctx context.Context, stage, outcome, code string, status int) {
	ReportEvent(ctx, StageEvent{Stage: stage, Outcome: outcome, Code: code, HTTPStatus: status})
}

func ReportEvent(ctx context.Context, e StageEvent) {
	if ctx == nil {
		return
	}
	if !validEventFields(e) {
		return
	}
	if f, ok := ctx.Value(reporterKey{}).(func(StageEvent)); ok && f != nil {
		f(e)
	}
}
func newTraceID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(id[:])
}
func (m *Manager) record(generation uint64, e StageEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation == m.generation {
		m.recordLocked(generation, e)
	}
}
func (m *Manager) recordLocked(generation uint64, e StageEvent) {
	if !validEventFields(e) || m.traceID == "" {
		return
	}
	m.eventSeq++
	e.TraceID = m.traceID
	e.Seq = m.eventSeq
	e.Attempt = generation
	e.At = time.Now().UnixMilli()
	m.currentStage = e.Stage
	m.history.Events = append(m.history.Events, e)
	if len(m.history.Events) > 128 {
		m.history.Events = append([]StageEvent{}, m.history.Events[len(m.history.Events)-128:]...)
	}
	if e.Outcome == "failed" {
		copy := e
		m.history.LastFailure = &copy
	}
}
func (m *Manager) resultFailure(generation uint64, code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation != m.generation {
		return
	}
	// Preserve the leaf's error (e.g. DNS/provision/schema), not a caller's
	// subsequent generic login error. A new attempt may replace a prior failure.
	if m.history.LastFailure != nil && m.history.LastFailure.Attempt == generation && m.history.LastFailure.Seq == m.eventSeq {
		return
	}
	stage := m.currentStage
	if !KnownStage(stage) {
		stage = "login_start"
	}
	m.recordLocked(generation, StageEvent{Stage: stage, Outcome: "failed", Code: code})
}
