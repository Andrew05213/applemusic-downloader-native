package nativeruntime

// Command is built exclusively from verified bundle roles and owned state.
// It has no shell string, inherited credentials, or configurable guest args.
type Command struct {
	Path string
	Args []string
	Cwd  string
}

type Process interface {
	Done() <-chan struct{}
	Stop() error
	Err() error
}
