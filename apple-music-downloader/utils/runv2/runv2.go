package runv2

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/grafov/m3u8"

	"encoding/binary"
	"github.com/schollz/progressbar/v3"

	"github.com/Andrew05213/applemusic_downloader/internal/mp4audio"
	"github.com/Andrew05213/applemusic_downloader/internal/transfer"
	"github.com/Andrew05213/applemusic_downloader/utils/structs"
)

const (
	prefetchKey              = "skd://itunes.apple.com/P000000000/s1/e1"
	playlistRequestTimeout   = 30 * time.Second
	mediaRequestTimeout      = 10 * time.Minute
	decryptorDialTimeout     = 5 * time.Second
	decryptorIOTimeout       = 30 * time.Second
	maxPlaylistResponseBytes = int64(8 << 20)
)

var ErrTimeout = errors.New("response timed out")

var ErrResponseTooLarge = errors.New("response exceeds configured size limit")

type decryptorContextKey struct{}
type decryptorOverride struct {
	dial    func(context.Context) (net.Conn, error)
	timeout time.Duration
}

// WithDecryptor replaces only the sample transport for the native runtime.
// Legacy CLI/Docker use the unchanged TCP contract when no override exists.
func WithDecryptor(ctx context.Context, dial func(context.Context) (net.Conn, error)) context.Context {
	return context.WithValue(ctx, decryptorContextKey{}, decryptorOverride{dial: dial, timeout: 90 * time.Second})
}

// ErrMediaTransferStalled marks an encrypted media response which stopped
// delivering bytes. It also wraps context.DeadlineExceeded at the call site so
// the job queue classifies it as a retryable timeout.
var ErrMediaTransferStalled = errors.New("media transfer stalled")

var (
	mediaResponseHeaderTimeout = 30 * time.Second
	mediaReadIdleTimeout       = 30 * time.Second
	errMediaReadIdle           = errors.New("media read idle timeout")
)

type mediaIdleWatchdog struct {
	touch  chan struct{}
	done   chan struct{}
	cancel context.CancelCauseFunc
	once   sync.Once
}

func newMediaIdleWatchdog(ctx context.Context, cancel context.CancelCauseFunc, timeout time.Duration) *mediaIdleWatchdog {
	watchdog := &mediaIdleWatchdog{
		touch:  make(chan struct{}, 1),
		done:   make(chan struct{}),
		cancel: cancel,
	}
	if timeout <= 0 {
		return watchdog
	}
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				watchdog.cancel(errMediaReadIdle)
				return
			case <-watchdog.touch:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(timeout)
			case <-watchdog.done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return watchdog
}

func (w *mediaIdleWatchdog) Touch() {
	if w == nil {
		return
	}
	select {
	case w.touch <- struct{}{}:
	default:
	}
}

func (w *mediaIdleWatchdog) Stop() {
	if w == nil {
		return
	}
	w.once.Do(func() { close(w.done) })
}

type activityReadCloser struct {
	io.ReadCloser
	touch func()
}

func (r activityReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 && r.touch != nil {
		r.touch()
	}
	return n, err
}

func mediaTransferError(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), errMediaReadIdle) {
		return fmt.Errorf("%w: %w", ErrMediaTransferStalled, context.DeadlineExceeded)
	}
	return err
}

func newMediaHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = mediaResponseHeaderTimeout
	return &http.Client{
		Transport:     transport,
		Timeout:       mediaRequestTimeout,
		CheckRedirect: stripRefererOnCrossOriginRedirect,
	}
}

func newPlaylistHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       playlistRequestTimeout,
		CheckRedirect: stripRefererOnCrossOriginRedirect,
	}
}

