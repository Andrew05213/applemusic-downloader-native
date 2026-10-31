package runv3

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"

	"google.golang.org/protobuf/proto"

	//"log/slog"
	"github.com/Andrew05213/applemusic_downloader/internal/outbound"
	cdm "github.com/Andrew05213/applemusic_downloader/utils/runv3/cdm"
	key "github.com/Andrew05213/applemusic_downloader/utils/runv3/key"
	"os"

	"bytes"
	"errors"
	"io"

	"github.com/Eyevinn/mp4ff/mp4"

	//"io/ioutil"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/transfer"
	"github.com/grafov/m3u8"
	"github.com/schollz/progressbar/v3"
)

type PlaybackLicense struct {
	ErrorCode  int    `json:"errorCode"`
	License    string `json:"license"`
	RenewAfter int    `json:"renew-after"`
	Status     int    `json:"status"`
}

const (
	defaultHTTPTimeout               = 30 * time.Second
	defaultHTTPResponseHeaderTimeout = 15 * time.Second
	maxWebPlaybackResponseBytes      = 4 << 20
	maxPlaylistResponseBytes         = 4 << 20
	maxLicenseResponseBytes          = 2 << 20
	maxMediaResponseBytes            = 512 << 20
	maxSegmentResponseBytes          = 64 << 20
)

var (
	// ErrUnexpectedHTTPStatus marks a response whose status code is not 2xx.
	ErrUnexpectedHTTPStatus = errors.New("unexpected HTTP status")
	// ErrResponseTooLarge marks a response that exceeds the caller's explicit size limit.
	ErrResponseTooLarge = errors.New("HTTP response exceeds size limit")
)

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       defaultHTTPTimeout,
		CheckRedirect: rejectCrossOriginRedirect,
	}
}

// rejectCrossOriginRedirect retains the redirect response before a request
// carrying Apple credentials can be replayed to another origin.
func rejectCrossOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 || req == nil || req.URL == nil || via[0] == nil || via[0].URL == nil {
		return http.ErrUseLastResponse
	}
	if !strings.EqualFold(via[0].URL.Scheme, req.URL.Scheme) || !strings.EqualFold(via[0].URL.Host, req.URL.Host) {
		return http.ErrUseLastResponse
	}
	return nil
}

func newLicenseHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// License requests carry bearer credentials. Do not inherit proxy settings
	// from the process environment, because the legacy request client did not
	// route these requests through HTTP_PROXY/HTTPS_PROXY either.
	outbound.ApplyToTransport(transport)
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.ResponseHeaderTimeout = defaultHTTPResponseHeaderTimeout
	transport.TLSHandshakeTimeout = defaultHTTPResponseHeaderTimeout
	return &http.Client{
		Timeout:   defaultHTTPTimeout,
		Transport: transport,
		// Do not follow a redirect with bearer credentials to a new URL.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func remoteErrorForLog(err error) string {
	switch {
	case err == nil:
		return "remote request failed"
	case errors.Is(err, context.Canceled):
		return "request cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "request timed out"
	case errors.Is(err, ErrUnexpectedHTTPStatus):
		return "unexpected HTTP status"
	case errors.Is(err, ErrResponseTooLarge):
		return "response exceeds size limit"
	default:
		return "remote request failed"
	}
}

// fetchLimited makes one context-bound HTTP request and reads no more than maxBytes.
// It intentionally accepts an injected client so callers can use a shorter timeout in tests.
func fetchLimited(ctx context.Context, client *http.Client, method, url string, body io.Reader, headers http.Header, maxBytes int64) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("context must not be nil")
	}
	if maxBytes <= 0 {
		return nil, errors.New("response size limit must be positive")
	}
	if client == nil {
		client = newHTTPClient()
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("create HTTP request: %w", err)
	}
	if headers != nil {
		req.Header = headers.Clone()
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: %s", ErrUnexpectedHTTPStatus, resp.Status)
	}
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("%w: declared %d bytes exceeds %d bytes", ErrResponseTooLarge, resp.ContentLength, maxBytes)
	}

	var source io.Reader = resp.Body
	if maxBytes == maxMediaResponseBytes || maxBytes == maxSegmentResponseBytes {
		var finish func()
		source, finish = transfer.Track(ctx, source)
		defer finish()
	}
	data, err := io.ReadAll(io.LimitReader(source, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read HTTP response: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: response exceeds %d bytes", ErrResponseTooLarge, maxBytes)
	}
	return data, nil
}

