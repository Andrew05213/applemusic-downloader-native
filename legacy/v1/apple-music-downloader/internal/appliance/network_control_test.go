package appliance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/buildinfo"
	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

func networkPreparePayload(t *testing.T, generation, digest string) []byte {
	t.Helper()
	payload, err := json.Marshal(NetworkPrepareRequest{
		SchemaVersion: 1,
		GenerationID:  generation,
		HelperPath:    "/mnt/c/Program Files/Apple Music Downloader/amdl-windows-probe.exe",
		HelperSHA256:  digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func networkCleanupPayload(t *testing.T, generation, version string) []byte {
	t.Helper()
	payload, err := json.Marshal(NetworkCleanupRequest{
		SchemaVersion: 1,
		GenerationID:  generation,
		Version:       version,
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestDecodeNetworkCleanupRequestIsStrictAndReleaseBound(t *testing.T) {
	generation := strings.Repeat("a", 64)
	version := baseStatus().Version
	valid := networkCleanupPayload(t, generation, version)
	request, err := decodeNetworkCleanupRequest(valid)
	if err != nil || request.GenerationID != generation || request.Version != version {
		t.Fatalf("decode cleanup = %#v, %v", request, err)
	}
	for _, invalid := range [][]byte{
		[]byte(`{"schemaVersion":1}`),
		[]byte(`{"schemaVersion":1,"generationId":"` + generation + `","version":"` + version + `","extra":true}`),
		[]byte(`{"schemaVersion":2,"generationId":"` + generation + `","version":"` + version + `"}`),
		[]byte(`{"schemaVersion":1,"generationId":"` + strings.Repeat("A", 64) + `","version":"` + version + `"}`),
		[]byte(`{"schemaVersion":1,"generationId":"` + generation + `","version":"1.2.14"}`),
	} {
		if _, err := decodeNetworkCleanupRequest(invalid); err == nil {
			t.Fatalf("invalid cleanup request accepted: %s", invalid)
		}
	}
}

func networkPreflightResult(ok bool, stage string) NativeNetworkPreflightResult {
	return NativeNetworkPreflightResult{
		SchemaVersion: 2,
		OK:            ok,
		Stage:         stage,
		CASource:      "bundled_v1",
	}
}

func newNetworkTestSupervisor(paths Paths, options Options) *Supervisor {
	if options.VerifyAndroidCABundle == nil {
		options.VerifyAndroidCABundle = func(string, string) error { return nil }
	}
	if options.VerifyAndroidNetworkPreflight == nil {
		options.VerifyAndroidNetworkPreflight = func(string, string) error { return nil }
	}
	return NewSupervisor(paths, options)
}

func TestSupervisorPreflightRejectsStandaloneProbeBeforeNativeRunner(t *testing.T) {
	generation := strings.Repeat("a", 64)
	digest := strings.Repeat("b", 64)
	probeCalls := 0
	verifierCalls := 0
	supervisor := newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
		NetworkRequired:    true,
		LogOutput:          io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) { return path, nil },
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) {
			return nil, errors.New("not reached")
		},
		VerifyAndroidNetworkPreflight: func(path, digest string) error {
			verifierCalls++
			if path == "" || digest != buildinfo.AndroidNetworkPreflightBuiltSHA256 {
				t.Fatalf("standalone probe identity = path %q digest %q", path, digest)
			}
			return errors.New("probe digest changed")
		},
		RunNetworkPreflight: func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			probeCalls++
			return networkPreflightResult(true, ""), nil
		},
	})
	if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK {
		t.Fatalf("prepare: %#v", response)
	}
	response := supervisor.preflightNetwork()
	if response.OK || response.Error == nil || response.Error.Code != "network_preflight_failed" {
		t.Fatalf("preflight response = %#v", response)
	}
	if verifierCalls != 1 || probeCalls != 0 || response.Status.Network.Stage != "native_probe_invalid" {
		t.Fatalf("probe identity failure verifier=%d probes=%d status=%#v", verifierCalls, probeCalls, response.Status.Network)
	}
	_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
}

func TestSupervisorStartFailsClosedWhenReadyStandaloneProbeIdentityDrifts(t *testing.T) {
	generation := strings.Repeat("e", 64)
	digest := strings.Repeat("f", 64)
	probeInvalid := false
	supervisor := newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
		NetworkRequired:    true,
		LogOutput:          io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) { return path, nil },
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) {
			return nil, errors.New("not reached")
		},
		VerifyAndroidNetworkPreflight: func(string, string) error {
			if probeInvalid {
				return errors.New("probe identity changed")
			}
			return nil
		},
		RunNetworkPreflight: func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			return networkPreflightResult(true, ""), nil
		},
	})
	if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK {
		t.Fatalf("prepare: %#v", response)
	}
	if response := supervisor.preflightNetwork(); !response.OK || response.Status.Network.State != "ready" {
		t.Fatalf("preflight: %#v", response)
	}

	probeInvalid = true
	if err := supervisor.start(); !errors.Is(err, errNetworkPreflightRequired) {
		t.Fatalf("start after probe drift = %v, want %v", err, errNetworkPreflightRequired)
	}
	status := supervisor.Status()
	if status.Network.State != "failed" || status.Network.Stage != "native_probe_invalid" || status.Network.ProxyPropagated {
		t.Fatalf("status after probe drift = %#v", status.Network)
	}
	_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
}

