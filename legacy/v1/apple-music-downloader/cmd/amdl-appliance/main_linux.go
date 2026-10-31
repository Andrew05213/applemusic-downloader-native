//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/Andrew05213/applemusic_downloader/internal/appliance"
	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

const applianceBackendHostEnvironment = "AMDL_APPLIANCE_BACKEND_HOST"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(arguments []string, input io.Reader, output, errorOutput io.Writer) int {
	if len(arguments) != 1 {
		fmt.Fprintln(errorOutput, "usage: amdl-appliance probe|status|configure-stdin|credential-stdin|network-prepare-stdin|network-preflight|network-cleanup-stdin|start|stop|update-stdin|diagnostics|proxy-stdio")
		return 2
	}
	operation := arguments[0]
	paths := appliance.DefaultPaths()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if appliance.IsSupervisorOperation(operation) {
		supervisor := appliance.NewSupervisor(paths, supervisorOptionsFromEnvironment())
		if err := supervisor.Serve(ctx); err != nil {
			fmt.Fprintln(errorOutput, "appliance supervisor failed")
			return 1
		}
		return 0
	}
	if !appliance.IsPublicOperation(operation) {
		fmt.Fprintln(errorOutput, "unsupported appliance operation")
		return 2
	}
	if operation == appliance.OperationProbe {
		return writeResponse(output, appliance.ProbeResponse())
	}
	client := appliance.Client{SocketPath: paths.SocketPath}
	if operation == appliance.OperationProxy {
		if err := client.Proxy(ctx, input, output); err != nil {
			writeProxyFailure(errorOutput, err)
			return 1
		}
		return 0
	}

	var payload []byte
	var err error
	switch operation {
	case appliance.OperationConfigure, appliance.OperationUpdate, appliance.OperationNetworkPrepare, appliance.OperationNetworkCleanup:
		payload, err = appliance.ReadLimited(input, appliance.MaxControlBytes)
	case appliance.OperationCredential:
		payload, err = appliance.ReadLimited(input, secretfd.MaxDocumentBytes)
	}
	if err != nil {
		fmt.Fprintln(errorOutput, "appliance input is invalid")
		return 2
	}
	response, err := client.Call(ctx, operation, payload)
	secretfd.Wipe(payload)
	if err != nil {
		fmt.Fprintln(errorOutput, "appliance control request failed")
		return 1
	}
	if writeResponse(output, response) != 0 {
		return 1
	}
	if !response.OK {
		return 1
	}
	return 0
}

func supervisorOptionsFromEnvironment() appliance.Options {
	return appliance.Options{BackendHost: os.Getenv(applianceBackendHostEnvironment), NetworkRequired: true}
}

func writeProxyFailure(output io.Writer, err error) {
	fmt.Fprintf(output, "appliance proxy failed: %s\n", appliance.ProxyStage(err))
}

func writeResponse(output io.Writer, response appliance.Response) int {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(true)
	var payload any = response
	if response.OK {
		if response.Logs != nil {
			payload = struct {
				appliance.Status
				Logs []string `json:"logs"`
			}{Status: response.Status, Logs: response.Logs}
		} else {
			payload = response.Status
		}
	}
	if err := encoder.Encode(payload); err != nil {
		return 1
	}
	return 0
}
