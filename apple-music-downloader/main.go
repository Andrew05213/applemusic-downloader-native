package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	runtimeconfig "github.com/Andrew05213/applemusic_downloader/internal/config"
	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
	"github.com/Andrew05213/applemusic_downloader/internal/outbound"
	"github.com/Andrew05213/applemusic_downloader/internal/transfer"
	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
	"github.com/Andrew05213/applemusic_downloader/utils/runv2"
	"github.com/Andrew05213/applemusic_downloader/utils/runv3"
	"github.com/Andrew05213/applemusic_downloader/utils/structs"
	"github.com/Andrew05213/applemusic_downloader/utils/task"

	"github.com/AlecAivazis/survey/v2"
	"github.com/fatih/color"
	"github.com/grafov/m3u8"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/pflag"
	"github.com/zhaarey/go-mp4tag"
)

var (
	forbiddenNames = regexp.MustCompile(`[/\\<>:"|?*]`)
	dl_atmos       bool
	dl_aac         bool
	dl_select      bool
	dl_song        bool
	artist_select  bool
	debug_mode     bool
	aac_type       *string
	Config         structs.ConfigSet
	counter        structs.Counter
	okDict         = make(map[string][]int)
	mediaUserToken string
	// resolvedConfigPath is process-local configuration provenance. The
	// isolated child receives it through AMDL_CONFIG so an explicit CLI YAML
	// file keeps the same lower-precedence settings after process isolation.
	resolvedConfigPath string
	// legacyRetrySourceIDs is populated only by the isolated __run-one child.
	// An empty map means the complete URL should run; a non-empty map limits
	// album, playlist, and station loops to the failed source IDs selected by
	// the durable job store.
	legacyRetrySourceIDs map[string]struct{}
)

const (
	legacyHTTPTimeout              = 30 * time.Second
	legacyDeviceTimeout            = 10 * time.Second
	maxManifestResponseBytes int64 = 4 * 1024 * 1024
	maxCoverResponseBytes    int64 = 32 * 1024 * 1024
	maxDeviceResponseBytes   int64 = 16 * 1024
	artistCatalogAPIBaseURL        = "https://amp-api.music.apple.com"
)

var (
	errNetworkResponseTooLarge = errors.New("network response exceeds size limit")
	legacyNetworkHTTPClient    = &http.Client{Timeout: legacyHTTPTimeout, CheckRedirect: stripLegacyRefererOnCrossOriginRedirect}
)

