// Package nativeruntime controls a private Wrapper Lite/QEMU guest without
// WSL, a shell, browser sessions, or implicit credential discovery.
package nativeruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const WrapperCommit = "2fd7e0eaef081f93cf687428b965674937832ba4"

var (
	ErrBundleMissing  = errors.New("native_bundle_missing")
	ErrBundleInvalid  = errors.New("native_bundle_invalid")
	ErrBundleIdentity = errors.New("native_bundle_identity_mismatch")
	ErrInvalidState   = errors.New("native_state_invalid")
	ErrStateInUse     = errors.New("native_state_in_use")
	ErrCancelled      = errors.New("native_operation_cancelled")
	ErrGuestNotReady  = errors.New("native_guest_not_ready")
	ErrGuestExited    = errors.New("native_guest_exited")
	ErrProcessStart   = errors.New("native_process_start_failed")
	ErrPortInUse      = errors.New("native_port_in_use")
)

type FileIdentity struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Bundle struct {
	SchemaVersion   int                     `json:"schemaVersion"`
	WrapperCommit   string                  `json:"wrapperCommit"`
	Isolated        bool                    `json:"isolated,omitempty"`
	ControlProtocol int                     `json:"controlProtocol,omitempty"`
	QEMU            string                  `json:"qemu"`
	Firmware        string                  `json:"firmware"`
	Kernel          string                  `json:"kernel"`
	Initramfs       string                  `json:"initramfs"`
	DataTemplate    string                  `json:"dataTemplate"`
	Files           map[string]FileIdentity `json:"files"`
	root            string
}

func safeRelative(name string) bool {
	return name != "" && name != "." && path.Clean(name) == name && !strings.HasPrefix(name, "/") && !strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\:\x00\r\n")
}