func TestSupervisorPreflightClassifiesRunnerFailureWithoutLeakingIt(t *testing.T) {
	generation := strings.Repeat("c", 64)
	digest := strings.Repeat("d", 64)
	supervisor := newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
		NetworkRequired:    true,
		LogOutput:          io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) { return path, nil },
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) {
			return nil, errors.New("not reached")
		},
		RunNetworkPreflight: func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			return NativeNetworkPreflightResult{}, errors.New("sensitive native loader detail sentinel")
		},
	})
	if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK {
		t.Fatalf("prepare: %#v", response)
	}
	response := supervisor.preflightNetwork()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error == nil || response.Error.Code != "network_preflight_failed" ||
		response.Status.Network.Stage != "native_startup" {
		t.Fatalf("runner failure response = %#v", response)
	}
	if strings.Contains(string(encoded), "sensitive native loader detail sentinel") {
		t.Fatalf("runner failure leaked through response: %s", encoded)
	}
	_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
}

func TestSupervisorDispatchNetworkControlDoesNotReenterLifecycleLock(t *testing.T) {
	generation := strings.Repeat("a", 64)
	digest := strings.Repeat("b", 64)
	supervisor := newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
		NetworkRequired:    true,
		LogOutput:          io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) { return path, nil },
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) { return nil, errors.New("not used") },
		RunNetworkPreflight: func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			return networkPreflightResult(true, ""), nil
		},
	})

	dispatch := func(operation string, payload []byte) Response {
		result := make(chan Response, 1)
		go func() { result <- supervisor.dispatch(operation, payload) }()
		select {
		case response := <-result:
			return response
		case <-time.After(time.Second):
			t.Fatalf("dispatch %q did not return; network control reentered lifecycle lock", operation)
			return Response{}
		}
	}

	if response := dispatch(OperationNetworkPrepare, networkPreparePayload(t, generation, digest)); !response.OK {
		t.Fatalf("dispatch network prepare: %#v", response)
	}
	if response := dispatch(OperationNetworkPreflight, nil); !response.OK || response.Status.Network.State != "ready" {
		t.Fatalf("dispatch network preflight: %#v", response)
	}
	if err := supervisor.closeNetworkBroker(egressBrokerCloseTimeout); err != nil {
		t.Fatalf("close network broker: %v", err)
	}
}

func TestSupervisorPreflightRejectsRunningBackendBeforeWrapperLaunch(t *testing.T) {
	generation := strings.Repeat("7", 64)
	digest := strings.Repeat("8", 64)
	runnerCalls := 0
	supervisor := newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
		NetworkRequired:    true,
		LogOutput:          io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) { return path, nil },
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) { return nil, errors.New("not used") },
		RunNetworkPreflight: func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			runnerCalls++
			return networkPreflightResult(true, ""), nil
		},
	})
	if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK {
		t.Fatalf("prepare network: %#v", response)
	}
	supervisor.mu.Lock()
	supervisor.state = "running"
	supervisor.process = newFakeManagedProcess(8099)
	supervisor.mu.Unlock()

	response := supervisor.preflightNetwork()
	if response.OK || response.Error == nil || response.Error.Code != "service_running" {
		t.Fatalf("running preflight response = %#v", response)
	}
	if runnerCalls != 0 {
		t.Fatalf("running preflight launched %d Wrapper probes", runnerCalls)
	}
	if response.Status.Network.State != "prepared" || response.Status.Network.GenerationID != generation {
		t.Fatalf("running preflight mutated broker state: %#v", response.Status.Network)
	}
	supervisor.process.(*fakeManagedProcess).finish(nil)
	_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
}