// stripLegacyRefererOnCrossOriginRedirect preserves CDN redirects while
// preventing Go's automatic Referer header from disclosing a signed manifest
// URL to a different origin.
func stripLegacyRefererOnCrossOriginRedirect(req *http.Request, via []*http.Request) error {
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

func networkContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func networkContextError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func getNetworkResponse(ctx context.Context, rawURL string, headers http.Header) (*http.Response, error) {
	ctx = networkContext(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	resp, err := legacyNetworkHTTPClient.Do(req)
	if err != nil {
		return nil, networkContextError(ctx, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, &ampapi.HTTPStatusError{StatusCode: resp.StatusCode}
	}
	return resp, nil
}

func responseTooLarge(contentLength, maximum int64) error {
	return fmt.Errorf("%w: %d bytes exceeds %d-byte limit", errNetworkResponseTooLarge, contentLength, maximum)
}

func readNetworkResponse(ctx context.Context, rawURL string, maximum int64, headers http.Header) ([]byte, error) {
	resp, err := getNetworkResponse(ctx, rawURL, headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.ContentLength > maximum {
		return nil, responseTooLarge(resp.ContentLength, maximum)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maximum+1))
	if err != nil {
		return nil, networkContextError(ctx, err)
	}
	if int64(len(body)) > maximum {
		return nil, responseTooLarge(int64(len(body)), maximum)
	}
	return body, nil
}

func copyNetworkResponse(ctx context.Context, rawURL string, maximum int64, headers http.Header, destination io.Writer) error {
	resp, err := getNetworkResponse(ctx, rawURL, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.ContentLength > maximum {
		return responseTooLarge(resp.ContentLength, maximum)
	}

	written, err := io.Copy(destination, io.LimitReader(resp.Body, maximum))
	if err != nil {
		return networkContextError(ctx, err)
	}
	var probe [1]byte
	read, err := io.ReadFull(resp.Body, probe[:])
	if read > 0 {
		return responseTooLarge(written+int64(read), maximum)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return networkContextError(ctx, err)
	}
	return nil
}

type motionArtworkCommandRunner func(context.Context, []string) error

// runMotionArtworkFFmpeg ensures ffmpeg's HLS subrequests use the same
// supervisor-delivered bridge as the Go clients. Downloading only the first
// playlist into a local file would leave its segment and key URLs able to
// bypass that bridge.
func runMotionArtworkFFmpeg(ctx context.Context, sourceURL, destinationPath string) error {
	bridgeURL, bridgeAvailable := outbound.BridgeURL()
	return runMotionArtworkFFmpegWithRunner(
		ctx,
		sourceURL,
		destinationPath,
		strings.TrimSpace(os.Getenv("AMDL_RUNTIME")) == "wsl-appliance",
		bridgeURL,
		bridgeAvailable,
		runMotionArtworkFFmpegCommand,
	)
}

func runMotionArtworkFFmpegWithRunner(
	ctx context.Context,
	sourceURL, destinationPath string,
	applianceRuntime bool,
	bridgeURL string,
	bridgeAvailable bool,
	runner motionArtworkCommandRunner,
) error {
	arguments, err := motionArtworkFFmpegArguments(sourceURL, destinationPath, applianceRuntime, bridgeURL, bridgeAvailable)
	if err != nil {
		return err
	}
	if runner == nil {
		return errors.New("animated artwork command runner is unavailable")
	}
	return runner(ctx, arguments)
}

func runMotionArtworkFFmpegCommand(ctx context.Context, arguments []string) error {
	return exec.CommandContext(ctx, "ffmpeg", arguments...).Run()
}

func motionArtworkFFmpegArguments(sourceURL, destinationPath string, applianceRuntime bool, bridgeURL string, bridgeAvailable bool) ([]string, error) {
	arguments := []string{"-loglevel", "quiet", "-y"}
	if applianceRuntime {
		if !bridgeAvailable {
			return nil, errors.New("appliance animated artwork requires a validated outbound bridge")
		}
		if err := validateApplianceMotionArtworkURL(sourceURL); err != nil {
			return nil, err
		}
		canonicalBridgeURL, err := canonicalMotionArtworkBridgeURL(bridgeURL)
		if err != nil {
			return nil, err
		}
		// HLS can recursively request playlists, keys, and media segments. Keep
		// the protocol set minimal and attach the validated bridge before -i so
		// every HTTPS request made by this ffmpeg input goes through CONNECT.
		arguments = append(arguments,
			"-protocol_whitelist", "https,http,tls,tcp,crypto,httpproxy",
			"-http_proxy", canonicalBridgeURL,
		)
	}
	arguments = append(arguments, "-i", sourceURL, "-c", "copy", destinationPath)
	return arguments, nil
}

func canonicalMotionArtworkBridgeURL(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Hostname() != "127.0.0.1" ||
		parsed.Port() == "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("appliance animated artwork bridge is invalid")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("appliance animated artwork bridge is invalid")
	}
	canonical := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if parsed.Host != net.JoinHostPort("127.0.0.1", parsed.Port()) || rawURL != canonical {
		return "", errors.New("appliance animated artwork bridge is invalid")
	}
	return canonical, nil
}

func validateApplianceMotionArtworkURL(rawURL string) error {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Hostname() == "" || parsed.Port() != "" && parsed.Port() != "443" ||
		!validApplianceMotionArtworkHostname(parsed.Hostname()) {
		return errors.New("appliance animated artwork URL is invalid")
	}
	return nil
}

func validApplianceMotionArtworkHostname(host string) bool {
	host = strings.ToLower(host)
	if len(host) > 253 || !strings.Contains(host, ".") || strings.HasSuffix(host, ".") || net.ParseIP(host) != nil {
		return false
	}
	for _, suffix := range []string{".local", ".localhost", ".localdomain", ".lan", ".home", ".internal", ".invalid", ".test", ".example"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func loadConfigWithOptions(path string, overrides runtimeconfig.Overrides) (runtimeconfig.Source, error) {
	loaded, source, err := runtimeconfig.Load(runtimeconfig.LoadOptions{
		ConfigPath: path,
		CLI:        overrides,
	})
	if err != nil {
		return runtimeconfig.Source{}, err
	}
	applyInheritedProcessCredentials(&loaded)
	Config = loaded
	mediaUserToken = loaded.MediaUserToken
	resolvedConfigPath = source.Path
	return source, nil
}

func LimitString(s string) string {
	if len([]rune(s)) > Config.LimitMax {
		return string([]rune(s)[:Config.LimitMax])
	}
	return s
}

func isInArray(arr []int, target int) bool {
	for _, num := range arr {
		if num == target {
			return true
		}
	}
	return false
}

func fileExists(path string) (bool, error) {
	f, err := os.Stat(path)
	if err == nil {
		return !f.IsDir(), nil
	} else if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func checkUrl(url string) (string, string) {
	pat := regexp.MustCompile(`^(?:https:\/\/(?:beta\.music|music|classical\.music)\.apple\.com\/(\w{2})(?:\/album|\/album\/.+))\/(?:id)?(\d[^\D]+)(?:$|\?)`)
	matches := pat.FindAllStringSubmatch(url, -1)

	if matches == nil {
		return "", ""
	} else {
		return matches[0][1], matches[0][2]
	}
}
func checkUrlMv(url string) (string, string) {
	pat := regexp.MustCompile(`^(?:https:\/\/(?:beta\.music|music)\.apple\.com\/(\w{2})(?:\/music-video|\/music-video\/.+))\/(?:id)?(\d[^\D]+)(?:$|\?)`)
	matches := pat.FindAllStringSubmatch(url, -1)

	if matches == nil {
		return "", ""
	} else {
		return matches[0][1], matches[0][2]
	}
}
func checkUrlSong(url string) (string, string) {
	pat := regexp.MustCompile(`^(?:https:\/\/(?:beta\.music|music|classical\.music)\.apple\.com\/(\w{2})(?:\/song|\/song\/.+))\/(?:id)?(\d[^\D]+)(?:$|\?)`)
	matches := pat.FindAllStringSubmatch(url, -1)

	if matches == nil {
		return "", ""
	} else {
		return matches[0][1], matches[0][2]
	}
}
func checkUrlPlaylist(url string) (string, string) {
	source, err := parseAppleMusicPlaylistURL(url)
	if err != nil {
		return "", ""
	}
	return source.Storefront, source.PlaylistID
}

func checkUrlStation(url string) (string, string) {
	pat := regexp.MustCompile(`^(?:https:\/\/(?:beta\.music|music)\.apple\.com\/(\w{2})(?:\/station|\/station\/.+))\/(?:id)?(ra\.[\w-]+)(?:$|\?)`)
	matches := pat.FindAllStringSubmatch(url, -1)

	if matches == nil {
		return "", ""
	} else {
		return matches[0][1], matches[0][2]
	}
}

func checkUrlArtist(url string) (string, string) {
	pat := regexp.MustCompile(`^(?:https:\/\/(?:beta\.music|music|classical\.music)\.apple\.com\/(\w{2})(?:\/artist|\/artist\/.+))\/(?:id)?(\d[^\D]+)(?:$|\?)`)
	matches := pat.FindAllStringSubmatch(url, -1)

	if matches == nil {
		return "", ""
	} else {
		return matches[0][1], matches[0][2]
	}
}
func getUrlSong(songUrl string, token string) (string, error) {
	storefront, songId := checkUrlSong(songUrl)
	manifest, err := ampapi.GetSongResp(storefront, songId, Config.Language, token)
	if err != nil {
		fmt.Println("\u26A0 Failed to get manifest:", err)
		counter.NotSong++
		return "", err
	}
	albumId := manifest.Data[0].Relationships.Albums.Data[0].ID
	songAlbumUrl := fmt.Sprintf("https://music.apple.com/%s/album/1/%s?i=%s", storefront, albumId, songId)
	return songAlbumUrl, nil
}

// doArtistCatalogRequest prevents bearer credentials from being replayed to
// another origin while leaving the caller's HTTP client unmodified.
func doArtistCatalogRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	clientCopy := *client
	priorCheckRedirect := client.CheckRedirect
	clientCopy.CheckRedirect = func(redirect *http.Request, via []*http.Request) error {
		if !artistRedirectStaysSameOrigin(redirect, via) {
			return http.ErrUseLastResponse
		}
		if priorCheckRedirect != nil {
			return priorCheckRedirect(redirect, via)
		}
		return nil
	}
	return clientCopy.Do(req)
}

func artistRedirectStaysSameOrigin(req *http.Request, via []*http.Request) bool {
	if len(via) == 0 || req == nil || req.URL == nil || via[0] == nil || via[0].URL == nil {
		return false
	}
	original := via[0].URL
	return strings.EqualFold(original.Scheme, req.URL.Scheme) &&
		strings.EqualFold(original.Hostname(), req.URL.Hostname()) &&
		original.Port() == req.URL.Port()
}

func getUrlArtistName(artistUrl string, token string) (string, string, error) {
	return getUrlArtistNameWithHTTPClient(artistUrl, token, artistCatalogAPIBaseURL, http.DefaultClient)
}

func getUrlArtistNameWithHTTPClient(artistUrl string, token string, apiBaseURL string, client *http.Client) (string, string, error) {
	storefront, artistId := checkUrlArtist(artistUrl)
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/v1/catalog/%s/artists/%s", strings.TrimRight(apiBaseURL, "/"), storefront, artistId), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Origin", "https://music.apple.com")
	query := url.Values{}
	query.Set("l", Config.Language)
	do, err := doArtistCatalogRequest(client, req)
	if err != nil {
		return "", "", err
	}
	defer do.Body.Close()
	if do.StatusCode != http.StatusOK {
		return "", "", errors.New(do.Status)
	}
	obj := new(structs.AutoGeneratedArtist)
	err = json.NewDecoder(do.Body).Decode(&obj)
	if err != nil {
		return "", "", err
	}
	return obj.Data[0].Attributes.Name, obj.Data[0].ID, nil
}

func checkArtist(artistUrl string, token string, relationship string) ([]string, error) {
	return checkArtistWithHTTPClient(artistUrl, token, relationship, artistCatalogAPIBaseURL, http.DefaultClient)
}

func checkArtistWithHTTPClient(artistUrl string, token string, relationship string, apiBaseURL string, client *http.Client) ([]string, error) {
	storefront, artistId := checkUrlArtist(artistUrl)
	Num := 0
	//id := 1
	var args []string
	var urls []string
	var options [][]string
	for {
		req, err := http.NewRequest("GET", fmt.Sprintf("%s/v1/catalog/%s/artists/%s/%s?limit=100&offset=%d&l=%s", strings.TrimRight(apiBaseURL, "/"), storefront, artistId, relationship, Num, Config.Language), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
		req.Header.Set("Origin", "https://music.apple.com")
		do, err := doArtistCatalogRequest(client, req)
		if err != nil {
			return nil, err
		}
		defer do.Body.Close()
		if do.StatusCode != http.StatusOK {
			return nil, errors.New(do.Status)
		}
		obj := new(structs.AutoGeneratedArtist)
		err = json.NewDecoder(do.Body).Decode(&obj)
		if err != nil {
			return nil, err
		}
		for _, album := range obj.Data {
			options = append(options, []string{album.Attributes.Name, album.Attributes.ReleaseDate, album.ID, album.Attributes.URL})
		}
		Num = Num + 100
		if len(obj.Next) == 0 {
			break
		}
	}
	sort.Slice(options, func(i, j int) bool {
		// 将日期字符串解析为 time.Time 类型进行比较
		dateI, _ := time.Parse("2006-01-02", options[i][1])
		dateJ, _ := time.Parse("2006-01-02", options[j][1])
		return dateI.Before(dateJ) // 返回 true 表示 i 在 j 前面
	})

	table := tablewriter.NewWriter(os.Stdout)
	if relationship == "albums" {
		table.SetHeader([]string{"", "Album Name", "Date", "Album ID"})
	} else if relationship == "music-videos" {
		table.SetHeader([]string{"", "MV Name", "Date", "MV ID"})
	}
	table.SetRowLine(false)
	table.SetHeaderColor(tablewriter.Colors{},
		tablewriter.Colors{tablewriter.FgRedColor, tablewriter.Bold},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgBlackColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgBlackColor})

	table.SetColumnColor(tablewriter.Colors{tablewriter.FgCyanColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgRedColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgBlackColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgBlackColor})
	for i, v := range options {
		urls = append(urls, v[3])
		options[i] = append([]string{fmt.Sprint(i + 1)}, v[:3]...)
		table.Append(options[i])
	}
	table.Render()
	if artist_select {
		fmt.Println("You have selected all options:")
		return urls, nil
	}
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Please select from the " + relationship + " options above (multiple options separated by commas, ranges supported, or type 'all' to select all)")
	cyanColor := color.New(color.FgCyan)
	cyanColor.Print("Enter your choice: ")
	input, _ := reader.ReadString('\n')

	input = strings.TrimSpace(input)
	if input == "all" {
		fmt.Println("You have selected all options:")
		return urls, nil
	}

	selectedOptions := [][]string{}
	parts := strings.Split(input, ",")
	for _, part := range parts {
		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			selectedOptions = append(selectedOptions, rangeParts)
		} else {
			selectedOptions = append(selectedOptions, []string{part})
		}
	}

	fmt.Println("You have selected the following options:")
	for _, opt := range selectedOptions {
		if len(opt) == 1 {
			num, err := strconv.Atoi(opt[0])
			if err != nil {
				fmt.Println("Invalid option:", opt[0])
				continue
			}
			if num > 0 && num <= len(options) {
				fmt.Println(options[num-1])
				args = append(args, urls[num-1])
			} else {
				fmt.Println("Option out of range:", opt[0])
			}
		} else if len(opt) == 2 {
			start, err1 := strconv.Atoi(opt[0])
			end, err2 := strconv.Atoi(opt[1])
			if err1 != nil || err2 != nil {
				fmt.Println("Invalid range:", opt)
				continue
			}
			if start < 1 || end > len(options) || start > end {
				fmt.Println("Range out of range:", opt)
				continue
			}
			for i := start; i <= end; i++ {
				fmt.Println(options[i-1])
				args = append(args, urls[i-1])
			}
		} else {
			fmt.Println("Invalid option:", opt)
		}
	}
	return args, nil
}

func writeCover(sanAlbumFolder, name string, rawURL string) (string, error) {
	return writeCoverContext(context.Background(), sanAlbumFolder, name, rawURL)
}

func writeCoverContext(ctx context.Context, sanAlbumFolder, name string, rawURL string) (string, error) {
	return writeCoverWithOutputRegistrationContext(ctx, sanAlbumFolder, name, rawURL, true)
}

// writeCoverWithoutJobOutputContext is used only for short-lived cover files
// embedded into a media artifact and explicitly removed by the caller. It
// must not add a non-persistent helper file to the durable task manifest.
func writeCoverWithoutJobOutputContext(ctx context.Context, sanAlbumFolder, name string, rawURL string) (string, error) {
	return writeCoverWithOutputRegistrationContext(ctx, sanAlbumFolder, name, rawURL, false)
}

func writeCoverWithOutputRegistrationContext(ctx context.Context, sanAlbumFolder, name string, rawURL string, registerOutput bool) (string, error) {
	covPath := filepath.Join(sanAlbumFolder, name+"."+Config.CoverFormat)
	if Config.CoverFormat == "original" {
		parts := strings.Split(rawURL, "/")
		if len(parts) < 2 {
			return "", errors.New("invalid original cover URL")
		}
		extSource := parts[len(parts)-2]
		dot := strings.LastIndex(extSource, ".")
		if dot < 0 || dot == len(extSource)-1 {
			return "", errors.New("original cover URL has no extension")
		}
		covPath = filepath.Join(sanAlbumFolder, name+"."+extSource[dot+1:])
	}

	requestURL := rawURL
	if Config.CoverFormat == "png" {
		re := regexp.MustCompile(`\{w\}x\{h\}`)
		parts := re.Split(requestURL, 2)
		if len(parts) != 2 {
			return "", errors.New("cover URL has no size placeholder")
		}
		requestURL = parts[0] + "{w}x{h}" + strings.Replace(parts[1], ".jpg", ".png", 1)
	}
	requestURL = strings.Replace(requestURL, "{w}x{h}", Config.CoverSize, 1)
	if Config.CoverFormat == "original" {
		requestURL = strings.Replace(requestURL, "is1-ssl.mzstatic.com/image/thumb", "a5.mzstatic.com/us/r1000/0", 1)
		lastSlash := strings.LastIndex(requestURL, "/")
		if lastSlash < 0 {
			return "", errors.New("invalid original cover URL")
		}
		requestURL = requestURL[:lastSlash]
	}

	var (
		output        mediaOutput
		temporary     *os.File
		temporaryPath string
		err           error
	)
	if registerOutput {
		output, err = prepareSidecarOutputContext(ctx, covPath, legacyTrackExistingPolicy)
		if err != nil {
			return "", err
		}
		if output.Skipped {
			// The existing sidecar remains unmanaged/user-owned. It can still be
			// used for embedding, but must never be emitted as this task's output.
			return covPath, nil
		}
		defer output.Cleanup()
		temporaryPath = output.WorkPath
		temporary, err = os.OpenFile(temporaryPath, os.O_WRONLY|os.O_TRUNC, 0)
	} else {
		// Embedding helpers are intentionally anonymous. A deterministic track
		// ID filename could replace and later delete a user-owned cover.
		extension := filepath.Ext(covPath)
		base := strings.TrimSuffix(filepath.Base(covPath), extension)
		temporary, err = os.CreateTemp(sanAlbumFolder, "."+base+".embed-*"+extension)
		if temporary != nil {
			temporaryPath = temporary.Name()
		}
	}
	if err != nil {
		return "", err
	}
	keepTemporary := false
	defer func() {
		if !registerOutput && !keepTemporary && temporaryPath != "" {
			_ = os.Remove(temporaryPath)
		}
	}()

	headers := make(http.Header)
	headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	if err := copyNetworkResponse(ctx, requestURL, maxCoverResponseBytes, headers, temporary); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if !registerOutput {
		if err := validateSidecarOutput(temporaryPath); err != nil {
			return "", err
		}
		keepTemporary = true
		return temporaryPath, nil
	}
	if err := output.Commit(); err != nil {
		return "", err
	}
	emitLegacyJobOutputPath(covPath)
	return covPath, nil
}

// replaceCoverFile first attempts the platform's atomic same-directory
// replacement. Windows does not allow that when the destination exists, so
// its fallback moves the old file to a same-directory backup and restores it
// if publishing the new cover fails. In particular, it never deletes the
// existing cover before a replacement has succeeded.
func replaceCoverFile(temporaryPath, destinationPath string, rename func(string, string) error) error {
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(temporaryPath, destinationPath); err == nil {
		return nil
	}

	if _, err := os.Lstat(destinationPath); err != nil {
		return err
	}

	backup, err := os.CreateTemp(filepath.Dir(destinationPath), "."+filepath.Base(destinationPath)+".bak-*")
	if err != nil {
		return fmt.Errorf("create cover backup: %w", err)
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return fmt.Errorf("close cover backup: %w", err)
	}
	if err := os.Remove(backupPath); err != nil {
		return fmt.Errorf("prepare cover backup: %w", err)
	}

	if err := rename(destinationPath, backupPath); err != nil {
		return fmt.Errorf("preserve existing cover before replacement: %w", err)
	}
	if err := rename(temporaryPath, destinationPath); err != nil {
		if restoreErr := rename(backupPath, destinationPath); restoreErr != nil {
			return fmt.Errorf("replace cover: %w; restore previous cover from %q: %v", err, backupPath, restoreErr)
		}
		return err
	}
	_ = os.Remove(backupPath)
	return nil
}

func writeLyrics(sanAlbumFolder, filename string, lrc string) error {
	return writeLyricsContext(context.Background(), sanAlbumFolder, filename, lrc)
}

func writeLyricsContext(ctx context.Context, sanAlbumFolder, filename string, lrc string) error {
	lyricspath := filepath.Join(sanAlbumFolder, filename)
	output, err := prepareSidecarOutputContext(ctx, lyricspath, legacyTrackExistingPolicy)
	if err != nil {
		return err
	}
	if output.Skipped {
		return nil
	}
	defer output.Cleanup()
	f, err := os.OpenFile(output.WorkPath, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err = f.WriteString(lrc); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := output.Commit(); err != nil {
		return err
	}
	emitLegacyJobOutputPath(lyricspath)
	return nil
}

func contains(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

// START: New functions for search functionality

// SearchResultItem is a unified struct to hold search results for display.
type SearchResultItem struct {
	Type   string
	Name   string
	Detail string
	URL    string
	ID     string
}

// QualityOption holds information about a downloadable quality.
type QualityOption struct {
	ID          string
	Description string
}

// setDlFlags configures the global download flags based on the user's quality selection.
func setDlFlags(quality string) {
	dl_atmos = false
	dl_aac = false

	switch quality {
	case "atmos":
		dl_atmos = true
		fmt.Println("Quality set to: Dolby Atmos")
	case "aac":
		dl_aac = true
		*aac_type = "aac"
		fmt.Println("Quality set to: High-Quality (AAC)")
	case "alac":
		fmt.Println("Quality set to: Lossless (ALAC)")
	}
}

// promptForQuality asks the user to select a download quality for the chosen media.
func promptForQuality(item SearchResultItem, token string) (string, error) {
	if item.Type == "Artist" {
		fmt.Println("Artist selected. Proceeding to list all albums/videos.")
		return "default", nil
	}

	fmt.Printf("\nFetching available qualities for: %s\n", item.Name)

	qualities := []QualityOption{
		{ID: "alac", Description: "Lossless (ALAC)"},
		{ID: "aac", Description: "High-Quality (AAC)"},
		{ID: "atmos", Description: "Dolby Atmos"},
	}
	qualityOptions := []string{}
	for _, q := range qualities {
		qualityOptions = append(qualityOptions, q.Description)
	}

	prompt := &survey.Select{
		Message:  "Select a quality to download:",
		Options:  qualityOptions,
		PageSize: 5,
	}

	selectedIndex := 0
	err := survey.AskOne(prompt, &selectedIndex)
	if err != nil {
		// This can happen if the user presses Ctrl+C
		return "", nil
	}

	return qualities[selectedIndex].ID, nil
}

// handleSearch manages the entire interactive search process.
func handleSearch(searchType string, queryParts []string, token string) (string, error) {
	query := strings.Join(queryParts, " ")
	validTypes := map[string]bool{"album": true, "song": true, "artist": true}
	if !validTypes[searchType] {
		return "", fmt.Errorf("invalid search type: %s. Use 'album', 'song', or 'artist'", searchType)
	}

	fmt.Printf("Searching for %ss: \"%s\" in storefront \"%s\"\n", searchType, query, Config.Storefront)

	offset := 0
	limit := 15 // Increased limit for better navigation

	apiSearchType := searchType + "s"

	for {
		searchResp, err := ampapi.Search(Config.Storefront, query, apiSearchType, Config.Language, token, limit, offset)
		if err != nil {
			return "", fmt.Errorf("error fetching search results: %w", err)
		}

		var items []SearchResultItem
		var displayOptions []string
		hasNext := false

		// Special options for navigation
		const prevPageOpt = "⬅️  Previous Page"
		const nextPageOpt = "➡️  Next Page"

		// Add previous page option if applicable
		if offset > 0 {
			displayOptions = append(displayOptions, prevPageOpt)
		}

		switch searchType {
		case "album":
			if searchResp.Results.Albums != nil {
				for _, item := range searchResp.Results.Albums.Data {
					year := ""
					if len(item.Attributes.ReleaseDate) >= 4 {
						year = item.Attributes.ReleaseDate[:4]
					}
					trackInfo := fmt.Sprintf("%d tracks", item.Attributes.TrackCount)
					detail := fmt.Sprintf("%s (%s, %s)", item.Attributes.ArtistName, year, trackInfo)
					displayOptions = append(displayOptions, fmt.Sprintf("%s - %s", item.Attributes.Name, detail))
					items = append(items, SearchResultItem{Type: "Album", URL: item.Attributes.URL, ID: item.ID})
				}
				hasNext = searchResp.Results.Albums.Next != ""
			}
		case "song":
			if searchResp.Results.Songs != nil {
				for _, item := range searchResp.Results.Songs.Data {
					detail := fmt.Sprintf("%s (%s)", item.Attributes.ArtistName, item.Attributes.AlbumName)
					displayOptions = append(displayOptions, fmt.Sprintf("%s - %s", item.Attributes.Name, detail))
					items = append(items, SearchResultItem{Type: "Song", URL: item.Attributes.URL, ID: item.ID})
				}
				hasNext = searchResp.Results.Songs.Next != ""
			}
		case "artist":
			if searchResp.Results.Artists != nil {
				for _, item := range searchResp.Results.Artists.Data {
					detail := ""
					if len(item.Attributes.GenreNames) > 0 {
						detail = strings.Join(item.Attributes.GenreNames, ", ")
					}
					displayOptions = append(displayOptions, fmt.Sprintf("%s (%s)", item.Attributes.Name, detail))
					items = append(items, SearchResultItem{Type: "Artist", URL: item.Attributes.URL, ID: item.ID})
				}
				hasNext = searchResp.Results.Artists.Next != ""
			}
		}

		if len(items) == 0 && offset == 0 {
			fmt.Println("No results found.")
			return "", nil
		}

		// Add next page option if applicable
		if hasNext {
			displayOptions = append(displayOptions, nextPageOpt)
		}

		prompt := &survey.Select{
			Message:  "Use arrow keys to navigate, Enter to select:",
			Options:  displayOptions,
			PageSize: limit, // Show a full page of results
		}

		selectedIndex := 0
		err = survey.AskOne(prompt, &selectedIndex)
		if err != nil {
			// User pressed Ctrl+C
			return "", nil
		}

		selectedOption := displayOptions[selectedIndex]

		// Handle pagination
		if selectedOption == nextPageOpt {
			offset += limit
			continue
		}
		if selectedOption == prevPageOpt {
			offset -= limit
			continue
		}

		// Adjust index to match the `items` slice if "Previous Page" was an option
		itemIndex := selectedIndex
		if offset > 0 {
			itemIndex--
		}

		selectedItem := items[itemIndex]

		// Automatically set single song download flag
		if selectedItem.Type == "Song" {
			dl_song = true
		}

		quality, err := promptForQuality(selectedItem, token)
		if err != nil {
			return "", fmt.Errorf("could not process quality selection: %w", err)
		}
		if quality == "" { // User cancelled quality selection
			fmt.Println("Selection cancelled.")
			return "", nil
		}

		if quality != "default" {
			setDlFlags(quality)
		}

		return selectedItem.URL, nil
	}
}

// END: New functions for search functionality

func shouldRunLegacyTrack(sourceID string) bool {
	if len(legacyRetrySourceIDs) == 0 {
		return true
	}
	_, selected := legacyRetrySourceIDs[sourceID]
	return selected
}

func legacyTrackItemStatus(before, after structs.Counter) JobItemStatus {
	if after.Error > before.Error {
		return JobItemStatusFailed
	}
	if after.Unavailable > before.Unavailable || after.NotSong > before.NotSong {
		return JobItemStatusUnavailable
	}
	if after.Skipped > before.Skipped {
		return JobItemStatusSkipped
	}
	if after.Success > before.Success {
		return JobItemStatusSucceeded
	}
	// A legacy branch that returns without changing a counter cannot be safely
	// considered successful. Mark it failed so the durable retry action remains
	// available instead of silently losing a track.
	return JobItemStatusFailed
}

func recordMediaTagFailure(counter *structs.Counter) {
	if counter != nil {
		counter.Error++
	}
}

func legacyTrackRelativePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	relative, err := filepath.Rel(defaultJobOutputDir(), path)
	if err != nil || validateRelativePath(relative) != nil {
		return ""
	}
	return filepath.ToSlash(relative)
}

// emitLegacyJobOutputPath reports a file only after the legacy downloader has
// finished publishing it. The parent verifies this exact regular file and
// persists it immediately, so task ownership does not depend on a shared
// output-directory before/after scan.
func emitLegacyJobOutputPath(path string) {
	if os.Getenv("AMDL_JOB_CHILD") != "1" {
		return
	}
	relativePath := legacyTrackRelativePath(path)
	if relativePath == "" {
		return
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	emitLegacyJobOutput(JobFileInput{
		RelativePath: relativePath,
		Name:         filepath.Base(relativePath),
		Size:         info.Size(),
	})
}

func ripTrack(track *task.Track, token string, mediaUserToken string) {
	_ = ripTrackContext(context.Background(), track, token, mediaUserToken)
}

func ripTrackContext(ctx context.Context, track *task.Track, token string, mediaUserToken string) (result error) {
	ctx = networkContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if track == nil {
		return
	}
	itemSourceID := track.SourceID
	if itemSourceID == "" {
		itemSourceID = track.ID
	}
	if !shouldRunLegacyTrack(itemSourceID) {
		return
	}
	ctx, finishTransferItem := transfer.BeginItem(ctx, itemSourceID)
	defer finishTransferItem()
	// Track downloads run concurrently. Keep all per-track accounting local and
	// merge it once at the end so workers never race on the process-wide summary.
	counter := structs.Counter{}
	before := counter
	stage := "metadata"
	var failureErr error
	name := track.Resp.Attributes.Name
	emitLegacyJobItem(JobItemInput{SourceID: itemSourceID, Name: name, Status: JobItemStatusRunning})
	defer func() {
		status := legacyTrackItemStatus(before, counter)
		if status == JobItemStatusFailed && nativeDownloadClient != nil && errors.Is(failureErr, nativeauth.ErrUnverified) {
			status = JobItemStatusInterrupted
		}
		transfer.Complete(ctx, string(status))
		item := JobItemInput{SourceID: itemSourceID, Name: name, Status: status}
		if status == JobItemStatusFailed || status == JobItemStatusInterrupted {
			item.ErrorCode, item.ErrorMessage = downloadItemDiagnostic(stage, failureErr)
		} else if status == JobItemStatusUnavailable {
			item.ErrorCode, item.ErrorMessage = "track_unavailable", "track has no supported playable resource"
		}
		if status == JobItemStatusSucceeded || status == JobItemStatusSkipped {
			item.RelativePath = legacyTrackRelativePath(track.SavePath)
		}
		emitLegacyJobItem(item)
		addLegacyCounter(counter)
	}()
	var err error
	counter.Total++
	fmt.Printf("Track %d of %d: %s\n", track.TaskNum, track.TaskTotal, track.Type)
	//提前获取到的播放列表下track所在的专辑信息
	if track.PreType == "playlists" && Config.UseSongInfoForPlaylist {
		if err := track.GetAlbumDataContext(ctx, token); err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	//mv dl dev
	if track.Type == "music-videos" {
		stage = "credentials"
		if len(mediaUserToken) <= 50 {
			fmt.Println("meida-user-token is not set, skip MV dl")
			counter.Error++
			return
		}
		if _, err := exec.LookPath("mp4decrypt"); err != nil {
			fmt.Println("mp4decrypt is not found, skip MV dl")
			counter.Error++
			return
		}
		stage = "media"
		err := mvDownloaderContext(ctx, track.ID, track.SaveDir, token, track.Storefront, mediaUserToken, track)
		failureErr = err
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println("\u26A0 Failed to dl MV:", err)
			counter.Error++
			return
		}
		counter.Success++
		return
	}
	needDlAacLc := false
	if dl_aac && Config.AacType == "aac-lc" {
		needDlAacLc = true
	}
	if nativeDownloadClient == nil && track.WebM3u8 == "" && !needDlAacLc {
		if dl_atmos {
			fmt.Println("Unavailable")
			counter.Unavailable++
			return
		}
		fmt.Println("Unavailable, trying to dl aac-lc")
		needDlAacLc = true
	}
	needCheck := false

	if Config.GetM3u8Mode == "all" {
		needCheck = true
	} else if Config.GetM3u8Mode == "hires" && contains(track.Resp.Attributes.AudioTraits, "hi-res-lossless") {
		needCheck = true
	}
	var EnhancedHls_m3u8 string
	if nativeDownloadClient == nil && needCheck && !needDlAacLc {
		EnhancedHls_m3u8, _ = checkM3u8Context(ctx, track.ID, "song")
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasSuffix(EnhancedHls_m3u8, ".m3u8") {
			track.DeviceM3u8 = EnhancedHls_m3u8
			track.M3u8 = EnhancedHls_m3u8
		}
	}
	var Quality string
	var nativeVariant string
	if nativeDownloadClient != nil {
		nativeVariant, Quality, err = nativeTrackVariantForDuration(ctx, track.ID, int64(track.Resp.Attributes.DurationInMillis))
		if err != nil {
			stage = "manifest"
			failureErr = err
			counter.Error++
			return err
		}
	}
	if nativeDownloadClient == nil && strings.Contains(Config.SongFileFormat, "Quality") {
		if dl_atmos {
			Quality = fmt.Sprintf("%dKbps", Config.AtmosMax-2000)
		} else if needDlAacLc {
			Quality = "256Kbps"
		} else {
			stage = "manifest"
			_, Quality, err = extractMediaContext(ctx, track.M3u8, true)
			failureErr = err
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				fmt.Println("Failed to extract quality from manifest.\n", err)
				counter.Error++
				return
			}
		}
	}
	track.Quality = Quality

	stringsToJoin := []string{}
	if track.Resp.Attributes.IsAppleDigitalMaster {
		if Config.AppleMasterChoice != "" {
			stringsToJoin = append(stringsToJoin, Config.AppleMasterChoice)
		}
	}
	if track.Resp.Attributes.ContentRating == "explicit" {
		if Config.ExplicitChoice != "" {
			stringsToJoin = append(stringsToJoin, Config.ExplicitChoice)
		}
	}
	if track.Resp.Attributes.ContentRating == "clean" {
		if Config.CleanChoice != "" {
			stringsToJoin = append(stringsToJoin, Config.CleanChoice)
		}
	}
	Tag_string := strings.Join(stringsToJoin, " ")

	songName := trackFilename(Config.SongFileFormat, track, Quality, Tag_string)
	fmt.Println(songName)
	filename := fmt.Sprintf("%s.m4a", forbiddenNames.ReplaceAllString(songName, "_"))
	track.SaveName = filename
	trackPath := filepath.Join(track.SaveDir, track.SaveName)
	stage = "output"
	output, err := prepareLegacyTrackMediaOutputContext(ctx, trackPath)
	failureErr = err
	if err != nil {
		fmt.Printf("Existing track cannot be used: %v\n", err)
		counter.Error++
		return
	}
	if output.Skipped {
		fmt.Println("Track exists, skipped.")
		track.SavePath = trackPath
		counter.Success++
		counter.Skipped++
		markLegacyTrackDone(track.PreID, track.TaskNum)
		return
	}
	defer output.Cleanup()
	workPath := output.WorkPath
	lrcFilename := fmt.Sprintf("%s.%s", forbiddenNames.ReplaceAllString(songName, "_"), Config.LrcFormat)

	//get lrc
	resolveTrackRegionalTextPreferences(ctx, track, token)
	if err := ctx.Err(); err != nil {
		return err
	}
	var lrc string = ""
	if Config.EmbedLrc || Config.SaveLrcFile {
		lrcStr, err := getTrackLyricsContext(ctx, track, token, mediaUserToken)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println(err)
		} else {
			if Config.SaveLrcFile {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := writeLyricsContext(ctx, track.SaveDir, lrcFilename, lrcStr); err != nil {
					stage = "lyrics"
					failureErr = err
					fmt.Printf("Failed to write lyrics: %v\n", err)
					counter.Error++
					return err
				}
			}
			if Config.EmbedLrc {
				lrc = lrcStr
			}
		}
	}

	if nativeDownloadClient != nil {
		stage = "media"
		err = nativeDownloadTrack(ctx, track.ID, nativeVariant, workPath)
		failureErr = err
		if err != nil {
			counter.Error++
			return err
		}
	} else if needDlAacLc {
		stage = "credentials"
		if len(mediaUserToken) <= 50 {
			fmt.Println("Invalid media-user-token")
			counter.Error++
			return
		}
		stage = "media"
		_, err := runv3.RunContext(ctx, track.ID, workPath, token, mediaUserToken, false)
		failureErr = err
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println("Failed to dl aac-lc:", err)
			if err.Error() == "Unavailable" {
				counter.Unavailable++
				return
			}
			counter.Error++
			return
		}
	} else {
		stage = "manifest"
		trackM3u8Url, _, err := extractMediaContext(ctx, track.M3u8, false)
		failureErr = err
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println("\u26A0 Failed to extract info from manifest:", err)
			counter.Error++
			return
		}
		//边下载边解密
		stage = "media"
		err = runv2.RunContext(ctx, track.ID, trackM3u8Url, workPath, Config)
		failureErr = err
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println("Failed to run v2:", err)
			counter.Error++
			return
		}
	}
	// Validate the temporary output before any tagger observes it.  Cleanup is
	// limited to this known temporary file, never the user's destination.
	if err := validateMediaOutput(workPath); err != nil {
		stage = "validation"
		failureErr = err
		fmt.Println("Downloaded file is empty or corrupted")
		counter.Error++
		return
	}
	stage = "muxing"
	transfer.Phase(ctx, "processing")
	if err := prepareCompatibleAudioContext(ctx, workPath); err != nil {
		failureErr = err
		counter.Error++
		return err
	}
	coverPath, temporaryCover, coverErr := resolveTrackCoverContext(ctx, track)
	if coverErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fmt.Println("Failed to write track cover.")
	}
	if temporaryCover {
		defer os.Remove(coverPath)
	}
	track.SavePath = workPath
	if err := ctx.Err(); err != nil {
		return err
	}
	stage = "tagging"
	transfer.Phase(ctx, "tagging")
	err = writeMP4TagsContext(ctx, track, lrc, coverPath)
	failureErr = err
	if err != nil {
		fmt.Println("\u26A0 Failed to write tags in media:", err)
		recordMediaTagFailure(&counter)
		return
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	transfer.Phase(ctx, "publish")
	if err := output.Commit(); err != nil {
		fmt.Println("\u26A0 Failed to publish media:", err)
		stage = "publish"
		failureErr = err
		counter.Error++
		return
	}
	track.SavePath = trackPath
	emitLegacyJobOutputPath(trackPath)
	counter.Success++
	markLegacyTrackDone(track.PreID, track.TaskNum)
	return nil
}

