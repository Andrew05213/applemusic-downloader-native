//go:build linux

package secretfd

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestInheritedDescriptorKeepsSecretsOutOfProcArgvAndEnvironment(t *testing.T) {
	if os.Getenv("AMDL_SECRET_FD_TEST_HELPER") == "1" {
		credentials, present, err := ReadFromEnvironment()
		if err != nil || !present || credentials.MediaUserToken == "" {
			os.Exit(20)
		}
		fmt.Println("ready")
		_, _ = bufio.NewReader(os.Stdin).ReadByte()
		os.Exit(0)
	}

	const sentinel = "proc-fd-token-sentinel-unique"
	document, err := Encode(Credentials{MediaUserToken: sentinel})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	command := exec.Command(os.Args[0], "-test.run=TestInheritedDescriptorKeepsSecretsOutOfProcArgvAndEnvironment")
	command.Env = append(os.Environ(), "AMDL_SECRET_FD_TEST_HELPER=1", EnvironmentKey+"=3")
	command.ExtraFiles = []*os.File{reader}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	_ = reader.Close()
	if _, err := writer.Write(document); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	_ = writer.Close()
	Wipe(document)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("helper readiness = %q, err=%v", line, err)
	}
	pid := strconv.Itoa(command.Process.Pid)
	for _, procFile := range []string{"/proc/" + pid + "/cmdline", "/proc/" + pid + "/environ"} {
		contents, err := os.ReadFile(procFile)
		if err != nil {
			t.Fatalf("read %s: %v", procFile, err)
		}
		if bytes.Contains(contents, []byte(sentinel)) {
			t.Fatalf("%s leaked credential sentinel", procFile)
		}
	}
	_ = stdin.Close()
	if err := command.Wait(); err != nil {
		t.Fatalf("wait helper: %v", err)
	}
}