func TestSupervisorNetworkCleanupBindsOnlyTheStoppedCurrentBroker(t *testing.T) {
	generation := strings.Repeat("c", 64)
	digest := strings.Repeat("d", 64)
	supervisor := newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
		NetworkRequired:    true,
		LogOutput:          io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) { return path, nil },
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) { return nil, errors.New("not used") },
	})
	if response := supervisor.dispatch(OperationNetworkPrepare, networkPreparePayload(t, generation, digest)); !response.OK {
		t.Fatalf("prepare network: %#v", response)
	}
	version := supervisor.Status().Version
	if response := supervisor.dispatch(OperationNetworkCleanup, []byte(`{"schemaVersion":1}`)); response.OK || response.Error == nil || response.Error.Code != "invalid_network_cleanup" {
		t.Fatalf("invalid cleanup request = %#v", response)
	}
	if response := supervisor.dispatch(OperationNetworkCleanup, networkCleanupPayload(t, strings.Repeat("e", 64), version)); response.OK || response.Error == nil || response.Error.Code != "network_generation_changed" {
		t.Fatalf("mismatched cleanup generation = %#v", response)
	}
	if status := supervisor.Status(); status.State != "stopped" || status.Network.State != "prepared" || status.Network.GenerationID != generation {
		t.Fatalf("rejected cleanup changed broker state: %#v", status)
	}

	supervisor.mu.Lock()
	supervisor.state = "running"
	supervisor.mu.Unlock()
	if response := supervisor.dispatch(OperationNetworkCleanup, networkCleanupPayload(t, generation, version)); response.OK || response.Error == nil || response.Error.Code != "service_running" {
		t.Fatalf("running cleanup request = %#v", response)
	}
	supervisor.mu.Lock()
	supervisor.state = "stopped"
	supervisor.mu.Unlock()

	response := supervisor.dispatch(OperationNetworkCleanup, networkCleanupPayload(t, generation, version))
	if !response.OK || response.Status.State != "stopped" || response.Status.Network.State != "unprepared" || response.Status.Network.GenerationID != "" {
		t.Fatalf("cleanup response = %#v", response)
	}
	if supervisor.networkBroker != nil {
		t.Fatal("cleanup retained a broker after reporting unprepared")
	}
	if response := supervisor.dispatch(OperationNetworkCleanup, networkCleanupPayload(t, generation, version)); response.OK || response.Error == nil || response.Error.Code != "network_generation_changed" {
		t.Fatalf("unknown cleanup generation = %#v", response)
	}
}

func TestSupervisorSameVersionCABundleRepairAdmissionIsNarrow(t *testing.T) {
	generation := strings.Repeat("a", 64)
	digest := strings.Repeat("b", 64)
	newFixture := func(t *testing.T) (*Supervisor, UpdateRequest) {
		t.Helper()
		supervisor := newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
			NetworkRequired:    true,
			LogOutput:          io.Discard,
			VerifyEgressHelper: func(path, _ string) (string, error) { return path, nil },
			LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) { return nil, errors.New("not used") },
		})
		if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK {
			t.Fatalf("prepare network: %#v", response)
		}
		supervisor.mu.Lock()
		supervisor.state = "stopped"
		supervisor.networkStatus.State = "failed"
		supervisor.networkStatus.Stage = "ca_bundle_missing"
		supervisor.networkStatus.ProxyPropagated = false
		supervisor.updateSource = releaseMetadata{
			SchemaVersion: 1,
			Version:       buildinfo.Version,
			RuntimeSHA256: strings.Repeat("c", 64),
			TransactionID: strings.Repeat("d", 64),
		}
		supervisor.mu.Unlock()
		return supervisor, UpdateRequest{
			SchemaVersion: 1,
			Mode:          "repair",
			Version:       buildinfo.Version,
			BundlePath:    "/mnt/c/runtime.tar.gz",
			SHA256:        strings.Repeat("e", 64),
			TransactionID: strings.Repeat("f", 64),
		}
	}

	valid, request := newFixture(t)
	if !valid.canRecoverCABundleWithSameVersionRepair(request) {
		t.Fatal("stopped same-version CA-bundle repair was rejected")
	}
	_ = valid.closeNetworkBroker(egressBrokerCloseTimeout)

	for _, mutate := range []struct {
		name    string
		request func(*UpdateRequest)
		state   func(*Supervisor)
	}{
		{name: "ordinary update", request: func(value *UpdateRequest) { value.Mode = "update" }},
		{name: "different version", request: func(value *UpdateRequest) { value.Version = "1.2.16" }},
		{name: "running backend", state: func(supervisor *Supervisor) { supervisor.state = "running" }},
		{name: "stopped state with process", state: func(supervisor *Supervisor) { supervisor.process = newFakeManagedProcess(8100) }},
		{name: "wrong current release", state: func(supervisor *Supervisor) { supervisor.updateSource.Version = "1.2.14" }},
		{name: "missing current release identity", state: func(supervisor *Supervisor) { supervisor.updateSource = releaseMetadata{} }},
		{name: "non CA stage", state: func(supervisor *Supervisor) { supervisor.networkStatus.Stage = "tls" }},
		{name: "cleanup failure", state: func(supervisor *Supervisor) { supervisor.networkCleanupErr = true }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			supervisor, candidate := newFixture(t)
			if mutate.request != nil {
				mutate.request(&candidate)
			}
			if mutate.state != nil {
				supervisor.mu.Lock()
				mutate.state(supervisor)
				supervisor.mu.Unlock()
			}
			if supervisor.canRecoverCABundleWithSameVersionRepair(candidate) {
				t.Fatal("unsafe CA-bundle repair admission was accepted")
			}
			_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
		})
	}

	t.Run("recovery pending", func(t *testing.T) {
		supervisor, candidate := newFixture(t)
		activation := &ReleaseActivation{
			paths:         supervisor.paths,
			finalPath:     filepath.Join(supervisor.paths.ReleaseRoot, buildinfo.Version),
			runtimeSHA256: strings.Repeat("1", 64),
			transactionID: strings.Repeat("2", 64),
		}
		if err := writeReleaseTransaction(activation, releaseTransactionPrepared); err != nil {
			t.Fatalf("write pending recovery marker: %v", err)
		}
		if supervisor.canRecoverCABundleWithSameVersionRepair(candidate) {
			t.Fatal("CA-bundle repair was accepted with a pending runtime recovery")
		}
		_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
	})
}