// resolveTrackCoverContext keeps the playlist's folder artwork separate from
// the cover embedded in each song. Missing or failed track artwork never
// silently substitutes the playlist image for that song.
func resolveTrackCoverContext(ctx context.Context, track *task.Track) (string, bool, error) {
	if track == nil || !Config.EmbedCover {
		return "", false, nil
	}
	useTrackArtwork := track.PreType == "playlists" ||
		(track.PreType == "stations" && strings.Contains(track.PreID, "ra.") && Config.DlAlbumcoverForPlaylist)
	if !useTrackArtwork {
		return track.CoverPath, false, nil
	}
	fallbackCover := ""
	if track.PreType == "stations" {
		fallbackCover = track.CoverPath
	}
	artworkURL := strings.TrimSpace(track.Resp.Attributes.Artwork.URL)
	if artworkURL == "" {
		return fallbackCover, false, nil
	}
	coverPath, err := writeCoverWithoutJobOutputContext(ctx, track.SaveDir, track.ID, artworkURL)
	if err != nil {
		return fallbackCover, false, err
	}
	return coverPath, true, nil
}

func ripStation(albumId string, token string, storefront string, mediaUserToken string) error {
	return ripStationContext(context.Background(), albumId, token, storefront, mediaUserToken)
}

func ripStationContext(ctx context.Context, albumId string, token string, storefront string, mediaUserToken string) error {
	ctx = networkContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	station := task.NewStation(storefront, albumId)
	err := station.GetRespContext(ctx, mediaUserToken, token, Config.Language)
	if err != nil {
		return err
	}
	fmt.Println(" -", station.Type)
	meta := station.Resp

	var Codec string
	if dl_atmos {
		Codec = "ATMOS"
	} else if dl_aac {
		Codec = "AAC"
	} else {
		Codec = "ALAC"
	}
	station.Codec = Codec
	var singerFoldername string
	if Config.ArtistFolderFormat != "" {
		singerFoldername = strings.NewReplacer(
			"{ArtistName}", "Apple Music Station",
			"{ArtistId}", "",
			"{UrlArtistName}", "Apple Music Station",
		).Replace(Config.ArtistFolderFormat)
		if strings.HasSuffix(singerFoldername, ".") {
			singerFoldername = strings.ReplaceAll(singerFoldername, ".", "")
		}
		singerFoldername = strings.TrimSpace(singerFoldername)
		fmt.Println(singerFoldername)
	}
	singerFolder := filepath.Join(Config.AlacSaveFolder, forbiddenNames.ReplaceAllString(singerFoldername, "_"))
	if dl_atmos {
		singerFolder = filepath.Join(Config.AtmosSaveFolder, forbiddenNames.ReplaceAllString(singerFoldername, "_"))
	}
	if dl_aac {
		singerFolder = filepath.Join(Config.AacSaveFolder, forbiddenNames.ReplaceAllString(singerFoldername, "_"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	os.MkdirAll(singerFolder, os.ModePerm)
	station.SaveDir = singerFolder

	playlistFolder := strings.NewReplacer(
		"{ArtistName}", "Apple Music Station",
		"{PlaylistName}", LimitString(station.Name),
		"{PlaylistId}", station.ID,
		"{Quality}", "",
		"{Codec}", Codec,
		"{Tag}", "",
	).Replace(Config.PlaylistFolderFormat)
	if strings.HasSuffix(playlistFolder, ".") {
		playlistFolder = strings.ReplaceAll(playlistFolder, ".", "")
	}
	playlistFolder = strings.TrimSpace(playlistFolder)
	playlistFolderPath := filepath.Join(singerFolder, forbiddenNames.ReplaceAllString(playlistFolder, "_"))
	if err := ctx.Err(); err != nil {
		return err
	}
	os.MkdirAll(playlistFolderPath, os.ModePerm)
	station.SaveName = playlistFolder
	fmt.Println(playlistFolder)

	covPath, err := writeCoverContext(ctx, playlistFolderPath, "cover", meta.Data[0].Attributes.Artwork.URL)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errExistingOutputPolicy) {
			return err
		}
		fmt.Println("Failed to write cover.")
	}
	station.CoverPath = covPath

	if Config.SaveAnimatedArtwork && meta.Data[0].Attributes.EditorialVideo.MotionSquare.Video != "" {
		fmt.Println("Found Animation Artwork.")

		motionvideoUrlSquare, err := extractVideoContext(ctx, meta.Data[0].Attributes.EditorialVideo.MotionSquare.Video)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println("no motion video square.\n", err)
		} else {
			exists, err := fileExists(filepath.Join(playlistFolderPath, "square_animated_artwork.mp4"))
			if err != nil {
				fmt.Println("Failed to check if animated artwork square exists.")
			}
			if exists {
				fmt.Println("Animated artwork square already exists locally.")
			} else {
				fmt.Println("Animation Artwork Square Downloading...")
				if err := runMotionArtworkFFmpeg(ctx, motionvideoUrlSquare, filepath.Join(playlistFolderPath, "square_animated_artwork.mp4")); err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					fmt.Printf("animated artwork square dl err: %v\n", err)
				} else {
					fmt.Println("Animation Artwork Square Downloaded")
					emitLegacyJobOutputPath(filepath.Join(playlistFolderPath, "square_animated_artwork.mp4"))
				}
			}
		}

		if Config.EmbyAnimatedArtwork {
			cmd3 := exec.CommandContext(ctx, "ffmpeg", "-i", filepath.Join(playlistFolderPath, "square_animated_artwork.mp4"), "-vf", "scale=440:-1", "-r", "24", "-f", "gif", filepath.Join(playlistFolderPath, "folder.jpg"))
			if err := cmd3.Run(); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				fmt.Printf("animated artwork square to gif err: %v\n", err)
			} else {
				emitLegacyJobOutputPath(filepath.Join(playlistFolderPath, "folder.jpg"))
			}
		}
	}
	if station.Type == "stream" {
		counter.Total++
		if isInArray(okDict[station.ID], 1) {
			counter.Success++
			return nil
		}
		stationTrack := &task.Track{ID: station.ID, TaskNum: 1, Codec: "AAC"}
		stationTrack.Resp.Attributes.Name = station.Name
		stationTrack.Resp.Attributes.DiscNumber, stationTrack.Resp.Attributes.TrackNumber = 1, 1
		songName := trackFilename(Config.SongFileFormat, stationTrack, "256Kbps", "")
		fmt.Println(songName)
		trackPath := filepath.Join(playlistFolderPath, fmt.Sprintf("%s.m4a", forbiddenNames.ReplaceAllString(songName, "_")))
		exists, _ := fileExists(trackPath)
		if exists {
			counter.Success++
			okDict[station.ID] = append(okDict[station.ID], 1)

			fmt.Println("Radio already exists locally.")
			return nil
		}
		assetsUrl, err := ampapi.GetStationAssetsUrlContext(ctx, station.ID, mediaUserToken, token)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println("Failed to get station assets url.", err)
			counter.Error++
			return err
		}
		trackM3U8 := strings.ReplaceAll(assetsUrl, "index.m3u8", "256/prog_index.m3u8")
		keyAndUrls, err := runv3.RunContext(ctx, station.ID, trackM3U8, token, mediaUserToken, true)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println("Failed to obtain station stream keys.", err)
			counter.Error++
			return err
		}
		err = runv3.ExtMvDataContext(ctx, keyAndUrls, trackPath)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println("Failed to download station stream.", err)
			counter.Error++
			return err
		}
		tags := []string{
			"tool=",
			"disk=1/1",
			"track=1",
			"tracknum=1/1",
			fmt.Sprintf("artist=%s", "Apple Music Station"),
			fmt.Sprintf("performer=%s", "Apple Music Station"),
			fmt.Sprintf("album_artist=%s", "Apple Music Station"),
			fmt.Sprintf("album=%s", station.Name),
			fmt.Sprintf("title=%s", station.Name),
		}
		if Config.EmbedCover {
			tags = append(tags, fmt.Sprintf("cover=%s", station.CoverPath))
		}
		tagsString := strings.Join(tags, ":")
		cmd := exec.CommandContext(ctx, "MP4Box", "-itags", tagsString, trackPath)
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Printf("MP4Box skipped (not installed?): %v\n", err)
		}
		emitLegacyJobOutputPath(trackPath)
		counter.Success++
		okDict[station.ID] = append(okDict[station.ID], 1)
		return nil
	}

	for i := range station.Tracks {
		station.Tracks[i].CoverPath = covPath
		station.Tracks[i].SaveDir = playlistFolderPath
		station.Tracks[i].Codec = Codec
	}

	trackTotal := len(station.Tracks)
	arr := make([]int, trackTotal)
	for i := 0; i < trackTotal; i++ {
		arr[i] = i + 1
	}
	var selected []int

	if true {
		selected = arr
	}
	tracks := make([]*task.Track, 0, len(selected))
	for i := range station.Tracks {
		if isInArray(selected, i+1) {
			tracks = append(tracks, &station.Tracks[i])
		}
	}
	return runMediaTrackPool(ctx, tracks, configuredDownloadConcurrency(), func(trackCtx context.Context, track *task.Track) error {
		return ripTrackContext(trackCtx, track, token, mediaUserToken)
	})
}