// stripRefererOnCrossOriginRedirect preserves CDN redirects while preventing
// Go's automatic Referer header from disclosing a signed source URL.
func stripRefererOnCrossOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 || req == nil || req.URL == nil || via[0] == nil || via[0].URL == nil {
		return http.ErrUseLastResponse
	}
	original := via[0].URL
	if !strings.EqualFold(original.Scheme, req.URL.Scheme) ||
		!strings.EqualFold(original.Hostname(), req.URL.Hostname()) ||
		original.Port() != req.URL.Port() {
		req.Header.Del("Referer")
	}
	return nil
}

type TimedResponseBody struct {
	timeout   time.Duration
	timer     *time.Timer
	threshold int
	body      io.Reader
}

func (b *TimedResponseBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if err != nil {
		return n, err
	}
	// fmt.Printf("Read %d bytes, buffer size %d bytes", n, len(p))
	if n >= b.threshold {
		b.timer.Reset(b.timeout)
	}
	return n, err
}

// Run is retained for callers that have not yet been migrated to a request
// context. New download paths should call RunContext so cancellation reaches
// both HTTP requests and the local decryptor connection.
func Run(adamId string, playlistUrl string, outfile string, Config structs.ConfigSet) error {
	return RunContext(context.Background(), adamId, playlistUrl, outfile, Config)
}

// RunContext downloads and decrypts one ALAC/Atmos stream. All network setup
// is tied to ctx so a cancelled job does not continue waiting on Apple or the
// local decryptor.
func RunContext(ctx context.Context, adamId string, playlistUrl string, outfile string, Config structs.ConfigSet) error {
	if ctx == nil {
		ctx = context.Background()
	}

	header := make(http.Header)

	// request media playlist
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, playlistUrl, nil)
	if err != nil {
		return err
	}
	req.Header = header
	playlistResponse, err := newPlaylistHTTPClient().Do(req)
	if err != nil {
		return err
	}
	if playlistResponse.StatusCode != http.StatusOK {
		_ = playlistResponse.Body.Close()
		return fmt.Errorf("playlist request: HTTP %s", playlistResponse.Status)
	}
	if playlistResponse.ContentLength > maxPlaylistResponseBytes {
		_ = playlistResponse.Body.Close()
		return fmt.Errorf("playlist response: %w", ErrResponseTooLarge)
	}

	// parse m3u8
	segments, err := parseMediaPlaylist(&limitedReadCloser{ReadCloser: playlistResponse.Body, remaining: maxPlaylistResponseBytes})
	if err != nil {
		return err
	}
	if len(segments) == 0 {
		return errors.New("no segments extracted from playlist")
	}
	segment := segments[0]
	if segment == nil {
		return errors.New("no segments extracted from playlist")
	}
	if segment.Limit <= 0 {
		return errors.New("non-byterange playlists are currently unsupported")
	}

	// get URL to the actual file
	parsedUrl, err := url.Parse(playlistUrl)
	if err != nil {
		return err
	}
	fileUrl, err := parsedUrl.Parse(segment.URI)
	if err != nil {
		return err
	}

	// A CDN connection can successfully return headers and then stop emitting
	// bytes. Keep its request context separate so the watchdog can abort that
	// transfer without cancelling the enclosing job.
	mediaContext, cancelMedia := context.WithCancelCause(ctx)
	defer cancelMedia(nil)

	// request mp4
	req, err = http.NewRequestWithContext(mediaContext, http.MethodGet, fileUrl.String(), nil)
	if err != nil {
		return err
	}
	req.Header = header

	var body io.Reader
	mediaResponse, err := newMediaHTTPClient().Do(req)
	if err != nil {
		return mediaTransferError(mediaContext, err)
	}
	defer mediaResponse.Body.Close()
	if mediaResponse.StatusCode != http.StatusOK {
		return fmt.Errorf("media request: HTTP %s", mediaResponse.Status)
	}
	transfer.SetTotal(ctx, mediaResponse.ContentLength, false)
	watchdog := newMediaIdleWatchdog(mediaContext, cancelMedia, mediaReadIdleTimeout)
	defer watchdog.Stop()
	mediaBody, finishTransfer := transfer.Track(ctx, activityReadCloser{ReadCloser: mediaResponse.Body, touch: watchdog.Touch})
	defer finishTransfer()
	if mediaResponse.ContentLength >= 0 && mediaResponse.ContentLength < int64(Config.MaxMemoryLimit*1024*1024) {
		var buffer bytes.Buffer
		bar := progressbar.NewOptions64(
			mediaResponse.ContentLength,
			progressbar.OptionClearOnFinish(),
			progressbar.OptionSetElapsedTime(false),
			progressbar.OptionSetPredictTime(false),
			progressbar.OptionShowElapsedTimeOnFinish(),
			progressbar.OptionShowCount(),
			progressbar.OptionEnableColorCodes(true),
			progressbar.OptionShowBytes(true),
			progressbar.OptionSetDescription("Downloading..."),
			progressbar.OptionSetTheme(progressbar.Theme{
				Saucer:        "",
				SaucerHead:    "",
				SaucerPadding: "",
				BarStart:      "",
				BarEnd:        "",
			}),
		)
		if _, err := io.Copy(io.MultiWriter(&buffer, bar), mediaBody); err != nil {
			return mediaTransferError(mediaContext, err)
		}
		watchdog.Stop()
		finishTransfer()
		body = &buffer
		fmt.Print("Downloaded\n")
	} else {
		body = mediaBody
	}

	totalLen := mediaResponse.ContentLength
	// connect to decryptor
	transfer.Phase(ctx, "decrypting")
	addr := Config.DecryptM3u8Port
	conn, err := dialDecryptor(ctx, addr)
	if err != nil {
		return err
	}
	timeout := decryptorIOTimeout
	if native, ok := ctx.Value(decryptorContextKey{}).(decryptorOverride); ok {
		timeout = native.timeout
	}
	boundedConn := deadlineConn{Conn: conn, timeout: timeout}
	defer Close(boundedConn)
	stopClosingConn := closeConnOnCancel(ctx, conn)
	defer stopClosingConn()

	err = downloadAndDecryptFileContext(ctx, boundedConn, body, outfile, adamId, segments, totalLen, Config)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return mediaTransferError(mediaContext, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Print("Decrypted\n")
	return nil
}