func TestSupervisorSameVersionCABundleRepairPreflightsCandidateBeforeHealthLaunch(t *testing.T) {
	generation := strings.Repeat("a", 64)
	digest := strings.Repeat("b", 64)
	paths := testPaths(t.TempDir())
	process := newFakeManagedProcess(8123)
	activated := false
	sequence := make([]string, 0, 4)
	supervisor := newNetworkTestSupervisor(paths, Options{
		BackendHost:         "127.0.0.2",
		NetworkRequired:     true,
		LogOutput:           io.Discard,
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		PrepareDatabaseUpdate: func(string) error {
			return nil
		},
		VerifyEgressHelper: func(path, _ string) (string, error) { return path, nil },
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) { return nil, errors.New("not used") },
		VerifyAndroidCABundle: func(path, _ string) error {
			if path != paths.AndroidCABundle || !activated {
				return errAndroidCABundleMissing
			}
			return nil
		},
		ActivateRelease: func(_ Paths, request UpdateRequest) (*ReleaseActivation, error) {
			if request.Mode != "repair" || request.Version != buildinfo.Version {
				t.Fatalf("unexpected recovery request: %#v", request)
			}
			activated = true
			sequence = append(sequence, "activate")
			return &ReleaseActivation{}, nil
		},
		ValidateRelease: func(*ReleaseActivation) error {
			sequence = append(sequence, "validate")
			return nil
		},
		RunNetworkPreflight: func(_ context.Context, spec NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			if !activated || spec.GenerationID != generation {
				t.Fatalf("candidate preflight ran before activation: activated=%v spec=%#v", activated, spec)
			}
			sequence = append(sequence, "preflight")
			return networkPreflightResult(true, ""), nil
		},
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			sequence = append(sequence, "launch")
			return process, nil
		},
		WaitReady: func(context.Context, string, int, string, string, uint64) error {
			sequence = append(sequence, "ready")
			return nil
		},
	})
	supervisor.mu.Lock()
	supervisor.configured = true
	supervisor.config = Config{SchemaVersion: 1, Port: 18080, DownloadPath: "/mnt/c/Music"}
	supervisor.mu.Unlock()
	if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK {
		t.Fatalf("prepare network: %#v", response)
	}
	supervisor.mu.Lock()
	supervisor.networkStatus.State = "failed"
	supervisor.networkStatus.Stage = "ca_bundle_missing"
	supervisor.networkStatus.ProxyPropagated = false
	supervisor.mu.Unlock()
	supervisor.beforeUpdateLifecycleLock = func() {
		supervisor.mu.Lock()
		supervisor.updateSource = releaseMetadata{
			SchemaVersion: 1,
			Version:       buildinfo.Version,
			RuntimeSHA256: strings.Repeat("c", 64),
			TransactionID: strings.Repeat("d", 64),
		}
		supervisor.mu.Unlock()
	}
	payload, err := json.Marshal(UpdateRequest{
		SchemaVersion: 1,
		Mode:          "repair",
		Version:       buildinfo.Version,
		BundlePath:    "/mnt/c/runtime.tar.gz",
		SHA256:        strings.Repeat("e", 64),
		TransactionID: strings.Repeat("f", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := supervisor.update(payload)
	if !response.OK {
		t.Fatalf("same-version CA repair response = %#v", response)
	}
	if got, want := strings.Join(sequence, ","), "activate,validate,preflight,launch,ready"; got != want {
		t.Fatalf("CA repair sequence = %q, want %q", got, want)
	}
	if status := supervisor.Status(); status.Network.State != "ready" || status.Network.Stage != "complete" || !status.Network.ProxyPropagated {
		t.Fatalf("candidate CA preflight did not become ready: %#v", status.Network)
	}
	process.finish(nil)
	if err := supervisor.closeNetworkBroker(egressBrokerCloseTimeout); err != nil {
		t.Fatalf("close broker: %v", err)
	}
}

func TestSupervisorSameVersionCABundleRepairRollsBackFailedCandidateBeforeBackendLaunch(t *testing.T) {
	generation := strings.Repeat("1", 64)
	digest := strings.Repeat("2", 64)
	paths := testPaths(t.TempDir())
	activated := false
	preflights := 0
	rollbacks := 0
	launches := 0
	supervisor := newNetworkTestSupervisor(paths, Options{
		BackendHost:           "127.0.0.2",
		NetworkRequired:       true,
		LogOutput:             io.Discard,
		PrepareDownload:       func(string, string) error { return nil },
		PrepareWrapperState:   func(string, string) error { return nil },
		PrepareDatabaseUpdate: func(string) error { return nil },
		VerifyEgressHelper:    func(path, _ string) (string, error) { return path, nil },
		LaunchEgressHelper:    func(context.Context, string) (EgressHelperProcess, error) { return nil, errors.New("not used") },
		VerifyAndroidCABundle: func(path, _ string) error {
			if !activated || path != paths.AndroidCABundle {
				t.Fatal("candidate CA verification did not use the activated release path")
			}
			return nil
		},
		ActivateRelease: func(_ Paths, request UpdateRequest) (*ReleaseActivation, error) {
			activated = true
			return &ReleaseActivation{
				paths:      paths,
				finalPath:  filepath.Join(paths.ReleaseRoot, request.Version),
				backupPath: filepath.Join(paths.ReleaseRoot, ".rollback-candidate"),
				operations: releaseOperations{
					unmountWrapperState: func(string) error { return nil },
					removeRuntimeTree: func(string, string) error {
						rollbacks++
						return nil
					},
					rename: func(string, string) error {
						rollbacks++
						return nil
					},
					syncDirectory:   func(string) error { return nil },
					restoreDatabase: func(string) error { return nil },
				},
			}, nil
		},
		ValidateRelease: func(*ReleaseActivation) error { return nil },
		RunNetworkPreflight: func(_ context.Context, spec NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			if !activated || spec.GenerationID != generation {
				t.Fatalf("candidate preflight ran before activation: activated=%v spec=%#v", activated, spec)
			}
			preflights++
			return networkPreflightResult(false, "tls"), nil
		},
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			launches++
			return newFakeManagedProcess(8124), nil
		},
		WaitReady: func(context.Context, string, int, string, string, uint64) error {
			t.Fatal("failed candidate preflight reached the backend health check")
			return nil
		},
	})
	supervisor.mu.Lock()
	supervisor.configured = true
	supervisor.config = Config{SchemaVersion: 1, Port: 18080, DownloadPath: "/mnt/c/Music"}
	supervisor.mu.Unlock()
	if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK {
		t.Fatalf("prepare network: %#v", response)
	}
	supervisor.mu.Lock()
	supervisor.networkStatus.State = "failed"
	supervisor.networkStatus.Stage = "ca_bundle_missing"
	supervisor.networkStatus.ProxyPropagated = false
	supervisor.mu.Unlock()
	supervisor.beforeUpdateLifecycleLock = func() {
		supervisor.mu.Lock()
		supervisor.updateSource = releaseMetadata{
			SchemaVersion: 1,
			Version:       buildinfo.Version,
			RuntimeSHA256: strings.Repeat("3", 64),
			TransactionID: strings.Repeat("4", 64),
		}
		supervisor.mu.Unlock()
	}
	payload, err := json.Marshal(UpdateRequest{
		SchemaVersion: 1,
		Mode:          "repair",
		Version:       buildinfo.Version,
		BundlePath:    "/mnt/c/runtime.tar.gz",
		SHA256:        strings.Repeat("5", 64),
		TransactionID: strings.Repeat("6", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := supervisor.update(payload)
	if response.OK || response.Error == nil || response.Error.Code != "network_preflight_failed" {
		t.Fatalf("failed same-version CA repair response = %#v", response)
	}
	if !activated || preflights != 1 || rollbacks < 2 || launches != 0 {
		t.Fatalf("failed CA repair activated=%v preflights=%d rollbacks=%d launches=%d", activated, preflights, rollbacks, launches)
	}
	if status := supervisor.Status(); status.State != "stopped" || status.Network.State != "failed" || status.Network.Stage != "tls" {
		t.Fatalf("failed CA repair status = %#v", status)
	}
	if err := supervisor.closeNetworkBroker(egressBrokerCloseTimeout); err != nil {
		t.Fatalf("close broker: %v", err)
	}
}

func TestSupervisorRequiresSuccessfulPreflightBeforeCredentialsAndBackend(t *testing.T) {
	generation := strings.Repeat("a", 64)
	digest := strings.Repeat("b", 64)
	process := newFakeManagedProcess(8101)
	launches := 0
	var launchedCredentials secretfd.Credentials
	supervisor := newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
		BackendHost:         "127.0.0.2",
		NetworkRequired:     true,
		LogOutput:           io.Discard,
		PrepareDownload:     func(string, string) error { return nil },
		PrepareWrapperState: func(string, string) error { return nil },
		VerifyEgressHelper:  func(path, _ string) (string, error) { return path, nil },
		LaunchEgressHelper:  func(context.Context, string) (EgressHelperProcess, error) { return nil, errors.New("not used") },
		RunNetworkPreflight: func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			return networkPreflightResult(true, ""), nil
		},
		LaunchBackend: func(_ context.Context, _ BackendSpec, credentials secretfd.Credentials, _ func(string)) (ManagedProcess, error) {
			launches++
			launchedCredentials = credentials
			return process, nil
		},
		WaitReady: func(context.Context, string, int, string, string, uint64) error { return nil },
	})
	config, _ := json.Marshal(Config{SchemaVersion: 1, Port: 18080, DownloadPath: "/mnt/c/Music"})
	if response := supervisor.configure(config); !response.OK {
		t.Fatalf("configure: %#v", response)
	}
	credentialPayload := desktopCredentialPayload(t, secretfd.Credentials{AppleEmail: "person@example.test", ApplePassword: "password-sentinel"})
	if response := supervisor.setCredentials(credentialPayload); response.OK || response.Error == nil || response.Error.Code != "network_preflight_required" {
		t.Fatalf("credentials before preflight = %#v", response)
	}
	if err := supervisor.start(); !errors.Is(err, errNetworkPreflightRequired) || launches != 0 {
		t.Fatalf("start before preflight err=%v launches=%d", err, launches)
	}
	if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK || response.Status.Network.State != "prepared" {
		t.Fatalf("prepare network: %#v", response)
	}
	if response := supervisor.preflightNetwork(); !response.OK || response.Status.Network.State != "ready" || !response.Status.Network.ProxyPropagated {
		t.Fatalf("preflight network: %#v", response)
	}
	if response := supervisor.setCredentials(credentialPayload); !response.OK {
		t.Fatalf("credentials after preflight: %#v", response)
	}
	if err := supervisor.start(); err != nil {
		t.Fatalf("start after preflight: %v", err)
	}
	if launches != 1 || launchedCredentials.NetworkGeneration != generation ||
		!strings.HasPrefix(launchedCredentials.NetworkProxyURL, "http://127.0.0.1:") {
		t.Fatalf("backend network capability = %#v launches=%d", launchedCredentials, launches)
	}
	process.finish(nil)
	if err := supervisor.closeNetworkBroker(egressBrokerCloseTimeout); err != nil {
		t.Fatalf("close network broker: %v", err)
	}
	if status := supervisor.Status(); status.WindowsHelperSHA256 != digest || status.Network.State != "unprepared" {
		t.Fatalf("stopped build identity = %#v", status)
	}
}

func TestSupervisorHelperDigestChangeBlocksBackendStart(t *testing.T) {
	generation := strings.Repeat("c", 64)
	digest := strings.Repeat("d", 64)
	identityValid := true
	launches := 0
	supervisor := newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
		BackendHost:     "127.0.0.2",
		NetworkRequired: true,
		LogOutput:       io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) {
			if !identityValid {
				return "", errors.New("digest changed")
			}
			return path, nil
		},
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) { return nil, errors.New("not used") },
		RunNetworkPreflight: func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			return networkPreflightResult(true, ""), nil
		},
		LaunchBackend: func(context.Context, BackendSpec, secretfd.Credentials, func(string)) (ManagedProcess, error) {
			launches++
			return newFakeManagedProcess(8102), nil
		},
	})
	supervisor.mu.Lock()
	supervisor.configured = true
	supervisor.config = Config{SchemaVersion: 1, Port: 18080, DownloadPath: "/mnt/c/Music"}
	supervisor.mu.Unlock()
	if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK {
		t.Fatal(response.Error)
	}
	if response := supervisor.preflightNetwork(); !response.OK {
		t.Fatal(response.Error)
	}
	identityValid = false
	if err := supervisor.start(); !errors.Is(err, errNetworkPreflightRequired) || launches != 0 {
		t.Fatalf("digest-changed start err=%v launches=%d", err, launches)
	}
	status := supervisor.Status().Network
	if status.State != "failed" || status.Stage != "helper_identity" || status.ProxyPropagated {
		t.Fatalf("digest-changed network status = %#v", status)
	}
	_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
}