func commandOutputContext(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("context must not be nil")
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

//	func log() {
//		f, err := os.OpenFile("log.txt", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
//		if err != nil {
//			slog.Error("error opening file: %s", err)
//		}
//		defer func(f *os.File) {
//			err := f.Close()
//			if err != nil {
//				slog.Error("error closing file: %s", err)
//			}
//		}(f)
//		opts := slog.HandlerOptions{
//			AddSource: true,
//			Level:     slog.LevelDebug,
//		}
//		logger := slog.New(slog.NewJSONHandler(os.Stdout, &opts))
//		slog.SetDefault(logger)
//	}
func getPSSH(contentId string, kidBase64 string) (string, error) {
	kidBytes, err := base64.StdEncoding.DecodeString(kidBase64)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64 KID: %v", err)
	}
	contentIdEncoded := base64.StdEncoding.EncodeToString([]byte(contentId))
	algo := cdm.WidevineCencHeader_AESCTR
	widevineCencHeader := &cdm.WidevineCencHeader{
		KeyId:     [][]byte{kidBytes},
		Algorithm: &algo,
		Provider:  new(string),
		ContentId: []byte(contentIdEncoded),
		Policy:    new(string),
	}
	widevineCenc, err := proto.Marshal(widevineCencHeader)
	if err != nil {
		return "", fmt.Errorf("failed to marshal WidevineCencHeader: %v", err)
	}
	//最前面添加32字节
	widevineCenc = append([]byte("0123456789abcdef0123456789abcdef"), widevineCenc...)
	pssh := base64.StdEncoding.EncodeToString(widevineCenc)
	return pssh, nil
}

type licenseContextKey string

const (
	psshContextKey   licenseContextKey = "pssh"
	adamIDContextKey licenseContextKey = "adam-id"
	webPlaybackURL                     = "https://play.music.apple.com/WebObjects/MZPlay.woa/wa/webPlayback"
)

func licenseContextValue(ctx context.Context, key licenseContextKey, legacyKey string) (string, bool) {
	if ctx == nil {
		return "", false
	}
	if value, ok := ctx.Value(key).(string); ok && value != "" {
		return value, true
	}
	value, ok := ctx.Value(legacyKey).(string)
	return value, ok && value != ""
}

func BeforeRequest(cl *http.Client, preCtx context.Context, method string, href string, headers http.Header, challenge []byte) (resp *http.Response, err error) {
	if cl == nil {
		return nil, errors.New("license client is nil")
	}
	if preCtx == nil {
		return nil, errors.New("license context is nil")
	}
	if len(challenge) == 0 {
		return nil, errors.New("license request challenge is missing")
	}
	pssh, ok := licenseContextValue(preCtx, psshContextKey, "pssh")
	if !ok {
		return nil, errors.New("license request PSSH is missing")
	}
	adamID, ok := licenseContextValue(preCtx, adamIDContextKey, "adamId")
	if !ok {
		return nil, errors.New("license request adam ID is missing")
	}

	body, err := json.Marshal(map[string]interface{}{
		"challenge":      base64.StdEncoding.EncodeToString(challenge),
		"key-system":     "com.widevine.alpha",
		"uri":            "data:;base64," + pssh,
		"adamId":         adamID,
		"isLibrary":      false,
		"user-initiated": true,
	})
	if err != nil {
		return nil, fmt.Errorf("encode license request: %w", err)
	}

	req, err := http.NewRequestWithContext(preCtx, method, href, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create license request: %w", err)
	}
	if headers != nil {
		req.Header = headers.Clone()
	}
	req.Header.Set("Content-Type", "application/json")
	return cl.Do(req)
}