func dialDecryptor(ctx context.Context, address string) (net.Conn, error) {
	if native, ok := ctx.Value(decryptorContextKey{}).(decryptorOverride); ok && native.dial != nil {
		return native.dial(ctx)
	}
	dialer := net.Dialer{Timeout: decryptorDialTimeout}
	return dialer.DialContext(ctx, "tcp", address)
}

// limitedReadCloser prevents a malformed playlist response from consuming
// unbounded memory while preserving the original close ownership contract.
type limitedReadCloser struct {
	io.ReadCloser
	remaining int64
}

func (r *limitedReadCloser) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.ReadCloser.Read(probe[:])
		if n > 0 {
			return 0, ErrResponseTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.ReadCloser.Read(p)
	r.remaining -= int64(n)
	return n, err
}

// deadlineConn puts a bound on every local decryptor read and write. The
// caller renews the deadline per operation, so healthy streams are not cut
// off solely because a complete download takes a long time.
type deadlineConn struct {
	net.Conn
	timeout time.Duration
}

func (c deadlineConn) Read(p []byte) (int, error) {
	if c.timeout > 0 {
		if err := c.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
			return 0, err
		}
	}
	return c.Conn.Read(p)
}

func (c deadlineConn) Write(p []byte) (int, error) {
	if c.timeout > 0 {
		if err := c.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
			return 0, err
		}
	}
	return c.Conn.Write(p)
}