func ripAlbum(albumId string, token string, storefront string, mediaUserToken string, urlArg_i string) error {
	return ripAlbumContext(context.Background(), albumId, token, storefront, mediaUserToken, urlArg_i)
}

func ripAlbumContext(ctx context.Context, albumId string, token string, storefront string, mediaUserToken string, urlArg_i string) error {
	ctx = networkContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	album := task.NewAlbum(storefront, albumId)
	err := album.GetRespContext(ctx, token, Config.Language)
	if err != nil {
		fmt.Println("Failed to get album response.")
		return err
	}
	meta := album.Resp
	if len(meta.Data) > 0 {
		title := meta.Data[0].Attributes.Name
		if urlArg_i != "" {
			title = ""
			for _, track := range meta.Data[0].Relationships.Tracks.Data {
				if track.ID == urlArg_i {
					title = track.Attributes.Name
					break
				}
			}
		}
		if title != "" {
			emitLegacyJobMetadata(jobMetadata{Title: title})
		}
	}
	if debug_mode {
		fmt.Println(meta.Data[0].Attributes.ArtistName)
		fmt.Println(meta.Data[0].Attributes.Name)

		for trackNum, track := range meta.Data[0].Relationships.Tracks.Data {
			if err := ctx.Err(); err != nil {
				return err
			}
			trackNum++
			fmt.Printf("\nTrack %d of %d:\n", trackNum, len(meta.Data[0].Relationships.Tracks.Data))
			fmt.Printf("%02d. %s\n", trackNum, track.Attributes.Name)

			manifest, err := ampapi.GetSongRespContext(ctx, storefront, track.ID, album.Language, token)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				fmt.Printf("Failed to get manifest for track %d: %v\n", trackNum, err)
				continue
			}

			var m3u8Url string
			if manifest.Data[0].Attributes.ExtendedAssetUrls.EnhancedHls != "" {
				m3u8Url = manifest.Data[0].Attributes.ExtendedAssetUrls.EnhancedHls
			}
			needCheck := false
			if Config.GetM3u8Mode == "all" {
				needCheck = true
			} else if Config.GetM3u8Mode == "hires" && contains(track.Attributes.AudioTraits, "hi-res-lossless") {
				needCheck = true
			}
			if needCheck {
				fullM3u8Url, err := checkM3u8Context(ctx, track.ID, "song")
				if err == nil && strings.HasSuffix(fullM3u8Url, ".m3u8") {
					m3u8Url = fullM3u8Url
				} else {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					fmt.Println("Failed to get best quality m3u8 from device m3u8 port, will use m3u8 from Web API")
				}
			}

			_, _, err = extractMediaContext(ctx, m3u8Url, true)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				fmt.Printf("Failed to extract quality info for track %d: %v\n", trackNum, err)
				continue
			}
		}
		return nil
	}
	var Codec string
	if dl_atmos {
		Codec = "ATMOS"
	} else if dl_aac {
		Codec = "AAC"
	} else {
		Codec = "ALAC"
	}
	album.Codec = Codec
	var singerFoldername string
	if Config.ArtistFolderFormat != "" {
		if len(meta.Data[0].Relationships.Artists.Data) > 0 {
			singerFoldername = strings.NewReplacer(
				"{UrlArtistName}", LimitString(meta.Data[0].Attributes.ArtistName),
				"{ArtistName}", LimitString(meta.Data[0].Attributes.ArtistName),
				"{ArtistId}", meta.Data[0].Relationships.Artists.Data[0].ID,
			).Replace(Config.ArtistFolderFormat)
		} else {
			singerFoldername = strings.NewReplacer(
				"{UrlArtistName}", LimitString(meta.Data[0].Attributes.ArtistName),
				"{ArtistName}", LimitString(meta.Data[0].Attributes.ArtistName),
				"{ArtistId}", "",
			).Replace(Config.ArtistFolderFormat)
		}
		if strings.HasSuffix(singerFoldername, ".") {
			singerFoldername = strings.ReplaceAll(singerFoldername, ".", "")
		}
		singerFoldername = strings.TrimSpace(singerFoldername)
		fmt.Println(singerFoldername)
	}
	singerFolder := filepath.Join(Config.AlacSaveFolder, forbiddenNames.ReplaceAllString(singerFoldername, "_"))
	if dl_atmos {
		singerFolder = filepath.Join(Config.AtmosSaveFolder, forbiddenNames.ReplaceAllString(singerFoldername, "_"))
	}
	if dl_aac {
		singerFolder = filepath.Join(Config.AacSaveFolder, forbiddenNames.ReplaceAllString(singerFoldername, "_"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	os.MkdirAll(singerFolder, os.ModePerm)
	album.SaveDir = singerFolder
	var Quality string
	if strings.Contains(Config.AlbumFolderFormat, "Quality") {
		if dl_atmos {
			Quality = fmt.Sprintf("%dKbps", Config.AtmosMax-2000)
		} else if dl_aac && Config.AacType == "aac-lc" {
			Quality = "256Kbps"
		} else {
			manifest1, err := ampapi.GetSongRespContext(ctx, storefront, meta.Data[0].Relationships.Tracks.Data[0].ID, album.Language, token)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				fmt.Println("Failed to get manifest.\n", err)
			} else {
				if manifest1.Data[0].Attributes.ExtendedAssetUrls.EnhancedHls == "" {
					Codec = "AAC"
					Quality = "256Kbps"
				} else {
					needCheck := false

					if Config.GetM3u8Mode == "all" {
						needCheck = true
					} else if Config.GetM3u8Mode == "hires" && contains(meta.Data[0].Relationships.Tracks.Data[0].Attributes.AudioTraits, "hi-res-lossless") {
						needCheck = true
					}
					var EnhancedHls_m3u8 string
					if needCheck {
						EnhancedHls_m3u8, _ = checkM3u8Context(ctx, meta.Data[0].Relationships.Tracks.Data[0].ID, "album")
						if err := ctx.Err(); err != nil {
							return err
						}
						if strings.HasSuffix(EnhancedHls_m3u8, ".m3u8") {
							manifest1.Data[0].Attributes.ExtendedAssetUrls.EnhancedHls = EnhancedHls_m3u8
						}
					}
					_, Quality, err = extractMediaContext(ctx, manifest1.Data[0].Attributes.ExtendedAssetUrls.EnhancedHls, true)
					if err != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						fmt.Println("Failed to extract quality from manifest.\n", err)
					}
				}
			}
		}
	}
	stringsToJoin := []string{}
	if meta.Data[0].Attributes.IsAppleDigitalMaster || meta.Data[0].Attributes.IsMasteredForItunes {
		if Config.AppleMasterChoice != "" {
			stringsToJoin = append(stringsToJoin, Config.AppleMasterChoice)
		}
	}
	if meta.Data[0].Attributes.ContentRating == "explicit" {
		if Config.ExplicitChoice != "" {
			stringsToJoin = append(stringsToJoin, Config.ExplicitChoice)
		}
	}
	if meta.Data[0].Attributes.ContentRating == "clean" {
		if Config.CleanChoice != "" {
			stringsToJoin = append(stringsToJoin, Config.CleanChoice)
		}
	}
	Tag_string := strings.Join(stringsToJoin, " ")
	var albumFolderName string
	albumFolderName = strings.NewReplacer(
		"{ReleaseDate}", meta.Data[0].Attributes.ReleaseDate,
		"{ReleaseYear}", meta.Data[0].Attributes.ReleaseDate[:4],
		"{ArtistName}", LimitString(meta.Data[0].Attributes.ArtistName),
		"{AlbumName}", LimitString(meta.Data[0].Attributes.Name),
		"{UPC}", meta.Data[0].Attributes.Upc,
		"{RecordLabel}", meta.Data[0].Attributes.RecordLabel,
		"{Copyright}", meta.Data[0].Attributes.Copyright,
		"{AlbumId}", albumId,
		"{Quality}", Quality,
		"{Codec}", Codec,
		"{Tag}", Tag_string,
	).Replace(Config.AlbumFolderFormat)

	if strings.HasSuffix(albumFolderName, ".") {
		albumFolderName = strings.ReplaceAll(albumFolderName, ".", "")
	}
	albumFolderName = strings.TrimSpace(albumFolderName)
	albumFolderPath := filepath.Join(singerFolder, forbiddenNames.ReplaceAllString(albumFolderName, "_"))
	if err := ctx.Err(); err != nil {
		return err
	}
	os.MkdirAll(albumFolderPath, os.ModePerm)
	album.SaveName = albumFolderName
	fmt.Println(albumFolderName)
	if Config.SaveArtistCover {
		if len(meta.Data[0].Relationships.Artists.Data) > 0 {
			_, err = writeCoverContext(ctx, singerFolder, "folder", meta.Data[0].Relationships.Artists.Data[0].Attributes.Artwork.Url)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if errors.Is(err, errExistingOutputPolicy) {
					return err
				}
				fmt.Println("Failed to write artist cover.")
			}
		}
	}
	covPath, err := writeCoverContext(ctx, albumFolderPath, "cover", meta.Data[0].Attributes.Artwork.URL)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errExistingOutputPolicy) {
			return err
		}
		fmt.Println("Failed to write cover.")
	}
	if Config.SaveAnimatedArtwork && meta.Data[0].Attributes.EditorialVideo.MotionDetailSquare.Video != "" {
		fmt.Println("Found Animation Artwork.")

		motionvideoUrlSquare, err := extractVideoContext(ctx, meta.Data[0].Attributes.EditorialVideo.MotionDetailSquare.Video)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println("no motion video square.\n", err)
		} else {
			exists, err := fileExists(filepath.Join(albumFolderPath, "square_animated_artwork.mp4"))
			if err != nil {
				fmt.Println("Failed to check if animated artwork square exists.")
			}
			if exists {
				fmt.Println("Animated artwork square already exists locally.")
			} else {
				fmt.Println("Animation Artwork Square Downloading...")
				if err := runMotionArtworkFFmpeg(ctx, motionvideoUrlSquare, filepath.Join(albumFolderPath, "square_animated_artwork.mp4")); err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					fmt.Printf("animated artwork square dl err: %v\n", err)
				} else {
					fmt.Println("Animation Artwork Square Downloaded")
					emitLegacyJobOutputPath(filepath.Join(albumFolderPath, "square_animated_artwork.mp4"))
				}
			}
		}

		if Config.EmbyAnimatedArtwork {
			cmd3 := exec.CommandContext(ctx, "ffmpeg", "-i", filepath.Join(albumFolderPath, "square_animated_artwork.mp4"), "-vf", "scale=440:-1", "-r", "24", "-f", "gif", filepath.Join(albumFolderPath, "folder.jpg"))
			if err := cmd3.Run(); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				fmt.Printf("animated artwork square to gif err: %v\n", err)
			} else {
				emitLegacyJobOutputPath(filepath.Join(albumFolderPath, "folder.jpg"))
			}
		}

		motionvideoUrlTall, err := extractVideoContext(ctx, meta.Data[0].Attributes.EditorialVideo.MotionDetailTall.Video)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println("no motion video tall.\n", err)
		} else {
			exists, err := fileExists(filepath.Join(albumFolderPath, "tall_animated_artwork.mp4"))
			if err != nil {
				fmt.Println("Failed to check if animated artwork tall exists.")
			}
			if exists {
				fmt.Println("Animated artwork tall already exists locally.")
			} else {
				fmt.Println("Animation Artwork Tall Downloading...")
				if err := runMotionArtworkFFmpeg(ctx, motionvideoUrlTall, filepath.Join(albumFolderPath, "tall_animated_artwork.mp4")); err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					fmt.Printf("animated artwork tall dl err: %v\n", err)
				} else {
					fmt.Println("Animation Artwork Tall Downloaded")
					emitLegacyJobOutputPath(filepath.Join(albumFolderPath, "tall_animated_artwork.mp4"))
				}
			}
		}
	}
	for i := range album.Tracks {
		album.Tracks[i].CoverPath = covPath
		album.Tracks[i].SaveDir = albumFolderPath
		album.Tracks[i].Codec = Codec
	}
	trackTotal := len(meta.Data[0].Relationships.Tracks.Data)
	arr := make([]int, trackTotal)
	for i := 0; i < trackTotal; i++ {
		arr[i] = i + 1
	}

	if dl_song {
		if urlArg_i == "" {
		} else {
			for i := range album.Tracks {
				if err := ctx.Err(); err != nil {
					return err
				}
				if urlArg_i == album.Tracks[i].ID {
					planTrackTransfers(ctx, []*task.Track{&album.Tracks[i]})
					if err := ripTrackContext(ctx, &album.Tracks[i], token, mediaUserToken); err != nil {
						return err
					}
					return nil
				}
			}
		}
		return nil
	}
	var selected []int
	if !dl_select {
		selected = arr
	} else {
		selected = album.ShowSelect()
	}
	tracks := make([]*task.Track, 0, len(selected))
	for i := range album.Tracks {
		taskNumber := i + 1
		if legacyTrackAlreadyDone(albumId, taskNumber) {
			addLegacyCounter(structs.Counter{Total: 1, Success: 1})
			continue
		}
		if isInArray(selected, taskNumber) {
			tracks = append(tracks, &album.Tracks[i])
		}
	}
	return runMediaTrackPool(ctx, tracks, configuredDownloadConcurrency(), func(trackCtx context.Context, track *task.Track) error {
		return ripTrackContext(trackCtx, track, token, mediaUserToken)
	})

}
func ripPlaylist(playlistId string, token string, storefront string, mediaUserToken string) error {
	return ripPlaylistContext(context.Background(), playlistId, token, storefront, mediaUserToken)
}