func contained(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (b *Bundle) resolve(relative string) (string, error) {
	if !safeRelative(relative) {
		return "", ErrBundleInvalid
	}
	current := b.root
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", ErrBundleInvalid
		}
	}
	if !contained(b.root, current) {
		return "", ErrBundleInvalid
	}
	return current, nil
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader) error {
	buffer := make([]byte, 128*1024)
	for {
		if ctx.Err() != nil {
			return ErrCancelled
		}
		n, err := src.Read(buffer)
		if n > 0 {
			if _, writeErr := dst.Write(buffer[:n]); writeErr != nil {
				return writeErr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// BundleInfo describes the bounded manifest only. It is not an execution
// capability: payload identity must still pass LoadBundle before any launch.
type BundleInfo struct {
	Isolated        bool
	ControlProtocol int
}

func ReadBundleInfo(ctx context.Context, manifestPath string) (BundleInfo, error) {
	b, err := readBundleManifest(ctx, manifestPath)
	if err != nil {
		return BundleInfo{}, err
	}
	return BundleInfo{Isolated: b.Isolated, ControlProtocol: b.ControlProtocol}, nil
}

func readBundleManifest(ctx context.Context, manifestPath string) (*Bundle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil, ErrCancelled
	}
	manifest, err := os.Open(manifestPath)
	if err != nil {
		return nil, ErrBundleMissing
	}
	defer manifest.Close()
	info, err := manifest.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return nil, ErrBundleInvalid
	}
	decoder := json.NewDecoder(io.LimitReader(manifest, 1024*1024+1))
	decoder.DisallowUnknownFields()
	var b Bundle
	if decoder.Decode(&b) != nil {
		return nil, ErrBundleInvalid
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, ErrBundleInvalid
	}
	abs, err := filepath.Abs(filepath.Dir(manifestPath))
	if err != nil {
		return nil, ErrBundleInvalid
	}
	b.root, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, ErrBundleInvalid
	}
	if b.SchemaVersion != 1 || b.WrapperCommit != WrapperCommit || len(b.Files) < 5 || len(b.Files) > 4096 || !safeRelative(b.Firmware) || (b.ControlProtocol != 0 && b.ControlProtocol != 1) {
		return nil, ErrBundleInvalid
	}
	roles := map[string]bool{}
	for _, role := range []string{b.QEMU, b.Kernel, b.Initramfs, b.DataTemplate} {
		if !safeRelative(role) || roles[strings.ToLower(role)] {
			return nil, ErrBundleInvalid
		}
		roles[strings.ToLower(role)] = true
		if _, ok := b.Files[role]; !ok {
			return nil, ErrBundleInvalid
		}
	}
	if path.Base(b.QEMU) != "qemu-system-x86_64.exe" {
		return nil, ErrBundleInvalid
	}
	if _, ok := b.Files[b.Firmware+"/bios-256k.bin"]; !ok {
		return nil, ErrBundleInvalid
	}
	seen := map[string]bool{}
	for name, identity := range b.Files {
		if !safeRelative(name) || seen[strings.ToLower(name)] || len(identity.SHA256) != 64 || identity.Size <= 0 {
			return nil, ErrBundleInvalid
		}
		seen[strings.ToLower(name)] = true
		decoded, err := hex.DecodeString(identity.SHA256)
		if err != nil || len(decoded) != sha256.Size || strings.ToLower(identity.SHA256) != identity.SHA256 {
			return nil, ErrBundleInvalid
		}
	}
	return &b, nil
}

func LoadBundle(ctx context.Context, manifestPath string) (*Bundle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	b, err := readBundleManifest(ctx, manifestPath)
	if err != nil {
		return nil, err
	}
	for name, identity := range b.Files {
		file, err := b.resolve(name)
		if err != nil {
			return nil, err
		}
		f, err := os.Open(file)
		if err != nil {
			return nil, ErrBundleInvalid
		}
		stat, err := f.Stat()
		if err != nil || !stat.Mode().IsRegular() || stat.Size() != identity.Size {
			_ = f.Close()
			return nil, ErrBundleIdentity
		}
		hash := sha256.New()
		err = copyContext(ctx, hash, f)
		_ = f.Close()
		if errors.Is(err, ErrCancelled) {
			return nil, ErrCancelled
		}
		if err != nil || hex.EncodeToString(hash.Sum(nil)) != identity.SHA256 {
			return nil, ErrBundleIdentity
		}
	}
	return b, nil
}

func prepareState(ctx context.Context, b *Bundle, stateDir string) (io.Closer, error) {
	if ctx.Err() != nil {
		return nil, ErrCancelled
	}
	if !filepath.IsAbs(stateDir) || contained(b.root, stateDir) {
		return nil, ErrInvalidState
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return nil, ErrInvalidState
	}
	canonical, err := filepath.EvalSymlinks(stateDir)
	if err != nil || contained(b.root, canonical) {
		return nil, ErrInvalidState
	}
	lease, err := lockState(filepath.Join(canonical, "native-runtime.lock"))
	if err != nil {
		return nil, ErrStateInUse
	}
	fail := func() (io.Closer, error) { _ = lease.Close(); return nil, ErrInvalidState }
	destination := filepath.Join(canonical, "data.img")
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() || info.Size() != b.Files[b.DataTemplate].Size {
			return fail()
		}
		return lease, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail()
	}
	source, err := os.Open(filepath.Join(b.root, filepath.FromSlash(b.DataTemplate)))
	if err != nil {
		return fail()
	}
	defer source.Close()
	image, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fail()
	}
	copyErr := copyContext(ctx, image, source)
	syncErr := image.Sync()
	closeErr := image.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		if errors.Is(copyErr, ErrCancelled) {
			_ = lease.Close()
			return nil, ErrCancelled
		}
		return fail()
	}
	return lease, nil
}

func (b *Bundle) command(stateDir string, port int) Command {
	asset := func(name string) string { return filepath.Join(b.root, filepath.FromSlash(name)) }
	// This contains service-only, non-secret args. No login input is accepted.
	guestArgs := "--base-dir\n/data\n--host\n0.0.0.0\n--port\n12340\n"
	// QEMU's Windows argument parsers cannot reliably open absolute Unicode
	// paths. CreateProcessW handles the executable and Unicode CWD; all guest
	// inputs below use verified, ASCII relative names from the owned state.
	inputs := b.runtimeInputsDirectory()
	network := "user,model=e1000,hostfwd=tcp:127.0.0.1:" + strconv.Itoa(port) + "-:12340"
	if b.Isolated {
		network += ",restrict=on"
	}
	command := Command{Path: asset(b.QEMU), Cwd: stateDir, Args: []string{
		"-L", inputs + "/firmware", "-accel", "tcg", "-cpu", "max", "-m", "512", "-smp", "2",
		"-kernel", inputs + "/kernel", "-initrd", inputs + "/initrd",
		"-append", "console=ttyS0 quiet net.ifnames=0 biosdevname=0 lite_args_b64=" + base64.StdEncoding.EncodeToString([]byte(guestArgs)),
		"-display", "none", "-serial", "null", "-monitor", "none", "-no-reboot",
		"-nic", network,
		"-drive", "file=data.img,format=raw,if=virtio",
	}}
	if b.ControlProtocol == 1 {
		command.Args = append(command.Args, "-fw_cfg", "name=opt/amdl/control,file=control.secret")
	}
	return command
}