func TestSupervisorPreflightReportsBrokerHelperIdentityFailure(t *testing.T) {
	generation := strings.Repeat("e", 64)
	digest := strings.Repeat("f", 64)
	verificationCount := 0
	supervisor := newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
		NetworkRequired: true,
		LogOutput:       io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) {
			verificationCount++
			if verificationCount >= 3 {
				return "", errors.New("identity changed")
			}
			return path, nil
		},
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) {
			t.Fatal("identity failure reached helper launch")
			return nil, errors.New("unreachable")
		},
		RunNetworkPreflight: func(_ context.Context, spec NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			connection, err := net.Dial("tcp", strings.TrimPrefix(spec.ProxyURL, "http://"))
			if err != nil {
				t.Fatalf("dial broker: %v", err)
			}
			defer connection.Close()
			_, _ = io.WriteString(connection, "CONNECT init.itunes.apple.com:443 HTTP/1.1\r\nHost: init.itunes.apple.com:443\r\n\r\n")
			line, _ := bufio.NewReader(connection).ReadString('\n')
			if !strings.Contains(line, "502") {
				t.Fatalf("identity failure response = %q", line)
			}
			return networkPreflightResult(false, "tls"), nil
		},
	})
	if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK {
		t.Fatalf("prepare: %#v", response)
	}
	response := supervisor.preflightNetwork()
	if response.OK || response.Error == nil || response.Error.Code != "network_preflight_failed" {
		t.Fatalf("preflight response = %#v", response)
	}
	if response.Status.Network.Stage != "helper_identity" {
		t.Fatalf("helper identity mutation stage = %q, want helper_identity", response.Status.Network.Stage)
	}
	_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
}