func AfterRequest(response *http.Response) ([]byte, error) {
	if response == nil {
		return nil, errors.New("license response is nil")
	}
	if response.Body == nil {
		return nil, errors.New("license response body is nil")
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: %s", ErrUnexpectedHTTPStatus, response.Status)
	}
	if response.ContentLength > maxLicenseResponseBytes {
		return nil, fmt.Errorf("%w: declared %d bytes exceeds %d bytes", ErrResponseTooLarge, response.ContentLength, maxLicenseResponseBytes)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, maxLicenseResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read license response: %w", err)
	}
	if len(data) > maxLicenseResponseBytes {
		return nil, fmt.Errorf("%w: license response exceeds %d bytes", ErrResponseTooLarge, maxLicenseResponseBytes)
	}

	var responseData PlaybackLicense
	if err := json.Unmarshal(data, &responseData); err != nil {
		return nil, fmt.Errorf("parse license response: %w", err)
	}
	if responseData.ErrorCode != 0 || responseData.Status != 0 {
		return nil, fmt.Errorf("license error: code=%d status=%d", responseData.ErrorCode, responseData.Status)
	}
	license, err := base64.StdEncoding.DecodeString(responseData.License)
	if err != nil {
		return nil, fmt.Errorf("decode license: %w", err)
	}
	return license, nil
}

// GetWebplayback is retained for callers that do not yet pass a context.
func GetWebplayback(adamId string, authtoken string, mutoken string, mvmode bool) (string, string, error) {
	return GetWebplaybackContext(context.Background(), adamId, authtoken, mutoken, mvmode)
}

// GetWebplaybackContext requests Apple Music's playback metadata with cancellation,
// a request timeout, and a bounded response body.
func GetWebplaybackContext(ctx context.Context, adamId string, authtoken string, mutoken string, mvmode bool) (string, string, error) {
	return getWebplaybackContext(ctx, newHTTPClient(), webPlaybackURL, adamId, authtoken, mutoken, mvmode)
}