func ripPlaylistContext(ctx context.Context, playlistId string, token string, storefront string, mediaUserToken string) error {
	source := PlaylistSource{Kind: PlaylistSourceCatalog, Storefront: storefront, PlaylistID: playlistId}
	if strings.HasPrefix(playlistId, "p.") {
		source.Kind = PlaylistSourceLibrary
	} else if strings.HasPrefix(playlistId, "pl.u-") {
		source.Kind = PlaylistSourcePublicUser
	}
	// Preserve the legacy helper's public fallback capability without changing
	// its API: construct only the canonical path shape implied by the already
	// validated storefront/id pair. New entry points always pass the parsed
	// PlaylistSource directly.
	if source.Kind == PlaylistSourceLibrary {
		source.CanonicalURL = fmt.Sprintf("https://music.apple.com/%s/library/playlist/%s", storefront, playlistId)
	} else {
		source.CanonicalURL = fmt.Sprintf("https://music.apple.com/%s/playlist/%s", storefront, playlistId)
	}
	return ripPlaylistSourceContext(ctx, source, token, mediaUserToken)
}

func emitPlaylistUnavailableItems(items []task.PlaylistUnavailableTrack) {
	for index, unavailable := range items {
		sourceID := unavailable.SourceID
		if sourceID == "" {
			sourceID = fmt.Sprintf("playlist-unavailable-%d", index+1)
		}
		status := JobItemStatusUnavailable
		if unavailable.ErrorCode == "duplicate_track" {
			status = JobItemStatusSkipped
		}
		emitLegacyJobItem(JobItemInput{SourceID: sourceID, Name: unavailable.Name, Status: status, ErrorCode: unavailable.ErrorCode})
		counter.Total++
		if status == JobItemStatusSkipped {
			counter.Skipped++
		} else {
			counter.Unavailable++
		}
	}
}