func TestSupervisorPreflightKeepsNativeStageWhenBrokerFailureHasNoSafeStage(t *testing.T) {
	generation := strings.Repeat("1", 64)
	digest := strings.Repeat("2", 64)
	var supervisor *Supervisor
	supervisor = newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
		NetworkRequired: true,
		LogOutput:       io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) {
			return path, nil
		},
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) {
			return nil, errors.New("not reached")
		},
		RunNetworkPreflight: func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			supervisor.networkBroker.recordHelperFailure("")
			return networkPreflightResult(false, "url_bag_fetch"), nil
		},
	})
	if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK {
		t.Fatalf("prepare: %#v", response)
	}
	response := supervisor.preflightNetwork()
	if response.OK || response.Error == nil || response.Error.Code != "network_preflight_failed" {
		t.Fatalf("preflight response = %#v", response)
	}
	if response.Status.Network.Stage != "url_bag_fetch" {
		t.Fatalf("empty broker stage overwrote native stage: %q", response.Status.Network.Stage)
	}
	_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
}

func TestSupervisorPreflightRejectsAndroidCABundleBeforeNativeProbe(t *testing.T) {
	for name, verifierError := range map[string]error{
		"missing": errAndroidCABundleMissing,
		"invalid": errAndroidCABundleInvalid,
	} {
		t.Run(name, func(t *testing.T) {
			generation := strings.Repeat("a", 64)
			digest := strings.Repeat("b", 64)
			probes := 0
			supervisor := newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
				NetworkRequired: true,
				LogOutput:       io.Discard,
				VerifyEgressHelper: func(path, _ string) (string, error) {
					return path, nil
				},
				LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) {
					return nil, errors.New("not reached")
				},
				VerifyAndroidCABundle: func(path, _ string) error {
					if path == "" {
						t.Fatal("CA bundle verifier did not receive the release path")
					}
					return verifierError
				},
				RunNetworkPreflight: func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
					probes++
					return networkPreflightResult(true, ""), nil
				},
			})
			if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK {
				t.Fatalf("prepare: %#v", response)
			}
			response := supervisor.preflightNetwork()
			if response.OK || response.Error == nil || response.Error.Code != "network_preflight_failed" {
				t.Fatalf("preflight response = %#v", response)
			}
			wantStage := "ca_bundle_invalid"
			if errors.Is(verifierError, errAndroidCABundleMissing) {
				wantStage = "ca_bundle_missing"
			}
			status := response.Status.Network
			if probes != 0 || status.SchemaVersion != 2 || status.Stage != wantStage ||
				status.CASource != "bundled_v1" || status.CurlCode != 0 || status.SSLVerifyResult != 0 {
				t.Fatalf("CA failure did not fail closed before native probe: probes=%d status=%#v", probes, status)
			}
			_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
		})
	}
}