func getWebplaybackContext(ctx context.Context, client *http.Client, endpoint string, adamId string, authtoken string, mutoken string, mvmode bool) (string, string, error) {
	jsonData, err := json.Marshal(map[string]string{"salableAdamId": adamId})
	if err != nil {
		return "", "", fmt.Errorf("encode playback request: %w", err)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Origin", "https://music.apple.com")
	headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	headers.Set("Referer", "https://music.apple.com/")
	headers.Set("Authorization", fmt.Sprintf("Bearer %s", authtoken))
	headers.Set("x-apple-music-user-token", mutoken)

	data, err := fetchLimited(ctx, client, http.MethodPost, endpoint, bytes.NewReader(jsonData), headers, maxWebPlaybackResponseBytes)
	if err != nil {
		return "", "", err
	}
	var obj Songlist
	if err := json.Unmarshal(data, &obj); err != nil {
		return "", "", fmt.Errorf("decode playback response: %w", err)
	}
	if len(obj.List) > 0 {
		if mvmode {
			return obj.List[0].HlsPlaylistUrl, "", nil
		}
		fmt.Println("Available flavors:")
		for i := range obj.List[0].Assets {
			fmt.Printf("  flavor: %s\n", obj.List[0].Assets[i].Flavor)
		}
		flavorPriority := []string{"28:ctrp256", "32:ctrp64", "30:cbcp256", "34:cbcp64"}
		for _, target := range flavorPriority {
			for i := range obj.List[0].Assets {
				if obj.List[0].Assets[i].Flavor == target {
					fmt.Printf("Selected flavor: %s\n", target)
					kidBase64, fileurl, err := extractKidBase64Context(ctx, obj.List[0].Assets[i].URL, false)
					if err != nil {
						return "", "", err
					}
					return fileurl, kidBase64, nil
				}
			}
		}
		if len(obj.List[0].Assets) > 0 {
			fmt.Printf("Fallback to first flavor: %s\n", obj.List[0].Assets[0].Flavor)
			kidBase64, fileurl, err := extractKidBase64Context(ctx, obj.List[0].Assets[0].URL, false)
			if err != nil {
				return "", "", err
			}
			return fileurl, kidBase64, nil
		}
	}
	return "", "", errors.New("Unavailable")
}

type Songlist struct {
	List []struct {
		Hlsurl         string `json:"hls-key-cert-url"`
		HlsPlaylistUrl string `json:"hls-playlist-url"`
		Assets         []struct {
			Flavor string `json:"flavor"`
			URL    string `json:"URL"`
		} `json:"assets"`
	} `json:"songList"`
	Status int `json:"status"`
}

func extractKidBase64(b string, mvmode bool) (string, string, error) {
	return extractKidBase64Context(context.Background(), b, mvmode)
}

func extractKidBase64Context(ctx context.Context, b string, mvmode bool) (string, string, error) {
	body, err := fetchLimited(ctx, newHTTPClient(), http.MethodGet, b, nil, nil, maxPlaylistResponseBytes)
	if err != nil {
		return "", "", err
	}
	masterString := string(body)
	from, listType, err := m3u8.DecodeFrom(strings.NewReader(masterString), true)
	if err != nil {
		return "", "", err
	}
	var kidbase64 string
	var urlBuilder strings.Builder
	if listType == m3u8.MEDIA {
		mediaPlaylist := from.(*m3u8.MediaPlaylist)
		if mediaPlaylist.Key != nil {
			uri := mediaPlaylist.Key.URI
			fmt.Println("Key URI received")
			split := strings.Split(uri, ",")
			if len(split) >= 2 {
				kidbase64 = split[1]
			} else {
				uuidPat := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
				uuid := uuidPat.FindString(uri)
				if uuid != "" {
					hexStr := strings.ReplaceAll(uuid, "-", "")
					kidBytes, err := hex.DecodeString(hexStr)
					if err == nil {
						kidbase64 = base64.StdEncoding.EncodeToString(kidBytes)
						fmt.Printf("Extracted KID from UUID: %s -> %s\n", uuid, kidbase64)
					}
				}
			}
			lastSlashIndex := strings.LastIndex(b, "/")
			// 截取最后一个斜杠之前的部分
			urlBuilder.WriteString(b[:lastSlashIndex])
			urlBuilder.WriteString("/")
			urlBuilder.WriteString(mediaPlaylist.Map.URI)
			//fileurl = b[:lastSlashIndex] + "/" + mediaPlaylist.Map.URI
			//fmt.Println("Extracted URI:", mediaPlaylist.Map.URI)
			if mvmode {
				for _, segment := range mediaPlaylist.Segments {
					if segment != nil {
						//fmt.Println("Extracted URI:", segment.URI)
						urlBuilder.WriteString(";")
						urlBuilder.WriteString(b[:lastSlashIndex])
						urlBuilder.WriteString("/")
						urlBuilder.WriteString(segment.URI)
						//fileurl = fileurl + ";" + b[:lastSlashIndex] + "/" + segment.URI
					}
				}
			}
		} else {
			fmt.Println("No key information found")
		}
	} else {
		fmt.Println("Not a media playlist")
	}
	return kidbase64, urlBuilder.String(), nil
}
func extsong(b string) bytes.Buffer {
	buffer, err := extsongContext(context.Background(), b)
	if err != nil {
		fmt.Printf("下载文件失败: %s\n", remoteErrorForLog(err))
		return bytes.Buffer{}
	}
	return buffer
}

func extsongContext(ctx context.Context, b string) (bytes.Buffer, error) {
	var buffer bytes.Buffer
	data, err := fetchLimited(ctx, newHTTPClient(), http.MethodGet, b, nil, nil, maxMediaResponseBytes)
	if err != nil {
		return buffer, err
	}
	if _, err := buffer.Write(data); err != nil {
		return buffer, fmt.Errorf("buffer downloaded media: %w", err)
	}
	return buffer, nil
}

// Run is retained for callers that do not yet pass a context.
func Run(adamId string, trackpath string, authtoken string, mutoken string, mvmode bool) (string, error) {
	return RunContext(context.Background(), adamId, trackpath, authtoken, mutoken, mvmode)
}

// RunContext downloads one track or obtains one MV key while binding every HTTP
// request and the license client to ctx.
func RunContext(ctx context.Context, adamId string, trackpath string, authtoken string, mutoken string, mvmode bool) (string, error) {
	if ctx == nil {
		return "", errors.New("context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	var keystr string //for mv key
	var fileurl string
	var kidBase64 string
	var err error
	if mvmode {
		kidBase64, fileurl, err = extractKidBase64Context(ctx, trackpath, true)
		if err != nil {
			return "", err
		}
	} else {
		fileurl, kidBase64, err = GetWebplaybackContext(ctx, adamId, authtoken, mutoken, false)
		if err != nil {
			return "", err
		}
	}
	licenseCtx := context.WithValue(ctx, psshContextKey, kidBase64)
	licenseCtx = context.WithValue(licenseCtx, adamIDContextKey, adamId)
	pssh, err := getPSSH("", kidBase64)
	if err != nil {
		return "", err
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+authtoken)
	headers.Set("x-apple-music-user-token", mutoken)
	client := newLicenseHTTPClient()
	defer client.CloseIdleConnections()

	licenseKey := key.Key{
		ReqCli:        client,
		Headers:       headers,
		BeforeRequest: BeforeRequest,
		AfterRequest:  AfterRequest,
	}
	licenseKey.CdmInit()
	var keybt []byte
	if strings.Contains(adamId, "ra.") {
		keystr, keybt, err = licenseKey.GetKey(licenseCtx, "https://play.itunes.apple.com/WebObjects/MZPlay.woa/web/radio/versions/1/license", pssh, nil)
		if err != nil {
			return "", err
		}
	} else {
		keystr, keybt, err = licenseKey.GetKey(licenseCtx, "https://play.itunes.apple.com/WebObjects/MZPlay.woa/wa/acquireWebPlaybackLicense", pssh, nil)
		if err != nil {
			return "", err
		}
	}
	if mvmode {
		keyAndUrls := "1:" + keystr + ";" + fileurl
		return keyAndUrls, nil
	}
	body, err := extsongContext(ctx, fileurl)
	if err != nil {
		return "", err
	}
	fmt.Print("Downloaded\n")
	var buffer bytes.Buffer

	transfer.Phase(ctx, "decrypting")
	err = DecryptMP4(&body, keybt, &buffer)
	if err != nil {
		fmt.Print("Decryption failed\n")
		return "", err
	} else {
		fmt.Print("Decrypted\n")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// create output file
	ofh, err := os.Create(trackpath)
	if err != nil {
		fmt.Printf("创建文件失败: %v\n", err)
		return "", err
	}
	defer ofh.Close()

	_, err = ofh.Write(buffer.Bytes())
	if err != nil {
		fmt.Printf("写入文件失败: %v\n", err)
		return "", err
	}
	return "", nil
}

// Segment 结构体用于在 Channel 中传递分段数据
type Segment struct {
	Index int
	Data  []byte
}

type segmentFailure struct {
	once   sync.Once
	err    error
	cancel context.CancelFunc
}

func (failure *segmentFailure) report(err error) {
	if err == nil {
		return
	}
	failure.once.Do(func() {
		failure.err = err
		failure.cancel()
	})
}

func downloadSegmentContext(ctx context.Context, url string, index int, wg *sync.WaitGroup, segmentsChan chan<- Segment, client *http.Client, limiter chan struct{}, failure *segmentFailure) {
	// 函数退出时，从 limiter 中接收一个值，释放一个并发槽位
	defer func() {
		<-limiter
		wg.Done()
	}()

	data, err := fetchLimited(ctx, client, http.MethodGet, url, nil, nil, maxSegmentResponseBytes)
	if err != nil {
		fmt.Printf("错误(分段 %d): 下载失败: %s\n", index, remoteErrorForLog(err))
		failure.report(err)
		return
	}

	// 将下载好的分段（包含序号和数据）发送到 Channel
	select {
	case segmentsChan <- Segment{Index: index, Data: data}:
	case <-ctx.Done():
		failure.report(ctx.Err())
	}
}

// fileWriter 从 Channel 接收分段并按顺序写入文件
func fileWriter(wg *sync.WaitGroup, segmentsChan <-chan Segment, outputFile io.Writer, totalSegments int) error {
	defer wg.Done()

	// 缓冲区，用于存放乱序到达的分段
	// key 是分段序号，value 是分段数据
	segmentBuffer := make(map[int][]byte)
	nextIndex := 0 // 期望写入的下一个分段的序号

	for segment := range segmentsChan {
		if segment.Index < nextIndex || segment.Index >= totalSegments {
			return errors.New("invalid MV segment sequence")
		}
		if _, exists := segmentBuffer[segment.Index]; exists {
			return errors.New("duplicate MV segment")
		}
		// 检查收到的分段是否是当前期望的
		if segment.Index == nextIndex {
			//fmt.Printf("写入分段 %d\n", segment.Index)
			n, err := outputFile.Write(segment.Data)
			if err != nil {
				return err
			}
			if n != len(segment.Data) {
				return io.ErrShortWrite
			}
			nextIndex++

			// 检查缓冲区中是否有下一个连续的分段
			for {
				data, ok := segmentBuffer[nextIndex]
				if !ok {
					break // 缓冲区里没有下一个，跳出循环，等待下一个分段到达
				}

				//fmt.Printf("从缓冲区写入分段 %d\n", nextIndex)
				n, err := outputFile.Write(data)
				if err != nil {
					return err
				}
				if n != len(data) {
					return io.ErrShortWrite
				}
				// 从缓冲区删除已写入的分段，释放内存
				delete(segmentBuffer, nextIndex)
				nextIndex++
			}
		} else {
			// 如果不是期望的分段，先存入缓冲区
			//fmt.Printf("缓冲分段 %d (等待 %d)\n", segment.Index, nextIndex)
			segmentBuffer[segment.Index] = segment.Data
		}
	}

	// 确保所有分段都已写入
	if nextIndex != totalSegments {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// ExtMvData is retained for callers that do not yet pass a context.
func ExtMvData(keyAndUrls string, savePath string) error {
	return ExtMvDataContext(context.Background(), keyAndUrls, savePath)
}

// ExtMvDataContext downloads and decrypts MV segments while honoring ctx for
// every segment request and the mp4decrypt subprocess.
func ExtMvDataContext(ctx context.Context, keyAndUrls string, savePath string) error {
	if ctx == nil {
		return errors.New("context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	segments := strings.Split(keyAndUrls, ";")
	if len(segments) < 2 || segments[0] == "" {
		return errors.New("MV data must contain a key and at least one segment URL")
	}
	key := segments[0]
	//fmt.Println(key)
	urls := segments[1:]
	tempFile, err := os.CreateTemp("", "enc_mv_data-*.mp4")
	if err != nil {
		fmt.Printf("创建文件失败：%v\n", err)
		return err
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	var downloadWg, writerWg sync.WaitGroup
	segmentsChan := make(chan Segment, len(urls))
	downloadCtx, cancelDownloads := context.WithCancel(ctx)
	defer cancelDownloads()
	failure := &segmentFailure{cancel: cancelDownloads}
	// --- 新增代码: 定义最大并发数 ---
	const maxConcurrency = 10
	// --- 新增代码: 创建带缓冲的 Channel 作为信号量 ---
	limiter := make(chan struct{}, maxConcurrency)
	client := newHTTPClient()

	// 初始化进度条
	bar := progressbar.DefaultBytes(-1, "Downloading...")
	barWriter := io.MultiWriter(tempFile, bar)

	// 启动写入 Goroutine
	writerWg.Add(1)
	go func() {
		// Keep failure reporting before Done so the parent reads it only after
		// all writers have stopped, including when cancellation drains senders.
		defer writerWg.Done()
		var inner sync.WaitGroup
		inner.Add(1)
		failure.report(fileWriter(&inner, segmentsChan, barWriter, len(urls)))
	}()

	// 启动下载 Goroutines
launchSegments:
	for i, url := range urls {
		// 在启动 Goroutine 前，向 limiter 发送一个值来“获取”一个槽位
		// 如果 limiter 已满 (达到10个)，这里会阻塞，直到有其他任务完成并释放槽位
		//fmt.Printf("请求启动任务 %d...\n", i)
		select {
		case <-downloadCtx.Done():
			break launchSegments
		case limiter <- struct{}{}:
		}
		//fmt.Printf("...任务 %d 已启动\n", i)

		downloadWg.Add(1)
		// 将 limiter 传递给下载函数
		go downloadSegmentContext(downloadCtx, url, i, &downloadWg, segmentsChan, client, limiter, failure)
	}

	// 等待所有下载任务完成
	downloadWg.Wait()
	// 下载完成后，关闭 Channel。写入 Goroutine 会在处理完 Channel 中所有数据后退出。
	close(segmentsChan)

	// 等待写入 Goroutine 完成所有写入和缓冲处理
	writerWg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if failure.err != nil {
		return fmt.Errorf("download MV segment: %w", failure.err)
	}

	// 显式关闭文件（defer会再次调用，但重复关闭是安全的）
	if err := tempFile.Close(); err != nil {
		fmt.Printf("关闭临时文件失败: %v\n", err)
		return err
	}
	fmt.Println("\nDownloaded.")

	outlog, err := commandOutputContext(ctx, filepath.Dir(savePath), "mp4decrypt", "--key", key, tempFile.Name(), filepath.Base(savePath))
	if err != nil {
		_ = outlog
		fmt.Println("Decrypt failed")
		return err
	} else {
		fmt.Println("Decrypted.")
	}
	return nil
}

// DecryptMP4 decrypts a fragmented MP4 file with keys from widevice license. Supports CENC and CBCS schemes.
func DecryptMP4(r io.Reader, key []byte, w io.Writer) error {
	// Initialization
	inMp4, err := mp4.DecodeFile(r)
	if err != nil {
		return fmt.Errorf("failed to decode file: %w", err)
	}
	if !inMp4.IsFragmented() {
		return errors.New("file is not fragmented")
	}
	// Handle init segment
	if inMp4.Init == nil {
		return errors.New("no init part of file")
	}
	decryptInfo, err := mp4.DecryptInit(inMp4.Init)
	if err != nil {
		return fmt.Errorf("failed to decrypt init: %w", err)
	}
	if err = inMp4.Init.Encode(w); err != nil {
		return fmt.Errorf("failed to write init: %w", err)
	}
	// Decode segments
	for _, seg := range inMp4.Segments {
		if err = mp4.DecryptSegment(seg, decryptInfo, key); err != nil {
			if err.Error() == "no senc box in traf" {
				// No SENC box, skip decryption for this segment as samples can have
				// unencrypted segments followed by encrypted segments. See:
				// https://github.com/iyear/gowidevine/pull/26#issuecomment-2385960551
				err = nil
			} else {
				return fmt.Errorf("failed to decrypt segment: %w", err)
			}
		}
		if err = seg.Encode(w); err != nil {
			return fmt.Errorf("failed to encode segment: %w", err)
		}
	}
	return nil
}