func ripPlaylistSourceContext(ctx context.Context, source PlaylistSource, token, mediaUserToken string) error {
	ctx = networkContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	playlistId := source.PlaylistID
	storefront := source.Storefront
	playlist := task.NewPlaylist(storefront, playlistId)
	playlist.SourceURL = source.CanonicalURL
	err := playlist.GetRespSourceContext(ctx, source.Kind, token, mediaUserToken, Config.Language)
	emitPlaylistUnavailableItems(playlist.Unavailable)
	if err != nil {
		fmt.Println("Failed to get playlist response.")
		return normalizePlaylistFailure(err)
	}
	// Library resources and credential-free public-page fallback resources both
	// carry only a CatalogID at this point.  Hydrate every source through the
	// Catalog endpoint so playback never falls back to credential-only AAC just
	// because the playlist envelope omitted extendedAssetUrls.
	if err := playlist.HydrateCatalogTracksContext(ctx, token); err != nil {
		return err
	}
	meta := playlist.Resp
	if len(meta.Data) > 0 {
		emitLegacyJobMetadata(jobMetadata{Title: meta.Data[0].Attributes.Name})
	}
	if debug_mode {
		fmt.Println(meta.Data[0].Attributes.ArtistName)
		fmt.Println(meta.Data[0].Attributes.Name)

		for trackNum, track := range meta.Data[0].Relationships.Tracks.Data {
			if err := ctx.Err(); err != nil {
				return err
			}
			trackNum++
			fmt.Printf("\nTrack %d of %d:\n", trackNum, len(meta.Data[0].Relationships.Tracks.Data))
			fmt.Printf("%02d. %s\n", trackNum, track.Attributes.Name)

			manifest, err := ampapi.GetSongRespContext(ctx, storefront, track.ID, playlist.Language, token)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				fmt.Printf("Failed to get manifest for track %d: %v\n", trackNum, err)
				continue
			}

			var m3u8Url string
			if manifest.Data[0].Attributes.ExtendedAssetUrls.EnhancedHls != "" {
				m3u8Url = manifest.Data[0].Attributes.ExtendedAssetUrls.EnhancedHls
			}
			needCheck := false
			if Config.GetM3u8Mode == "all" {
				needCheck = true
			} else if Config.GetM3u8Mode == "hires" && contains(track.Attributes.AudioTraits, "hi-res-lossless") {
				needCheck = true
			}
			if needCheck {
				fullM3u8Url, err := checkM3u8Context(ctx, track.ID, "song")
				if err == nil && strings.HasSuffix(fullM3u8Url, ".m3u8") {
					m3u8Url = fullM3u8Url
				} else {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					fmt.Println("Failed to get best quality m3u8 from device m3u8 port, will use m3u8 from Web API")
				}
			}

			_, _, err = extractMediaContext(ctx, m3u8Url, true)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				fmt.Printf("Failed to extract quality info for track %d: %v\n", trackNum, err)
				continue
			}
		}
		return nil
	}
	var Codec string
	if dl_atmos {
		Codec = "ATMOS"
	} else if dl_aac {
		Codec = "AAC"
	} else {
		Codec = "ALAC"
	}
	playlist.Codec = Codec
	var singerFoldername string
	if Config.ArtistFolderFormat != "" {
		singerFoldername = strings.NewReplacer(
			"{ArtistName}", "Apple Music",
			"{ArtistId}", "",
			"{UrlArtistName}", "Apple Music",
		).Replace(Config.ArtistFolderFormat)
		if strings.HasSuffix(singerFoldername, ".") {
			singerFoldername = strings.ReplaceAll(singerFoldername, ".", "")
		}
		singerFoldername = strings.TrimSpace(singerFoldername)
		fmt.Println(singerFoldername)
	}
	singerFolder := filepath.Join(Config.AlacSaveFolder, forbiddenNames.ReplaceAllString(singerFoldername, "_"))
	if dl_atmos {
		singerFolder = filepath.Join(Config.AtmosSaveFolder, forbiddenNames.ReplaceAllString(singerFoldername, "_"))
	}
	if dl_aac {
		singerFolder = filepath.Join(Config.AacSaveFolder, forbiddenNames.ReplaceAllString(singerFoldername, "_"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	os.MkdirAll(singerFolder, os.ModePerm)
	playlist.SaveDir = singerFolder

	var Quality string
	if strings.Contains(Config.AlbumFolderFormat, "Quality") {
		if dl_atmos {
			Quality = fmt.Sprintf("%dKbps", Config.AtmosMax-2000)
		} else if dl_aac && Config.AacType == "aac-lc" {
			Quality = "256Kbps"
		} else {
			manifest1, err := ampapi.GetSongRespContext(ctx, storefront, meta.Data[0].Relationships.Tracks.Data[0].ID, playlist.Language, token)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				fmt.Println("Failed to get manifest.\n", err)
			} else {
				if manifest1.Data[0].Attributes.ExtendedAssetUrls.EnhancedHls == "" {
					Codec = "AAC"
					Quality = "256Kbps"
				} else {
					needCheck := false

					if Config.GetM3u8Mode == "all" {
						needCheck = true
					} else if Config.GetM3u8Mode == "hires" && contains(meta.Data[0].Relationships.Tracks.Data[0].Attributes.AudioTraits, "hi-res-lossless") {
						needCheck = true
					}
					var EnhancedHls_m3u8 string
					if needCheck {
						EnhancedHls_m3u8, _ = checkM3u8Context(ctx, meta.Data[0].Relationships.Tracks.Data[0].ID, "album")
						if err := ctx.Err(); err != nil {
							return err
						}
						if strings.HasSuffix(EnhancedHls_m3u8, ".m3u8") {
							manifest1.Data[0].Attributes.ExtendedAssetUrls.EnhancedHls = EnhancedHls_m3u8
						}
					}
					_, Quality, err = extractMediaContext(ctx, manifest1.Data[0].Attributes.ExtendedAssetUrls.EnhancedHls, true)
					if err != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						fmt.Println("Failed to extract quality from manifest.\n", err)
					}
				}
			}
		}
	}
	stringsToJoin := []string{}
	if meta.Data[0].Attributes.IsAppleDigitalMaster || meta.Data[0].Attributes.IsMasteredForItunes {
		if Config.AppleMasterChoice != "" {
			stringsToJoin = append(stringsToJoin, Config.AppleMasterChoice)
		}
	}
	if meta.Data[0].Attributes.ContentRating == "explicit" {
		if Config.ExplicitChoice != "" {
			stringsToJoin = append(stringsToJoin, Config.ExplicitChoice)
		}
	}
	if meta.Data[0].Attributes.ContentRating == "clean" {
		if Config.CleanChoice != "" {
			stringsToJoin = append(stringsToJoin, Config.CleanChoice)
		}
	}
	Tag_string := strings.Join(stringsToJoin, " ")
	playlistFolder := strings.NewReplacer(
		"{ArtistName}", "Apple Music",
		"{PlaylistName}", LimitString(meta.Data[0].Attributes.Name),
		"{PlaylistId}", playlistId,
		"{Quality}", Quality,
		"{Codec}", Codec,
		"{Tag}", Tag_string,
	).Replace(Config.PlaylistFolderFormat)
	if strings.HasSuffix(playlistFolder, ".") {
		playlistFolder = strings.ReplaceAll(playlistFolder, ".", "")
	}
	playlistFolder = strings.TrimSpace(playlistFolder)
	playlistFolderPath := filepath.Join(singerFolder, forbiddenNames.ReplaceAllString(playlistFolder, "_"))
	if err := ctx.Err(); err != nil {
		return err
	}
	os.MkdirAll(playlistFolderPath, os.ModePerm)
	playlist.SaveName = playlistFolder
	fmt.Println(playlistFolder)
	covPath, err := writeCoverContext(ctx, playlistFolderPath, "cover", meta.Data[0].Attributes.Artwork.URL)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errExistingOutputPolicy) {
			return err
		}
		fmt.Println("Failed to write cover.")
	}

	for i := range playlist.Tracks {
		playlist.Tracks[i].CoverPath = covPath
		playlist.Tracks[i].SaveDir = playlistFolderPath
		playlist.Tracks[i].Codec = Codec
	}

	if Config.SaveAnimatedArtwork && meta.Data[0].Attributes.EditorialVideo.MotionDetailSquare.Video != "" {
		fmt.Println("Found Animation Artwork.")

		motionvideoUrlSquare, err := extractVideoContext(ctx, meta.Data[0].Attributes.EditorialVideo.MotionDetailSquare.Video)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println("no motion video square.\n", err)
		} else {
			exists, err := fileExists(filepath.Join(playlistFolderPath, "square_animated_artwork.mp4"))
			if err != nil {
				fmt.Println("Failed to check if animated artwork square exists.")
			}
			if exists {
				fmt.Println("Animated artwork square already exists locally.")
			} else {
				fmt.Println("Animation Artwork Square Downloading...")
				if err := runMotionArtworkFFmpeg(ctx, motionvideoUrlSquare, filepath.Join(playlistFolderPath, "square_animated_artwork.mp4")); err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					fmt.Printf("animated artwork square dl err: %v\n", err)
				} else {
					fmt.Println("Animation Artwork Square Downloaded")
					emitLegacyJobOutputPath(filepath.Join(playlistFolderPath, "square_animated_artwork.mp4"))
				}
			}
		}

		if Config.EmbyAnimatedArtwork {
			cmd3 := exec.CommandContext(ctx, "ffmpeg", "-i", filepath.Join(playlistFolderPath, "square_animated_artwork.mp4"), "-vf", "scale=440:-1", "-r", "24", "-f", "gif", filepath.Join(playlistFolderPath, "folder.jpg"))
			if err := cmd3.Run(); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				fmt.Printf("animated artwork square to gif err: %v\n", err)
			} else {
				emitLegacyJobOutputPath(filepath.Join(playlistFolderPath, "folder.jpg"))
			}
		}

		motionvideoUrlTall, err := extractVideoContext(ctx, meta.Data[0].Attributes.EditorialVideo.MotionDetailTall.Video)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println("no motion video tall.\n", err)
		} else {
			exists, err := fileExists(filepath.Join(playlistFolderPath, "tall_animated_artwork.mp4"))
			if err != nil {
				fmt.Println("Failed to check if animated artwork tall exists.")
			}
			if exists {
				fmt.Println("Animated artwork tall already exists locally.")
			} else {
				fmt.Println("Animation Artwork Tall Downloading...")
				if err := runMotionArtworkFFmpeg(ctx, motionvideoUrlTall, filepath.Join(playlistFolderPath, "tall_animated_artwork.mp4")); err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					fmt.Printf("animated artwork tall dl err: %v\n", err)
				} else {
					fmt.Println("Animation Artwork Tall Downloaded")
					emitLegacyJobOutputPath(filepath.Join(playlistFolderPath, "tall_animated_artwork.mp4"))
				}
			}
		}
	}
	trackTotal := len(meta.Data[0].Relationships.Tracks.Data)
	arr := make([]int, trackTotal)
	for i := 0; i < trackTotal; i++ {
		arr[i] = i + 1
	}
	var selected []int

	if !dl_select {
		selected = arr
	} else {
		selected = playlist.ShowSelect()
	}
	tracks := make([]*task.Track, 0, len(selected))
	for i := range playlist.Tracks {
		taskNumber := i + 1
		if legacyTrackAlreadyDone(playlistId, taskNumber) {
			addLegacyCounter(structs.Counter{Total: 1, Success: 1})
			continue
		}
		if isInArray(selected, taskNumber) {
			tracks = append(tracks, &playlist.Tracks[i])
		}
	}
	return runMediaTrackPool(ctx, tracks, configuredDownloadConcurrency(), func(trackCtx context.Context, track *task.Track) error {
		return ripTrackContext(trackCtx, track, token, mediaUserToken)
	})
}

func firstGenre(genres []string) string {
	if len(genres) == 0 {
		return ""
	}
	return genres[0]
}