func prepareBootCapability(stateDir string) (string, error) {
	file := filepath.Join(stateDir, "control.secret")
	if info, err := os.Lstat(file); err == nil {
		if !info.Mode().IsRegular() || info.Size() != 64 {
			return "", ErrInvalidState
		}
		old, err := os.ReadFile(file)
		if err != nil {
			return "", ErrInvalidState
		}
		if _, err := hex.DecodeString(string(old)); err != nil {
			return "", ErrInvalidState
		}
		if os.Remove(file) != nil {
			return "", ErrInvalidState
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", ErrInvalidState
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", ErrInvalidState
	}
	capability := hex.EncodeToString(raw)
	out, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", ErrInvalidState
	}
	_, writeErr := io.WriteString(out, capability)
	syncErr, closeErr := out.Sync(), out.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return "", ErrInvalidState
	}
	return capability, nil
}

func (b *Bundle) runtimeInputs() map[string]string {
	inputs := map[string]string{"kernel": b.Kernel, "initrd": b.Initramfs}
	for name := range b.Files {
		if strings.HasPrefix(name, b.Firmware+"/") {
			inputs["firmware/"+strings.TrimPrefix(name, b.Firmware+"/")] = name
		}
	}
	return inputs
}

func (b *Bundle) runtimeInputsDirectory() string {
	// A bundle upgrade uses a new derived directory; never overwrite inputs
	// or the account image belonging to a previous runtime.
	inputs := b.runtimeInputs()
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		identity := b.Files[inputs[name]]
		_, _ = io.WriteString(h, name+"\x00"+identity.SHA256+"\x00"+strconv.FormatInt(identity.Size, 10)+"\n")
	}
	return "runtime-" + hex.EncodeToString(h.Sum(nil))
}

func privateDirectories(root, relative string) error {
	current := root
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		if err := os.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return ErrInvalidState
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidState
		}
	}
	return nil
}

func verifyRuntimeInput(ctx context.Context, file string, identity FileIdentity) error {
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() != identity.Size {
		return ErrBundleIdentity
	}
	f, err := os.Open(file)
	if err != nil {
		return ErrBundleIdentity
	}
	defer f.Close()
	h := sha256.New()
	if err := copyContext(ctx, h, f); err != nil {
		if errors.Is(err, ErrCancelled) {
			return ErrCancelled
		}
		return ErrBundleIdentity
	}
	if hex.EncodeToString(h.Sum(nil)) != identity.SHA256 {
		return ErrBundleIdentity
	}
	return nil
}

// Called only while holding the state lease. Copies are ordinary files, never
// hardlinks to the immutable bundle. Unknown/corrupted cache files fail closed.
func prepareRuntimeInputs(ctx context.Context, b *Bundle, stateDir string) error {
	if ctx.Err() != nil {
		return ErrCancelled
	}
	directory := b.runtimeInputsDirectory()
	for name, sourceName := range b.runtimeInputs() {
		for _, r := range name {
			if r < 32 || r > 126 {
				return ErrBundleInvalid
			}
		}
		if !safeRelative(name) {
			return ErrBundleInvalid
		}
		relative := directory + "/" + name
		if err := privateDirectories(stateDir, path.Dir(relative)); err != nil {
			return err
		}
		destination := filepath.Join(stateDir, filepath.FromSlash(relative))
		identity := b.Files[sourceName]
		if _, err := os.Lstat(destination); err == nil {
			if err := verifyRuntimeInput(ctx, destination, identity); err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return ErrInvalidState
		}
		source, err := b.resolve(sourceName)
		if err != nil {
			return err
		}
		if err := copyRuntimeInput(ctx, source, destination, identity); err != nil {
			return err
		}
	}
	return nil
}

func copyRuntimeInput(ctx context.Context, source, destination string, identity FileIdentity) error {
	in, err := os.Open(source)
	if err != nil {
		return ErrBundleInvalid
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrInvalidState
	}
	copyErr := copyContext(ctx, out, in)
	syncErr, closeErr := out.Sync(), out.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(destination) // Only the file created exclusively above.
		if errors.Is(copyErr, ErrCancelled) {
			return ErrCancelled
		}
		return ErrInvalidState
	}
	return verifyRuntimeInput(ctx, destination, identity)
}