// closeConnOnCancel turns context cancellation into a connection close so a
// blocking local decryptor read cannot outlive the job that started it.
func closeConnOnCancel(ctx context.Context, conn net.Conn) func() {
	if ctx.Done() == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

func downloadAndDecryptFile(conn io.ReadWriter, in io.Reader, outfile string,
	adamId string, playlistSegments []*m3u8.MediaSegment, totalLen int64, Config structs.ConfigSet) error {
	return downloadAndDecryptFileContext(context.Background(), conn, in, outfile, adamId, playlistSegments, totalLen, Config)
}
func downloadAndDecryptFileContext(ctx context.Context, conn io.ReadWriter, in io.Reader, outfile string, adamId string, playlistSegments []*m3u8.MediaSegment, totalLen int64, Config structs.ConfigSet) error {
	// m3u8 stores unused capacity as trailing nil entries. Count actual entries,
	// not slice capacity, and require every declared fragment to arrive.
	expected := 0
	for _, segment := range playlistSegments {
		if segment == nil {
			break
		}
		expected++
	}
	if expected == 0 {
		return errors.New("media playlist has no fragments")
	}
	var buffer bytes.Buffer
	var outBuf *bufio.Writer
	var outputFile *os.File
	maxMemorySize := int64(Config.MaxMemoryLimit * 1024 * 1024)
	useMemoryBuffer := totalLen >= 0 && totalLen <= maxMemorySize
	inBuf := bufio.NewReader(in)
	if useMemoryBuffer {
		outBuf = bufio.NewWriter(&buffer)
	} else {
		ofh, err := os.Create(outfile)
		if err != nil {
			return err
		}
		defer ofh.Close()
		outputFile = ofh
		outBuf = bufio.NewWriter(ofh)
	}
	init, offset, err := ReadInitSegment(inBuf)
	if err != nil {
		return err
	}
	if init == nil {
		return errors.New("no init segment found")
	}

	tracks, err := TransformInit(init)
	if err != nil {
		return err
	}
	finalizer, err := mp4audio.New(init)
	if err != nil {
		return err
	}
	initialSize := init.Size()
	writtenOffset := initialSize
	err = init.Encode(outBuf)
	if err != nil {
		return err
	}

	// 'segment' in m3u8 == 'fragment' in mp4ff
	//fmt.Println("Starting decryption...")
	bar := progressbar.NewOptions64(totalLen,
		progressbar.OptionClearOnFinish(),
		progressbar.OptionSetElapsedTime(false),
		progressbar.OptionSetPredictTime(false),
		progressbar.OptionShowElapsedTimeOnFinish(),
		progressbar.OptionShowCount(),
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetDescription("Decrypting..."),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "",
			SaucerHead:    "",
			SaucerPadding: "",
			BarStart:      "",
			BarEnd:        "",
		}),
	)
	bar.Add64(int64(offset))
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	for i := 0; ; i++ {
		var frag *mp4.Fragment
		rawoffset := offset
		frag, offset, err = ReadNextFragment(inBuf, offset)
		rawoffset = offset - rawoffset
		if err != nil {
			return err
		}
		if frag == nil {
			if i != expected || (totalLen >= 0 && offset != uint64(totalLen)) {
				return io.ErrUnexpectedEOF
			}
			break
		}
		// print progress

		// if totalLen > 0 {
		// 	fmt.Printf("%.2f%% of %d bytes\n", 100*float32(offset)/float32(totalLen), totalLen)
		// }
		if i >= expected {
			return errors.New("media has undeclared fragments")
		}
		segment := playlistSegments[i]
		if segment == nil {
			return errors.New("segment number out of sync")
		}
		key := segment.Key
		if key != nil {
			if i != 0 {
				if err := SwitchKeys(rw); err != nil {
					return err
				}
			}
			if key.URI == prefetchKey {
				if err := SendString(rw, "0"); err != nil {
					return err
				}
			} else {
				if err := SendString(rw, adamId); err != nil {
					return err
				}
			}
			if err := SendString(rw, key.URI); err != nil {
				return err
			}
		}
		// flushes the buffer
		err = DecryptFragment(frag, tracks, rw)
		if err != nil {
			return fmt.Errorf("decryptFragment: %w", err)
		}
		if err := finalizer.Fragment(frag.Moof); err != nil {
			return err
		}
		if err := normalizeOutputFragmentBase(frag, writtenOffset); err != nil {
			return err
		}
		err = frag.Encode(outBuf)
		if err != nil {
			return err
		}
		writtenOffset += frag.Size()
		transfer.Processed(ctx, int64(offset), totalLen)
		bar.Add64(int64(rawoffset))
	}
	err = outBuf.Flush()
	if err != nil {
		return err
	}
	if err := finalizer.Finish(); err != nil {
		return err
	}
	var completedInit bytes.Buffer
	if err := init.Encode(&completedInit); err != nil {
		return err
	}
	if uint64(completedInit.Len()) != initialSize {
		return errors.New("audio init changed size while finalizing duration")
	}
	if useMemoryBuffer {
		copy(buffer.Bytes(), completedInit.Bytes())
		// create output file
		ofh, err := os.Create(outfile)
		if err != nil {
			return err
		}
		defer ofh.Close()
		n, err := ofh.Write(buffer.Bytes())
		if err != nil {
			return err
		}
		if n != buffer.Len() {
			return io.ErrShortWrite
		}
		return ofh.Close()
	}
	n, err := outputFile.WriteAt(completedInit.Bytes(), 0)
	if err != nil {
		return err
	}
	if n != completedInit.Len() {
		return io.ErrShortWrite
	}
	return outputFile.Close()
}