// optionalItunesInternalID converts an Apple catalog identifier only when it
// can be represented by go-mp4tag's positive int32 fields. These internal
// linkage tags are optional metadata: an identifier outside that legacy range
// must be omitted rather than truncated or allowed to discard valid media.
func optionalItunesInternalID(value string) (int32, bool) {
	if value == "" {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil || parsed <= 0 {
		return 0, false
	}
	return int32(parsed), true
}

func writeMP4Tags(track *task.Track, lrc string) error {
	return writeMP4TagsContext(context.Background(), track, lrc, "")
}

func writeMP4TagsContext(ctx context.Context, track *task.Track, lrc, coverPath string) error {
	t := &mp4tag.MP4Tags{
		Title:      track.Resp.Attributes.Name,
		TitleSort:  track.Resp.Attributes.Name,
		Artist:     track.Resp.Attributes.ArtistName,
		ArtistSort: track.Resp.Attributes.ArtistName,
		Custom: map[string]string{
			"PERFORMER":   track.Resp.Attributes.ArtistName,
			"RELEASETIME": track.Resp.Attributes.ReleaseDate,
			"ISRC":        track.Resp.Attributes.Isrc,
			"LABEL":       "",
			"UPC":         "",
		},
		Composer:     track.Resp.Attributes.ComposerName,
		ComposerSort: track.Resp.Attributes.ComposerName,
		CustomGenre:  firstGenre(track.Resp.Attributes.GenreNames),
		Lyrics:       lrc,
		TrackNumber:  int16(track.Resp.Attributes.TrackNumber),
		DiscNumber:   int16(track.Resp.Attributes.DiscNumber),
		Album:        track.Resp.Attributes.AlbumName,
		AlbumSort:    track.Resp.Attributes.AlbumName,
	}

	if track.PreType == "albums" {
		if albumID, ok := optionalItunesInternalID(track.PreID); ok {
			t.ItunesAlbumID = albumID
		}
	}

	if len(track.Resp.Relationships.Artists.Data) > 0 {
		if artistID, ok := optionalItunesInternalID(track.Resp.Relationships.Artists.Data[0].ID); ok {
			t.ItunesArtistID = artistID
		}
	}

	if (track.PreType == "playlists" || track.PreType == "stations") && !Config.UseSongInfoForPlaylist {
		t.DiscNumber = 1
		t.DiscTotal = 1
		t.TrackNumber = int16(track.TaskNum)
		t.TrackTotal = int16(track.TaskTotal)
		t.Album = track.PlaylistData.Attributes.Name
		t.AlbumSort = track.PlaylistData.Attributes.Name
		t.AlbumArtist = track.PlaylistData.Attributes.ArtistName
		t.AlbumArtistSort = track.PlaylistData.Attributes.ArtistName
	} else if (track.PreType == "playlists" || track.PreType == "stations") && Config.UseSongInfoForPlaylist {
		t.DiscTotal = int16(track.DiscTotal)
		t.TrackTotal = int16(track.AlbumData.Attributes.TrackCount)
		t.AlbumArtist = track.AlbumData.Attributes.ArtistName
		t.AlbumArtistSort = track.AlbumData.Attributes.ArtistName
		t.Custom["UPC"] = track.AlbumData.Attributes.Upc
		t.Custom["LABEL"] = track.AlbumData.Attributes.RecordLabel
		t.Date = track.AlbumData.Attributes.ReleaseDate
		t.Copyright = track.AlbumData.Attributes.Copyright
		t.Publisher = track.AlbumData.Attributes.RecordLabel
	} else {
		t.DiscTotal = int16(track.DiscTotal)
		t.TrackTotal = int16(track.AlbumData.Attributes.TrackCount)
		t.AlbumArtist = track.AlbumData.Attributes.ArtistName
		t.AlbumArtistSort = track.AlbumData.Attributes.ArtistName
		t.Custom["UPC"] = track.AlbumData.Attributes.Upc
		t.Date = track.AlbumData.Attributes.ReleaseDate
		t.Copyright = track.AlbumData.Attributes.Copyright
		t.Publisher = track.AlbumData.Attributes.RecordLabel
	}

	if track.Resp.Attributes.ContentRating == "explicit" {
		t.ItunesAdvisory = mp4tag.ItunesAdvisoryExplicit
	} else if track.Resp.Attributes.ContentRating == "clean" {
		t.ItunesAdvisory = mp4tag.ItunesAdvisoryClean
	} else {
		t.ItunesAdvisory = mp4tag.ItunesAdvisoryNone
	}

	applyRegionalTextTags(t, track)
	return writeNativeMediaTags(ctx, track.SavePath, t, coverPath)
}

func runOneJobChild(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runOneJobChildContext(ctx, args)
}

// runOneJobChildContext is the isolated-job entry point used by the process
// executor. Keeping it separate makes the child cooperate with cancellation
// signals instead of restarting network work through Background contexts.
func runOneJobChildContext(ctx context.Context, args []string) error {
	ctx = networkContext(ctx)
	if os.Getenv("AMDL_JOB_CHILD") == "1" {
		var stop func()
		ctx, stop = transfer.Start(ctx, func(value JobTransfer) { emitLegacyJobMetadata(jobMetadata{Transfer: &value}) })
		defer stop()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	flags := pflag.NewFlagSet("__run-one", pflag.ContinueOnError)
	flags.SetInterspersed(true)
	flags.SetOutput(io.Discard)
	quality := flags.String("quality", QualityALAC, "")
	existing := flags.String("existing", string(ExistingPolicySkip), "")
	retryItems := flags.StringArray("retry-item", nil, "")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	urls := flags.Args()
	if len(urls) != 1 {
		return errors.New("internal job runner requires exactly one URL")
	}
	policy := ExistingPolicy(*existing)
	if policy != ExistingPolicySkip && policy != ExistingPolicyOverwrite && policy != ExistingPolicyFail {
		return fmt.Errorf("invalid existing-file policy %q", policy)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	configureJobOutputDirectories(defaultJobOutputDir())
	resetLegacyJobState()
	legacyRetrySourceIDs = make(map[string]struct{}, len(*retryItems))
	for _, sourceID := range *retryItems {
		if sourceID = strings.TrimSpace(sourceID); sourceID != "" {
			legacyRetrySourceIDs[sourceID] = struct{}{}
		}
	}
	return runDownloadWithExistingPolicyContext(ctx, DownloadRequest{URL: urls[0], Quality: *quality}, policy)
}

func configureJobOutputDirectories(root string) {
	Config.AlacSaveFolder = filepath.Join(root, "AM-DL downloads")
	Config.AtmosSaveFolder = filepath.Join(root, "AM-DL-Atmos downloads")
	Config.AacSaveFolder = filepath.Join(root, "AM-DL-AAC downloads")
	for _, directory := range []string{Config.AlacSaveFolder, Config.AtmosSaveFolder, Config.AacSaveFolder} {
		_ = os.MkdirAll(directory, 0o755)
	}
}

func resetLegacyJobState() {
	counter = structs.Counter{}
	okDict = make(map[string][]int)
	dl_atmos = false
	dl_aac = false
	dl_select = false
	dl_song = false
	artist_select = false
	debug_mode = false
	legacyRetrySourceIDs = nil
}

// contextDownloadRunner is the test seam for the isolated child boundary.
// Production always uses runDownloadContext.
var contextDownloadRunner = runDownloadContext

func runDownloadWithExistingPolicyContext(ctx context.Context, request DownloadRequest, policy ExistingPolicy) error {
	ctx = networkContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if policy != ExistingPolicySkip && policy != ExistingPolicyOverwrite && policy != ExistingPolicyFail {
		return fmt.Errorf("invalid existing-file policy %q", policy)
	}
	previousPolicy := legacyTrackExistingPolicy
	legacyTrackExistingPolicy = policy
	defer func() { legacyTrackExistingPolicy = previousPolicy }()
	return contextDownloadRunner(ctx, request)
}

// runDownloadContext dispatches supported download URLs while passing the job
// context through token lookup, metadata, manifests, media download, tagging
// and subprocess boundaries.
func runDownloadContext(ctx context.Context, request DownloadRequest) error {
	ctx = networkContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}

	if envToken := os.Getenv("MEDIA_USER_TOKEN"); envToken != "" && len(envToken) > 50 {
		mediaUserToken = envToken
		fmt.Println("Using media-user-token from environment variable")
	} else {
		mediaUserToken = Config.MediaUserToken
	}

	var token string
	var err error
	if nativeDownloadClient != nil {
		token = Config.AuthorizationToken
		mediaUserToken = Config.MediaUserToken
		if token == "" || mediaUserToken == "" {
			return errors.New("native_auth_required")
		}
	} else {
		token, err = ampapi.GetTokenContext(ctx)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if Config.AuthorizationToken != "" && Config.AuthorizationToken != "your-authorization-token" {
			token = strings.Replace(Config.AuthorizationToken, "Bearer ", "", -1)
		} else {
			return fmt.Errorf("failed to get token: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	setJobQualityFlags(request.Quality)

	urlRaw := rewriteDownloadStorefront(request.URL, Config.DownloadRegion)
	if nativeDownloadClient != nil && (strings.Contains(urlRaw, "/music-video/") || strings.Contains(urlRaw, "/station/")) {
		return errors.New("native_media_type_unsupported")
	}
	if source, playlistErr := parseAppleMusicPlaylistURL(urlRaw); playlistErr == nil {
		fmt.Println("Playlist")
		return ripPlaylistSourceContext(ctx, source, token, mediaUserToken)
	}

	if strings.Contains(urlRaw, "/album/") {
		fmt.Println("Album")
		storefront, id := checkUrl(urlRaw)
		if storefront == "" || id == "" {
			return fmt.Errorf("invalid album URL format")
		}
		urlArgI := ""
		if parsed, err := url.Parse(urlRaw); err == nil {
			urlArgI = parsed.Query().Get("i")
		}
		return retryFailedWithContext(ctx, 2, "album", func() error {
			return ripAlbumContext(ctx, id, token, storefront, mediaUserToken, urlArgI)
		}, sleepLegacyRetry)
	}

	if strings.Contains(urlRaw, "/song/") {
		fmt.Println("Song")
		storefront, songID := checkUrlSong(urlRaw)
		if storefront == "" || songID == "" {
			return fmt.Errorf("invalid song URL format")
		}
		return ripSongContext(ctx, songID, token, storefront, mediaUserToken)
	}

	if strings.Contains(urlRaw, "/music-video/") {
		fmt.Println("Music Video")
		storefront, musicVideoID := checkUrlMv(urlRaw)
		if storefront == "" || musicVideoID == "" {
			return fmt.Errorf("invalid music video URL format")
		}
		if len(mediaUserToken) <= 50 {
			return fmt.Errorf("media-user-token is required for music video download")
		}
		if _, err := exec.LookPath("mp4decrypt"); err != nil {
			return fmt.Errorf("mp4decrypt is not found, cannot download MV")
		}
		return mvDownloaderContext(ctx, musicVideoID, Config.AlacSaveFolder, token, storefront, mediaUserToken, nil)
	}

	if strings.Contains(urlRaw, "/station/") {
		fmt.Println("Station")
		storefront, id := checkUrlStation(urlRaw)
		if storefront == "" || id == "" {
			return fmt.Errorf("invalid station URL format")
		}
		if len(mediaUserToken) <= 50 {
			return fmt.Errorf("media-user-token is required for station download")
		}
		return ripStationContext(ctx, id, token, storefront, mediaUserToken)
	}

	return fmt.Errorf("unsupported URL type. Supported: album, playlist, song, music-video, station")
}

func setJobQualityFlags(quality string) {
	if aac_type == nil {
		configuredAACType := Config.AacType
		aac_type = &configuredAACType
	}
	if quality == QualityAACLC {
		Config.AacType = QualityAACLC
	}
	setQualityFlags(quality)
}

// prepareMVMediaOutput gives music-video downloads the same existing-file
// policy and atomic publication boundary as audio tracks.  The muxer writes
// only to WorkPath; FinalPath remains user-owned until Commit succeeds.
func prepareMVMediaOutput(finalPath string) (mediaOutput, error) {
	return prepareLegacyTrackMediaOutput(finalPath)
}

func prepareMVMediaOutputContext(ctx context.Context, finalPath string) (mediaOutput, error) {
	return prepareLegacyTrackMediaOutputContext(ctx, finalPath)
}

func mvDownloader(adamID string, saveDir string, token string, storefront string, mediaUserToken string, track *task.Track) error {
	return mvDownloaderContext(context.Background(), adamID, saveDir, token, storefront, mediaUserToken, track)
}

func mvDownloaderContext(ctx context.Context, adamID string, saveDir string, token string, storefront string, mediaUserToken string, track *task.Track) (result error) {
	ctx = networkContext(ctx)
	var finalPath string
	skipped := false
	if track == nil {
		emitLegacyJobItem(JobItemInput{SourceID: adamID, Status: JobItemStatusRunning})
		defer func() {
			delta := structs.Counter{Total: 1}
			item := JobItemInput{SourceID: adamID, Status: JobItemStatusSucceeded, RelativePath: legacyTrackRelativePath(finalPath)}
			if result != nil {
				delta.Error = 1
				item.Status = JobItemStatusFailed
				item.RelativePath = ""
				item.ErrorCode, item.ErrorMessage = downloadItemDiagnostic("media", result)
			} else {
				delta.Success = 1
				if skipped {
					delta.Skipped = 1
					item.Status = JobItemStatusSkipped
				}
			}
			addLegacyCounter(delta)
			emitLegacyJobItem(item)
		}()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	MVInfo, err := ampapi.GetMusicVideoRespContext(ctx, storefront, adamID, Config.Language, token)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fmt.Println("\u26A0 Failed to get MV manifest:", err)
		return err
	}

	if strings.HasSuffix(saveDir, ".") {
		saveDir = strings.ReplaceAll(saveDir, ".", "")
	}
	saveDir = strings.TrimSpace(saveDir)

	vidPath := filepath.Join(saveDir, fmt.Sprintf("%s_vid.mp4", adamID))
	audPath := filepath.Join(saveDir, fmt.Sprintf("%s_aud.mp4", adamID))
	mvSaveName := fmt.Sprintf("%s (%s)", MVInfo.Data[0].Attributes.Name, adamID)
	if track != nil {
		mvSaveName = fmt.Sprintf("%02d. %s", track.TaskNum, MVInfo.Data[0].Attributes.Name)
	}

	mvOutPath := filepath.Join(saveDir, fmt.Sprintf("%s.mp4", forbiddenNames.ReplaceAllString(mvSaveName, "_")))

	fmt.Println(MVInfo.Data[0].Attributes.Name)

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(saveDir, os.ModePerm); err != nil {
		return fmt.Errorf("create MV output directory: %w", err)
	}
	output, err := prepareMVMediaOutputContext(ctx, mvOutPath)
	if err != nil {
		return fmt.Errorf("existing MV cannot be used: %w", err)
	}
	if output.Skipped {
		fmt.Println("MV already exists locally, skipped.")
		skipped = true
		finalPath = mvOutPath
		if track != nil {
			track.SavePath = mvOutPath
		}
		return nil
	}
	defer output.Cleanup()
	defer os.Remove(vidPath)
	defer os.Remove(audPath)

	mvm3u8url, _, err := runv3.GetWebplaybackContext(ctx, adamID, token, mediaUserToken, true)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("get music video playback: %w", err)
	}
	if mvm3u8url == "" {
		return errors.New("media-user-token may wrong or expired")
	}

	videom3u8url, err := extractVideoContext(ctx, mvm3u8url)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("extract music video stream: %w", err)
	}
	videokeyAndUrls, err := runv3.RunContext(ctx, adamID, videom3u8url, token, mediaUserToken, true)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("download music video stream: %w", err)
	}
	if err := runv3.ExtMvDataContext(ctx, videokeyAndUrls, vidPath); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("decrypt music video stream: %w", err)
	}
	audiom3u8url, err := extractMvAudioContext(ctx, mvm3u8url)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("extract music video audio stream: %w", err)
	}
	audiokeyAndUrls, err := runv3.RunContext(ctx, adamID, audiom3u8url, token, mediaUserToken, true)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("download music video audio stream: %w", err)
	}
	if err := runv3.ExtMvDataContext(ctx, audiokeyAndUrls, audPath); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("decrypt music video audio stream: %w", err)
	}

	genre := firstGenre(MVInfo.Data[0].Attributes.GenreNames)
	tags := []string{
		"tool=",
		fmt.Sprintf("artist=%s", MVInfo.Data[0].Attributes.ArtistName),
		fmt.Sprintf("title=%s", MVInfo.Data[0].Attributes.Name),
		fmt.Sprintf("genre=%s", genre),
		fmt.Sprintf("created=%s", MVInfo.Data[0].Attributes.ReleaseDate),
		fmt.Sprintf("ISRC=%s", MVInfo.Data[0].Attributes.Isrc),
	}

	if MVInfo.Data[0].Attributes.ContentRating == "explicit" {
		tags = append(tags, "rating=1")
	} else if MVInfo.Data[0].Attributes.ContentRating == "clean" {
		tags = append(tags, "rating=2")
	} else {
		tags = append(tags, "rating=0")
	}

	if track != nil {
		if track.PreType == "playlists" && !Config.UseSongInfoForPlaylist {
			tags = append(tags, "disk=1/1")
			tags = append(tags, fmt.Sprintf("album=%s", track.PlaylistData.Attributes.Name))
			tags = append(tags, fmt.Sprintf("track=%d", track.TaskNum))
			tags = append(tags, fmt.Sprintf("tracknum=%d/%d", track.TaskNum, track.TaskTotal))
			tags = append(tags, fmt.Sprintf("album_artist=%s", track.PlaylistData.Attributes.ArtistName))
			tags = append(tags, fmt.Sprintf("performer=%s", track.Resp.Attributes.ArtistName))
		} else if track.PreType == "playlists" && Config.UseSongInfoForPlaylist {
			tags = append(tags, fmt.Sprintf("album=%s", track.AlbumData.Attributes.Name))
			tags = append(tags, fmt.Sprintf("disk=%d/%d", track.Resp.Attributes.DiscNumber, track.DiscTotal))
			tags = append(tags, fmt.Sprintf("track=%d", track.Resp.Attributes.TrackNumber))
			tags = append(tags, fmt.Sprintf("tracknum=%d/%d", track.Resp.Attributes.TrackNumber, track.AlbumData.Attributes.TrackCount))
			tags = append(tags, fmt.Sprintf("album_artist=%s", track.AlbumData.Attributes.ArtistName))
			tags = append(tags, fmt.Sprintf("performer=%s", track.Resp.Attributes.ArtistName))
			tags = append(tags, fmt.Sprintf("copyright=%s", track.AlbumData.Attributes.Copyright))
			tags = append(tags, fmt.Sprintf("UPC=%s", track.AlbumData.Attributes.Upc))
		} else {
			tags = append(tags, fmt.Sprintf("album=%s", track.AlbumData.Attributes.Name))
			tags = append(tags, fmt.Sprintf("disk=%d/%d", track.Resp.Attributes.DiscNumber, track.DiscTotal))
			tags = append(tags, fmt.Sprintf("track=%d", track.Resp.Attributes.TrackNumber))
			tags = append(tags, fmt.Sprintf("tracknum=%d/%d", track.Resp.Attributes.TrackNumber, track.AlbumData.Attributes.TrackCount))
			tags = append(tags, fmt.Sprintf("album_artist=%s", track.AlbumData.Attributes.ArtistName))
			tags = append(tags, fmt.Sprintf("performer=%s", track.Resp.Attributes.ArtistName))
			tags = append(tags, fmt.Sprintf("copyright=%s", track.AlbumData.Attributes.Copyright))
			tags = append(tags, fmt.Sprintf("UPC=%s", track.AlbumData.Attributes.Upc))
		}
	} else {
		tags = append(tags, fmt.Sprintf("album=%s", MVInfo.Data[0].Attributes.AlbumName))
		tags = append(tags, fmt.Sprintf("disk=%d", MVInfo.Data[0].Attributes.DiscNumber))
		tags = append(tags, fmt.Sprintf("track=%d", MVInfo.Data[0].Attributes.TrackNumber))
		tags = append(tags, fmt.Sprintf("tracknum=%d", MVInfo.Data[0].Attributes.TrackNumber))
		tags = append(tags, fmt.Sprintf("performer=%s", MVInfo.Data[0].Attributes.ArtistName))
	}

	var covPath string
	if true {
		thumbURL := MVInfo.Data[0].Attributes.Artwork.URL
		baseThumbName := forbiddenNames.ReplaceAllString(mvSaveName, "_") + "_thumbnail"
		covPath, err = writeCoverWithoutJobOutputContext(ctx, saveDir, baseThumbName, thumbURL)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println("Failed to save MV thumbnail:", err)
		} else {
			defer os.Remove(covPath)
			tags = append(tags, fmt.Sprintf("cover=%s", covPath))
		}
	}

	tagsString := strings.Join(tags, ":")
	muxCmd := exec.CommandContext(ctx, "MP4Box", "-itags", tagsString, "-quiet", "-add", vidPath, "-add", audPath, "-keep-utc", "-new", output.WorkPath)
	fmt.Printf("MV Remuxing...")
	if err := muxCmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fmt.Printf("MV mux failed: %v\n", err)
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := output.Commit(); err != nil {
		fmt.Printf("MV publish failed: %v\n", err)
		return err
	}
	if track != nil {
		track.SavePath = mvOutPath
	}
	emitLegacyJobOutputPath(mvOutPath)
	finalPath = mvOutPath
	fmt.Printf("\rMV Remuxed.   \n")

	return nil
}

func extractMvAudio(c string) (string, error) {
	return extractMvAudioContext(context.Background(), c)
}