func TestSupervisorPreflightRejectsCABundleOutsideFixedReleasePath(t *testing.T) {
	generation := strings.Repeat("a", 64)
	digest := strings.Repeat("b", 64)
	paths := testPaths(t.TempDir())
	paths.AndroidCABundle = filepath.Join(paths.CurrentLink, "rootfs", "etc", "ssl", "cert.pem")
	probes := 0
	supervisor := newNetworkTestSupervisor(paths, Options{
		NetworkRequired: true,
		LogOutput:       io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) {
			return path, nil
		},
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) {
			return nil, errors.New("not reached")
		},
		RunNetworkPreflight: func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			probes++
			return networkPreflightResult(true, ""), nil
		},
	})
	if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK {
		t.Fatalf("prepare: %#v", response)
	}
	response := supervisor.preflightNetwork()
	if response.OK || response.Error == nil || response.Error.Code != "network_preflight_failed" {
		t.Fatalf("preflight response = %#v", response)
	}
	if probes != 0 || response.Status.Network.Stage != "ca_bundle_invalid" {
		t.Fatalf("non-canonical CA path reached native probe: probes=%d status=%#v", probes, response.Status.Network)
	}
	_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
}

func TestSupervisorPreflightRejectsCompiledCABundlePathDrift(t *testing.T) {
	const driftedPath = "/tmp/not-the-android-trust-store.pem"
	originalPath := buildinfo.AndroidCABundlePath
	buildinfo.AndroidCABundlePath = driftedPath
	t.Cleanup(func() { buildinfo.AndroidCABundlePath = originalPath })

	generation := strings.Repeat("c", 64)
	digest := strings.Repeat("d", 64)
	verifierCalls := 0
	probeCalls := 0
	supervisor := newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
		NetworkRequired: true,
		LogOutput:       io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) {
			return path, nil
		},
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) {
			return nil, errors.New("not reached")
		},
		VerifyAndroidCABundle: func(string, string) error {
			verifierCalls++
			return nil
		},
		RunNetworkPreflight: func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			probeCalls++
			return networkPreflightResult(true, ""), nil
		},
	})
	if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK {
		t.Fatalf("prepare: %#v", response)
	}
	response := supervisor.preflightNetwork()
	if response.OK || response.Error == nil || response.Error.Code != "network_preflight_failed" {
		t.Fatalf("preflight response = %#v", response)
	}
	if verifierCalls != 0 || probeCalls != 0 || response.Status.Network.Stage != "ca_bundle_invalid" {
		t.Fatalf("compiled CA path drift reached validation/probe: verifier=%d probe=%d status=%#v", verifierCalls, probeCalls, response.Status.Network)
	}
	_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
}

