package nativeruntime

// Command is built exclusively from verified bundle roles and owned state.
// It has no shell string, inherited credentials, or configurable guest args.
type Command struct {
	Path        string
	Args        []string
	Cwd         string
	captureBoot bool
}

type Process interface {
	Done() <-chan struct{}
	Stop() error
	Err() error
}

// Read only known numeric exit status, never executable arguments or stderr.
func processExitCode(p Process) *uint32 {
	if source, ok := p.(interface{ ExitCode() *uint32 }); ok {
		return source.ExitCode()
	}
	return nil
}

func processBootEvent(p Process) string {
	if source, ok := p.(interface{ BootEvent() string }); ok {
		return source.BootEvent()
	}
	return ""
}