func extractMvAudioContext(ctx context.Context, c string) (string, error) {
	MediaUrl, err := url.Parse(c)
	if err != nil {
		return "", err
	}

	body, err := readNetworkResponse(ctx, c, maxManifestResponseBytes, nil)
	if err != nil {
		return "", err
	}

	audioString := string(body)
	from, listType, err := m3u8.DecodeFrom(strings.NewReader(audioString), true)
	if err != nil || listType != m3u8.MASTER {
		return "", errors.New("m3u8 not of media type")
	}

	audio := from.(*m3u8.MasterPlaylist)

	var audioPriority = []string{"audio-atmos", "audio-ac3", "audio-stereo-256"}
	if Config.MVAudioType == "ac3" {
		audioPriority = []string{"audio-ac3", "audio-stereo-256"}
	} else if Config.MVAudioType == "aac" {
		audioPriority = []string{"audio-stereo-256"}
	}

	re := regexp.MustCompile(`_gr(\d+)_`)

	type AudioStream struct {
		URL     string
		Rank    int
		GroupID string
	}
	var audioStreams []AudioStream

	for _, variant := range audio.Variants {
		for _, audiov := range variant.Alternatives {
			if audiov.URI != "" {
				for _, priority := range audioPriority {
					if audiov.GroupId == priority {
						matches := re.FindStringSubmatch(audiov.URI)
						if len(matches) == 2 {
							var rank int
							fmt.Sscanf(matches[1], "%d", &rank)
							streamUrl, _ := MediaUrl.Parse(audiov.URI)
							audioStreams = append(audioStreams, AudioStream{
								URL:     streamUrl.String(),
								Rank:    rank,
								GroupID: audiov.GroupId,
							})
						}
					}
				}
			}
		}
	}

	if len(audioStreams) == 0 {
		return "", errors.New("no suitable audio stream found")
	}

	sort.Slice(audioStreams, func(i, j int) bool {
		return audioStreams[i].Rank > audioStreams[j].Rank
	})
	fmt.Println("Audio: " + audioStreams[0].GroupID)
	return audioStreams[0].URL, nil
}

func checkM3u8(b string, f string) (string, error) {
	return checkM3u8Context(context.Background(), b, f)
}

func checkM3u8Context(ctx context.Context, b string, f string) (string, error) {
	if nativeDownloadClient != nil {
		return nativeDownloadClient.M3U8(ctx, b)
	}
	var EnhancedHls string
	if Config.GetM3u8FromDevice {
		ctx = networkContext(ctx)
		adamID := b
		dialer := net.Dialer{Timeout: legacyDeviceTimeout}
		conn, err := dialer.DialContext(ctx, "tcp", Config.GetM3u8Port)
		if err != nil {
			fmt.Println("Error connecting to device:", err)
			return "none", networkContextError(ctx, err)
		}
		defer conn.Close()
		deadline := time.Now().Add(legacyDeviceTimeout)
		if err := conn.SetDeadline(deadline); err != nil {
			return "none", err
		}
		stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stopCancel()
		if f == "song" {
			fmt.Println("Connected to device")
		}

		adamIDBuffer := []byte(adamID)
		lengthBuffer := []byte{byte(len(adamIDBuffer))}

		_, err = conn.Write(lengthBuffer)
		if err != nil {
			fmt.Println("Error writing length to device:", err)
			return "none", networkContextError(ctx, err)
		}

		_, err = conn.Write(adamIDBuffer)
		if err != nil {
			fmt.Println("Error writing adamID to device:", err)
			return "none", networkContextError(ctx, err)
		}

		response, err := bufio.NewReader(io.LimitReader(conn, maxDeviceResponseBytes+1)).ReadBytes('\n')
		if int64(len(response)) > maxDeviceResponseBytes {
			return "none", responseTooLarge(int64(len(response)), maxDeviceResponseBytes)
		}
		if err != nil {
			fmt.Println("Error reading response from device:", err)
			return "none", networkContextError(ctx, err)
		}

		response = bytes.TrimSpace(response)
		if len(response) > 0 {
			if f == "song" {
				fmt.Println("Received device manifest URL")
			}
			EnhancedHls = string(response)
		} else {
			fmt.Println("Received an empty response")
		}
	}
	return EnhancedHls, nil
}

func formatAvailability(available bool, quality string) string {
	if !available {
		return "Not Available"
	}
	return quality
}

func extractMedia(b string, more_mode bool) (string, string, error) {
	return extractMediaContext(context.Background(), b, more_mode)
}

func extractMediaContext(ctx context.Context, b string, more_mode bool) (string, string, error) {
	masterUrl, err := url.Parse(b)
	if err != nil {
		return "", "", err
	}
	body, err := readNetworkResponse(ctx, b, maxManifestResponseBytes, nil)
	if err != nil {
		return "", "", err
	}
	masterString := string(body)
	from, listType, err := m3u8.DecodeFrom(strings.NewReader(masterString), true)
	if err != nil || listType != m3u8.MASTER {
		return "", "", errors.New("m3u8 not of master type")
	}
	master := from.(*m3u8.MasterPlaylist)
	var streamUrl *url.URL
	fmt.Println("M3U8 variants:")
	for _, v := range master.Variants {
		fmt.Printf("  Codec=%s Audio=%s BW=%d\n", v.Codecs, v.Audio, v.Bandwidth)
	}
	sort.Slice(master.Variants, func(i, j int) bool {
		return master.Variants[i].AverageBandwidth > master.Variants[j].AverageBandwidth
	})
	if debug_mode && more_mode {
		fmt.Println("\nDebug: All Available Variants:")
		var data [][]string
		for _, variant := range master.Variants {
			data = append(data, []string{variant.Codecs, variant.Audio, fmt.Sprint(variant.Bandwidth)})
		}
		table := tablewriter.NewWriter(os.Stdout)
		table.SetHeader([]string{"Codec", "Audio", "Bandwidth"})
		table.SetAutoMergeCells(true)
		table.SetRowLine(true)
		table.AppendBulk(data)
		table.Render()

		var hasAAC, hasLossless, hasHiRes, hasAtmos, hasDolbyAudio bool
		var aacQuality, losslessQuality, hiResQuality, atmosQuality, dolbyAudioQuality string

		for _, variant := range master.Variants {
			if variant.Codecs == "mp4a.40.2" { // AAC
				hasAAC = true
				split := strings.Split(variant.Audio, "-")
				if len(split) >= 3 {
					bitrate, _ := strconv.Atoi(split[2])
					currentBitrate := 0
					if aacQuality != "" {
						current := strings.Split(aacQuality, " | ")[2]
						current = strings.Split(current, " ")[0]
						currentBitrate, _ = strconv.Atoi(current)
					}
					if bitrate > currentBitrate {
						aacQuality = fmt.Sprintf("AAC | 2 Channel | %d Kbps", bitrate)
					}
				}
			} else if variant.Codecs == "ec-3" && strings.Contains(variant.Audio, "atmos") { // Dolby Atmos
				hasAtmos = true
				split := strings.Split(variant.Audio, "-")
				if len(split) > 0 {
					bitrateStr := split[len(split)-1]
					if len(bitrateStr) == 4 && bitrateStr[0] == '2' {
						bitrateStr = bitrateStr[1:]
					}
					bitrate, _ := strconv.Atoi(bitrateStr)
					currentBitrate := 0
					if atmosQuality != "" {
						current := strings.Split(strings.Split(atmosQuality, " | ")[2], " ")[0]
						currentBitrate, _ = strconv.Atoi(current)
					}
					if bitrate > currentBitrate {
						atmosQuality = fmt.Sprintf("E-AC-3 | 16 Channel | %d Kbps", bitrate)
					}
				}
			} else if variant.Codecs == "alac" { // ALAC (Lossless or Hi-Res)
				split := strings.Split(variant.Audio, "-")
				if len(split) >= 3 {
					bitDepth := split[len(split)-1]
					sampleRate := split[len(split)-2]
					sampleRateInt, _ := strconv.Atoi(sampleRate)
					if sampleRateInt > 48000 { // Hi-Res
						hasHiRes = true
						hiResQuality = fmt.Sprintf("ALAC | 2 Channel | %s-bit/%d kHz", bitDepth, sampleRateInt/1000)
					} else { // Standard Lossless
						hasLossless = true
						losslessQuality = fmt.Sprintf("ALAC | 2 Channel | %s-bit/%d kHz", bitDepth, sampleRateInt/1000)
					}
				}
			} else if variant.Codecs == "ac-3" { // Dolby Audio
				hasDolbyAudio = true
				split := strings.Split(variant.Audio, "-")
				if len(split) > 0 {
					bitrate, _ := strconv.Atoi(split[len(split)-1])
					dolbyAudioQuality = fmt.Sprintf("AC-3 |  16 Channel | %d Kbps", bitrate)
				}
			}
		}

		fmt.Println("Available Audio Formats:")
		fmt.Println("------------------------")
		fmt.Printf("AAC             : %s\n", formatAvailability(hasAAC, aacQuality))
		fmt.Printf("Lossless        : %s\n", formatAvailability(hasLossless, losslessQuality))
		fmt.Printf("Hi-Res Lossless : %s\n", formatAvailability(hasHiRes, hiResQuality))
		fmt.Printf("Dolby Atmos     : %s\n", formatAvailability(hasAtmos, atmosQuality))
		fmt.Printf("Dolby Audio     : %s\n", formatAvailability(hasDolbyAudio, dolbyAudioQuality))
		fmt.Println("------------------------")

		return "", "", nil
	}
	var Quality string
	for _, variant := range master.Variants {
		if dl_atmos {
			if variant.Codecs == "ec-3" && strings.Contains(variant.Audio, "atmos") {
				if debug_mode && !more_mode {
					fmt.Printf("Debug: Found Dolby Atmos variant - %s (Bitrate: %d Kbps)\n",
						variant.Audio, variant.Bandwidth/1000)
				}
				split := strings.Split(variant.Audio, "-")
				length := len(split)
				length_int, err := strconv.Atoi(split[length-1])
				if err != nil {
					return "", "", err
				}
				if length_int <= Config.AtmosMax {
					if !debug_mode && !more_mode {
						fmt.Printf("%s\n", variant.Audio)
					}
					streamUrlTemp, err := masterUrl.Parse(variant.URI)
					if err != nil {
						return "", "", err
					}
					streamUrl = streamUrlTemp
					Quality = fmt.Sprintf("%s Kbps", split[len(split)-1])
					break
				}
			} else if variant.Codecs == "ac-3" { // Add Dolby Audio support
				if debug_mode && !more_mode {
					fmt.Printf("Debug: Found Dolby Audio variant - %s (Bitrate: %d Kbps)\n",
						variant.Audio, variant.Bandwidth/1000)
				}
				streamUrlTemp, err := masterUrl.Parse(variant.URI)
				if err != nil {
					return "", "", err
				}
				streamUrl = streamUrlTemp
				split := strings.Split(variant.Audio, "-")
				Quality = fmt.Sprintf("%s Kbps", split[len(split)-1])
				break
			}
		} else if dl_aac {
			if variant.Codecs == "mp4a.40.2" {
				if debug_mode && !more_mode {
					fmt.Printf("Debug: Found AAC variant - %s (Bitrate: %d)\n", variant.Audio, variant.Bandwidth)
				}
				aacregex := regexp.MustCompile(`audio-stereo-\d+`)
				replaced := aacregex.ReplaceAllString(variant.Audio, "aac")
				if replaced == Config.AacType {
					if !debug_mode && !more_mode {
						fmt.Printf("%s\n", variant.Audio)
					}
					streamUrlTemp, err := masterUrl.Parse(variant.URI)
					if err != nil {
						panic(err)
					}
					streamUrl = streamUrlTemp
					split := strings.Split(variant.Audio, "-")
					Quality = fmt.Sprintf("%s Kbps", split[2])
					break
				}
			}
		} else {
			if variant.Codecs == "alac" {
				split := strings.Split(variant.Audio, "-")
				length := len(split)
				length_int, err := strconv.Atoi(split[length-2])
				if err != nil {
					return "", "", err
				}
				if length_int <= Config.AlacMax {
					if !debug_mode && !more_mode {
						fmt.Printf("%s-bit / %s Hz\n", split[length-1], split[length-2])
					}
					streamUrlTemp, err := masterUrl.Parse(variant.URI)
					if err != nil {
						panic(err)
					}
					streamUrl = streamUrlTemp
					KHZ := float64(length_int) / 1000.0
					Quality = fmt.Sprintf("%sB-%.1fkHz", split[length-1], KHZ)
					break
				}
			}
		}
	}
	if streamUrl == nil {
		return "", "", errors.New("no codec found")
	}
	return streamUrl.String(), Quality, nil
}
func extractVideo(c string) (string, error) {
	return extractVideoContext(context.Background(), c)
}

func extractVideoContext(ctx context.Context, c string) (string, error) {
	MediaUrl, err := url.Parse(c)
	if err != nil {
		return "", err
	}

	body, err := readNetworkResponse(ctx, c, maxManifestResponseBytes, nil)
	if err != nil {
		return "", err
	}
	videoString := string(body)

	from, listType, err := m3u8.DecodeFrom(strings.NewReader(videoString), true)
	if err != nil || listType != m3u8.MASTER {
		return "", errors.New("m3u8 not of media type")
	}

	video := from.(*m3u8.MasterPlaylist)

	re := regexp.MustCompile(`_(\d+)x(\d+)`)

	var streamUrl *url.URL
	sort.Slice(video.Variants, func(i, j int) bool {
		return video.Variants[i].AverageBandwidth > video.Variants[j].AverageBandwidth
	})

	maxHeight := Config.MVMax

	for _, variant := range video.Variants {
		matches := re.FindStringSubmatch(variant.URI)
		if len(matches) == 3 {
			height := matches[2]
			var h int
			_, err := fmt.Sscanf(height, "%d", &h)
			if err != nil {
				continue
			}
			if h <= maxHeight {
				streamUrl, err = MediaUrl.Parse(variant.URI)
				if err != nil {
					return "", err
				}
				fmt.Println("Video: " + variant.Resolution + "-" + variant.VideoRange)
				break
			}
		}
	}

	if streamUrl == nil {
		return "", errors.New("no suitable video stream found")
	}

	return streamUrl.String(), nil
}

func ripSong(songId string, token string, storefront string, mediaUserToken string) error {
	return ripSongContext(context.Background(), songId, token, storefront, mediaUserToken)
}

func ripSongContext(ctx context.Context, songId string, token string, storefront string, mediaUserToken string) error {
	ctx = networkContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	// Get song info to find album ID
	manifest, err := ampapi.GetSongRespContext(ctx, storefront, songId, Config.Language, token)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fmt.Println("Failed to get song response.")
		return err
	}

	songData := manifest.Data[0]
	albumId := songData.Relationships.Albums.Data[0].ID

	// Use album approach but only download the specific song
	dl_song = true
	err = ripAlbumContext(ctx, albumId, token, storefront, mediaUserToken, songId)
	if err != nil {
		fmt.Println("Failed to rip song:", err)
		return err
	}

	return nil
}