func normalizeOutputFragmentBase(frag *mp4.Fragment, outputStart uint64) error {
	if len(frag.Moof.Trafs) == 1 && len(frag.Moof.Traf.Truns) == 1 {
		// Encode recalculates the sole run's data offset from the final moof
		// size. Make that base explicitly moof-relative, including inputs whose
		// absolute base pointed directly at mdat and omitted trun.data_offset.
		h := frag.Moof.Traf.Tfhd
		h.Flags = (h.Flags &^ 1) | 0x020000
		h.BaseDataOffset = 0
		frag.Moof.Traf.Trun.Flags |= mp4.TrunDataOffsetPresentFlag
		return nil
	}
	// Multi-run Encode retains decoded run offsets. Relocate only a known
	// moof-anchored absolute base; do not guess unsupported implicit anchors.
	moofStart := outputStart
	for _, child := range frag.Children {
		if child == frag.Moof {
			break
		}
		moofStart += child.Size()
	}
	for _, traf := range frag.Moof.Trafs {
		if traf.Tfhd.HasBaseDataOffset() {
			if traf.Tfhd.BaseDataOffset != frag.Moof.StartPos {
				return errors.New("unsupported multi-run absolute audio base")
			}
			traf.Tfhd.BaseDataOffset = moofStart
		}
	}
	return nil
}