func TestSupervisorPreflightRetriesFailedGenerationWithoutMisclassifyingIdentity(t *testing.T) {
	generation := strings.Repeat("3", 64)
	digest := strings.Repeat("4", 64)
	runs := 0
	supervisor := newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
		NetworkRequired:    true,
		LogOutput:          io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) { return path, nil },
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) {
			return nil, errors.New("not reached")
		},
		RunNetworkPreflight: func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			runs++
			if runs == 1 {
				return networkPreflightResult(false, "dns"), nil
			}
			return networkPreflightResult(true, ""), nil
		},
	})
	if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK {
		t.Fatalf("prepare: %#v", response)
	}
	if response := supervisor.preflightNetwork(); response.OK || response.Error == nil || response.Error.Code != "network_preflight_failed" {
		t.Fatalf("first preflight = %#v", response)
	}
	if response := supervisor.preflightNetwork(); !response.OK || response.Status.Network.State != "ready" || runs != 2 {
		t.Fatalf("retry preflight = %#v runs=%d", response, runs)
	}
	_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
}

func TestSupervisorPreflightRejectsConcurrentCheckingGeneration(t *testing.T) {
	generation := strings.Repeat("5", 64)
	digest := strings.Repeat("6", 64)
	entered := make(chan struct{})
	release := make(chan struct{})
	firstResult := make(chan Response, 1)
	supervisor := newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
		NetworkRequired:    true,
		LogOutput:          io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) { return path, nil },
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) {
			return nil, errors.New("not reached")
		},
		RunNetworkPreflight: func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			close(entered)
			<-release
			return networkPreflightResult(true, ""), nil
		},
	})
	if response := supervisor.prepareNetwork(networkPreparePayload(t, generation, digest)); !response.OK {
		t.Fatalf("prepare: %#v", response)
	}
	go func() { firstResult <- supervisor.preflightNetwork() }()
	<-entered
	response := supervisor.preflightNetwork()
	if response.OK || response.Error == nil || response.Error.Code != "network_preflight_busy" {
		t.Fatalf("concurrent preflight = %#v", response)
	}
	close(release)
	if response := <-firstResult; !response.OK || response.Status.Network.State != "ready" {
		t.Fatalf("first preflight = %#v", response)
	}
	_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
}

func TestSupervisorPreflightRejectsReplacementGenerationWhileChecking(t *testing.T) {
	firstGeneration := strings.Repeat("7", 64)
	secondGeneration := strings.Repeat("8", 64)
	digest := strings.Repeat("9", 64)
	verifyEntered := make(chan struct{})
	releaseVerify := make(chan struct{})
	var verifyMu sync.Mutex
	verifyCalls := 0
	supervisor := newNetworkTestSupervisor(testPaths(t.TempDir()), Options{
		NetworkRequired: true,
		LogOutput:       io.Discard,
		VerifyEgressHelper: func(path, _ string) (string, error) {
			verifyMu.Lock()
			verifyCalls++
			call := verifyCalls
			verifyMu.Unlock()
			if call == 2 {
				close(verifyEntered)
				<-releaseVerify
			}
			return path, nil
		},
		LaunchEgressHelper: func(context.Context, string) (EgressHelperProcess, error) {
			return nil, errors.New("not reached")
		},
		RunNetworkPreflight: func(context.Context, NativeNetworkPreflightSpec) (NativeNetworkPreflightResult, error) {
			return networkPreflightResult(true, ""), nil
		},
	})
	if response := supervisor.prepareNetwork(networkPreparePayload(t, firstGeneration, digest)); !response.OK {
		t.Fatalf("first prepare: %#v", response)
	}
	responseDone := make(chan Response, 1)
	go func() { responseDone <- supervisor.preflightNetwork() }()
	<-verifyEntered
	if response := supervisor.prepareNetwork(networkPreparePayload(t, secondGeneration, digest)); response.OK || response.Error == nil || response.Error.Code != "network_preflight_busy" {
		t.Fatalf("replacement prepare while checking: %#v", response)
	}
	close(releaseVerify)
	response := <-responseDone
	if !response.OK || response.Status.Network.State != "ready" {
		t.Fatalf("first preflight response = %#v", response)
	}
	status := supervisor.Status().Network
	if status.GenerationID != firstGeneration || status.State != "ready" || status.Stage != "complete" {
		t.Fatalf("checking generation was mutated: %#v", status)
	}
	_ = supervisor.closeNetworkBroker(egressBrokerCloseTimeout)
}