// Workaround for m3u8 not supporting multiple keys - remove
// PlayReady and Widevine
func filterResponse(f io.Reader) (*bytes.Buffer, error) {
	buf := &bytes.Buffer{}
	scanner := bufio.NewScanner(f)

	prefix := []byte("#EXT-X-KEY:")
	keyFormat := []byte("streamingkeydelivery")
	for scanner.Scan() {
		lineBytes := scanner.Bytes()
		if bytes.HasPrefix(lineBytes, prefix) && !bytes.Contains(lineBytes, keyFormat) {
			continue
		}
		_, err := buf.Write(lineBytes)
		if err != nil {
			return nil, err
		}
		_, err = buf.WriteString("\n")
		if err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return buf, nil
}

func parseMediaPlaylist(r io.ReadCloser) ([]*m3u8.MediaSegment, error) {
	defer r.Close()
	playlistBuf, err := filterResponse(r)
	if err != nil {
		return nil, err
	}

	playlist, listType, err := m3u8.Decode(*playlistBuf, true)
	if err != nil {
		return nil, err
	}

	if listType != m3u8.MEDIA {
		return nil, errors.New("m3u8 not of media type")
	}

	mediaPlaylist := playlist.(*m3u8.MediaPlaylist)
	return mediaPlaylist.Segments, nil
}

// pasing
func ReadInitSegment(r io.Reader) (*mp4.InitSegment, uint64, error) {
	var offset uint64 = 0
	init := mp4.NewMP4Init()
	for i := 0; i < 2; i++ {
		box, err := mp4.DecodeBox(offset, r)
		if err != nil {
			return nil, offset, err
		}
		boxType := box.Type()
		if boxType != "ftyp" && boxType != "moov" {
			return nil, offset, fmt.Errorf("unexpected box type %s, should be ftyp or moov", boxType)
		}
		init.AddChild(box)
		offset += box.Size()
	}
	if init.Ftyp == nil || init.Moov == nil || len(init.Moov.Traks) == 0 {
		return nil, offset, errors.New("media init has no usable track")
	}
	for _, track := range init.Moov.Traks {
		if track.Tkhd == nil || track.Mdia == nil || track.Mdia.Minf == nil || track.Mdia.Minf.Stbl == nil || track.Mdia.Minf.Stbl.Stsd == nil || len(track.Mdia.Minf.Stbl.Stsd.Children) == 0 {
			return nil, offset, errors.New("media init has incomplete track metadata")
		}
	}
	return init, offset, nil
}

// Get the next fragment. Returns nil and no error on EOF
func ReadNextFragment(r io.Reader, offset uint64) (*mp4.Fragment, uint64, error) {
	frag := mp4.NewFragment()
	for {
		box, err := mp4.DecodeBox(offset, r)
		if err == io.EOF {
			if len(frag.Children) != 0 {
				return nil, offset, io.ErrUnexpectedEOF
			}
			return nil, offset, nil
		}
		if err != nil {
			return nil, offset, err
		}
		boxType := box.Type()
		// fmt.Printf("processing %s, box starts @ offset %d\n", boxType, offset)
		offset += box.Size()
		if boxType == "moof" || boxType == "emsg" || boxType == "prft" {
			frag.AddChild(box)
			continue
		}
		if boxType == "mdat" {
			frag.AddChild(box)
			break
		}
		fmt.Printf("ignoring a %s box found mid-stream", boxType)
	}
	// only 1 mdat box in fragment, meaning that the box doesn't have a preceding moof box
	if frag.Moof == nil {
		return nil, offset, fmt.Errorf("more than one mdat box in fragment (box ends @ offset %d)", offset)
	}
	return frag, offset, nil
}

// Return a new slice of boxes with encryption-related sbgp and sgpd removed,
// and the total number of bytes removed.
// Non-encryption-related ones such as 'roll' are left untouched.
func FilterSbgpSgpd(children []mp4.Box) ([]mp4.Box, uint64) {
	var bytesRemoved uint64 = 0
	remainingChildren := make([]mp4.Box, 0, len(children))
	for _, child := range children {
		switch box := child.(type) {
		case *mp4.SbgpBox:
			if box.GroupingType == "seam" || box.GroupingType == "seig" {
				bytesRemoved += child.Size()
				continue
			}
		case *mp4.SgpdBox:
			if box.GroupingType == "seam" || box.GroupingType == "seig" {
				bytesRemoved += child.Size()
				continue
			}
		}
		remainingChildren = append(remainingChildren, child)
	}
	return remainingChildren, bytesRemoved
}

// Get decryption info for tracks from init segment and remove encryption-related boxes
func TransformInit(init *mp4.InitSegment) (map[uint32]mp4.DecryptTrackInfo, error) {
	di, err := mp4.DecryptInit(init)
	tracks := make(map[uint32]mp4.DecryptTrackInfo, len(di.TrackInfos))
	for _, ti := range di.TrackInfos {
		tracks[ti.TrackID] = ti
	}
	if err != nil {
		return tracks, err
	}
	// remove encryption-related sbgp and sgpd
	for _, trak := range init.Moov.Traks {
		stbl := trak.Mdia.Minf.Stbl
		stbl.Children, _ = FilterSbgpSgpd(stbl.Children)
	}
	return tracks, nil
}

// remote
// Reset the loops on the script's end and close the connection
func Close(conn io.WriteCloser) error {
	defer conn.Close()
	_, err := conn.Write([]byte{0, 0, 0, 0, 0})
	return err
}

func SwitchKeys(conn io.Writer) error {
	_, err := conn.Write([]byte{0, 0, 0, 0})
	return err
}

// Send id or keyUri
func SendString(conn io.Writer, uri string) error {
	_, err := conn.Write([]byte{byte(len(uri))})
	if err != nil {
		return err
	}
	_, err = io.WriteString(conn, uri)
	return err
}

func cbcsFullSubsampleDecrypt(data []byte, conn *bufio.ReadWriter) error {
	// Drops 4 last bits -> multiple of 16
	// It wouldn't hurt to send the remaining bytes also because the decryption
	// function would just return them as-is, but we're truncating the data here
	// for clarity and interoperability
	truncatedLen := len(data) & ^0xf
	// send the whole chunk at once
	err := binary.Write(conn, binary.LittleEndian, uint32(truncatedLen))
	if err != nil {
		return err
	}
	_, err = conn.Write(data[:truncatedLen])
	if err != nil {
		return err
	}
	err = conn.Flush()
	if err != nil {
		return err
	}
	_, err = io.ReadFull(conn, data[:truncatedLen])
	return err
}

func cbcsStripeDecrypt(data []byte, conn *bufio.ReadWriter, decryptBlockLen, skipBlockLen int) error {
	size := len(data)

	// block too small, ignore
	if size < decryptBlockLen {
		return nil
	}

	// number of encrypted blocks in this sample
	count := ((size - decryptBlockLen) / (decryptBlockLen + skipBlockLen)) + 1
	totalLen := count * decryptBlockLen

	err := binary.Write(conn, binary.LittleEndian, uint32(totalLen))
	if err != nil {
		return err
	}

	pos := 0
	for {
		if size-pos < decryptBlockLen { // Leave the rest
			break
		}
		_, err = conn.Write(data[pos : pos+decryptBlockLen])
		if err != nil {
			return err
		}
		pos += decryptBlockLen
		if size-pos < skipBlockLen {
			break
		}
		pos += skipBlockLen
	}
	err = conn.Flush()
	if err != nil {
		return err
	}

	pos = 0
	for {
		if size-pos < decryptBlockLen {
			break
		}
		_, err = io.ReadFull(conn, data[pos:pos+decryptBlockLen])
		if err != nil {
			return err
		}
		pos += decryptBlockLen
		if size-pos < skipBlockLen {
			break
		}
		pos += skipBlockLen
	}
	return nil
}

// Decryption function dispatcher
func cbcsDecryptRaw(data []byte, conn *bufio.ReadWriter, decryptBlockLen, skipBlockLen int) error {
	if skipBlockLen == 0 {
		// Full encryption of subsamples
		// e.g. Apple Music ALAC
		return cbcsFullSubsampleDecrypt(data, conn)
	} else {
		// Pattern (stripe) encryption of subsamples
		// e.g. most AVC and HEVC applications
		return cbcsStripeDecrypt(data, conn, decryptBlockLen, skipBlockLen)
	}
}

// Decrypt a cbcs-encrypted sample in-place
func cbcsDecryptSample(sample []byte, conn *bufio.ReadWriter,
	subSamplePatterns []mp4.SubSamplePattern, tenc *mp4.TencBox) error {

	decryptBlockLen := int(tenc.DefaultCryptByteBlock) * 16
	skipBlockLen := int(tenc.DefaultSkipByteBlock) * 16
	var pos uint32 = 0

	// Full sample encryption
	if len(subSamplePatterns) == 0 {
		return cbcsDecryptRaw(sample, conn, decryptBlockLen, skipBlockLen)
	}

	// Has subsamples
	for j := 0; j < len(subSamplePatterns); j++ {
		ss := subSamplePatterns[j]
		pos += uint32(ss.BytesOfClearData)

		// Nothing to decrypt!
		if ss.BytesOfProtectedData <= 0 {
			continue
		}

		err := cbcsDecryptRaw(sample[pos:pos+ss.BytesOfProtectedData],
			conn, decryptBlockLen, skipBlockLen)
		if err != nil {
			return err
		}
		pos += ss.BytesOfProtectedData
	}

	return nil
}

// Decrypt an array of cbcs-encrypted samples in-place
func cbcsDecryptSamples(samples []mp4.FullSample, conn *bufio.ReadWriter,
	tenc *mp4.TencBox, senc *mp4.SencBox) error {

	for i := range samples {
		var subSamplePatterns []mp4.SubSamplePattern
		if len(senc.SubSamples) != 0 {
			subSamplePatterns = senc.SubSamples[i]
		}
		err := cbcsDecryptSample(samples[i].Data, conn, subSamplePatterns, tenc)
		if err != nil {
			return err
		}
	}
	return nil
}

func DecryptFragment(frag *mp4.Fragment, tracks map[uint32]mp4.DecryptTrackInfo, conn *bufio.ReadWriter) error {
	moof := frag.Moof
	var bytesRemoved uint64 = 0
	var sxxxBytesRemoved uint64

	for _, traf := range moof.Trafs {
		ti, ok := tracks[traf.Tfhd.TrackID]
		if !ok {
			return fmt.Errorf("could not find decryption info for track %d", traf.Tfhd.TrackID)
		}
		if ti.Sinf == nil {
			// unencrypted track
			continue
		}

		schemeType := ti.Sinf.Schm.SchemeType
		if schemeType != "cbcs" {
			return fmt.Errorf("scheme type %s not supported", schemeType)
		}
		hasSenc, isParsed := traf.ContainsSencBox()
		if !hasSenc {
			return fmt.Errorf("no senc box in traf")
		}

		var senc *mp4.SencBox
		if traf.Senc != nil {
			senc = traf.Senc
		} else {
			senc = traf.UUIDSenc.Senc
		}

		if !isParsed {
			// simply ignore sbgp and sgpd
			// "Sample To Group Box ('sbgp') and Sample Group Description Box ('sgpd')
			// of type 'seig' are used to indicate the KID applied to each sample, and changes
			// to KIDs over time (i.e. 'key rotation')"
			// (ref: https://dashif.org/docs/DASH-IF-IOP-v3.2.pdf)
			err := senc.ParseReadBox(ti.Sinf.Schi.Tenc.DefaultPerSampleIVSize, traf.Saiz)
			if err != nil {
				return err
			}
		}

		samples, err := frag.GetFullSamples(ti.Trex)
		if err != nil {
			return err
		}

		err = cbcsDecryptSamples(samples, conn, ti.Sinf.Schi.Tenc, senc)
		if err != nil {
			return err
		}

		bytesRemoved += traf.RemoveEncryptionBoxes()
		// remove sbgp and sgpd
		traf.Children, sxxxBytesRemoved = FilterSbgpSgpd(traf.Children)
		bytesRemoved += sxxxBytesRemoved
	}
	_, psshBytesRemoved := moof.RemovePsshs()
	bytesRemoved += psshBytesRemoved
	for _, traf := range moof.Trafs {
		for _, trun := range traf.Truns {
			trun.DataOffset -= int32(bytesRemoved)
		}
	}

	return nil
}
