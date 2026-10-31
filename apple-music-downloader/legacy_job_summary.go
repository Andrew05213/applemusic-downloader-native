package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Andrew05213/applemusic_downloader/utils/structs"
)

const legacyJobSummaryPrefix = "AMDL_JOB_SUMMARY "

// legacyJobItemPrefix is an internal, line-delimited child-process protocol.
// It is never forwarded as a public log message: processJobExecutor validates
// and converts it to an EventTypeItem before it reaches SQLite or SSE.
const legacyJobItemPrefix = "__AMDL_JOB_ITEM__:"

// legacyJobOutputPrefix is the child-to-parent protocol for one finalized
// output. Unlike a diff of the shared download directory, this identifies
// only an artifact the isolated downloader deliberately published.
const legacyJobOutputPrefix = "__AMDL_JOB_OUTPUT__:"

var legacyProtocolOutputMu sync.Mutex

type legacyJobSummary struct {
	Total       int `json:"total"`
	Succeeded   int `json:"succeeded"`
	Failed      int `json:"failed"`
	Skipped     int `json:"skipped"`
	Unavailable int `json:"unavailable"`
}

func legacySummaryFromCounter(counter structs.Counter) legacyJobSummary {
	succeeded := counter.Success - counter.Skipped
	if succeeded < 0 {
		succeeded = 0
	}
	return legacyJobSummary{
		Total:       counter.Total,
		Succeeded:   succeeded,
		Failed:      counter.Error,
		Skipped:     counter.Skipped,
		Unavailable: counter.Unavailable + counter.NotSong,
	}
}

func encodeLegacyJobSummary(counter structs.Counter) string {
	payload, err := json.Marshal(legacySummaryFromCounter(counter))
	if err != nil {
		return legacyJobSummaryPrefix + `{"total":0,"failed":1}`
	}
	return legacyJobSummaryPrefix + string(payload)
}

func parseLegacyJobSummary(message string) (legacyJobSummary, bool) {
	if !strings.HasPrefix(message, legacyJobSummaryPrefix) {
		return legacyJobSummary{}, false
	}
	var summary legacyJobSummary
	if err := json.Unmarshal([]byte(strings.TrimPrefix(message, legacyJobSummaryPrefix)), &summary); err != nil {
		return legacyJobSummary{}, false
	}
	if summary.Total < 0 || summary.Succeeded < 0 || summary.Failed < 0 || summary.Skipped < 0 || summary.Unavailable < 0 {
		return legacyJobSummary{}, false
	}
	return summary, true
}

func legacySummaryFromEvent(data any) (legacyJobSummary, bool) {
	values, ok := data.(map[string]string)
	if !ok {
		return legacyJobSummary{}, false
	}
	return parseLegacyJobSummary(values["message"])
}

func encodeLegacyJobItem(input JobItemInput) string {
	normalized, err := normalizeJobItemInput(input)
	if err != nil {
		return ""
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	return legacyJobItemPrefix + string(payload)
}

func parseLegacyJobItem(message string) (JobItemInput, bool) {
	if !strings.HasPrefix(message, legacyJobItemPrefix) {
		return JobItemInput{}, false
	}
	var item JobItemInput
	if err := json.Unmarshal([]byte(strings.TrimPrefix(message, legacyJobItemPrefix)), &item); err != nil {
		return JobItemInput{}, false
	}
	normalized, err := normalizeJobItemInput(item)
	if err != nil {
		return JobItemInput{}, false
	}
	return normalized, true
}

func legacyJobItemFromEvent(data any) (JobItemInput, bool) {
	values, ok := data.(map[string]string)
	if !ok {
		return JobItemInput{}, false
	}
	return parseLegacyJobItem(values["message"])
}

type legacyJobOutput struct {
	RelativePath string `json:"relativePath"`
	Name         string `json:"name,omitempty"`
	Size         int64  `json:"size,omitempty"`
	ContentType  string `json:"contentType,omitempty"`
}

func encodeLegacyJobOutput(input JobFileInput) string {
	if err := validateRelativePath(input.RelativePath); err != nil || input.Size < 0 {
		return ""
	}
	payload, err := json.Marshal(legacyJobOutput{
		RelativePath: filepath.ToSlash(input.RelativePath),
		Name:         input.Name,
		Size:         input.Size,
		ContentType:  input.ContentType,
	})
	if err != nil {
		return ""
	}
	return legacyJobOutputPrefix + string(payload)
}

func parseLegacyJobOutput(message string) (JobFileInput, bool) {
	if !strings.HasPrefix(message, legacyJobOutputPrefix) {
		return JobFileInput{}, false
	}
	var decoded legacyJobOutput
	if err := json.Unmarshal([]byte(strings.TrimPrefix(message, legacyJobOutputPrefix)), &decoded); err != nil {
		return JobFileInput{}, false
	}
	input := JobFileInput{
		RelativePath: filepath.ToSlash(decoded.RelativePath),
		Name:         decoded.Name,
		Size:         decoded.Size,
		ContentType:  decoded.ContentType,
	}
	if err := validateRelativePath(input.RelativePath); err != nil || input.Size < 0 {
		return JobFileInput{}, false
	}
	if input.Name == "" {
		input.Name = filepath.Base(input.RelativePath)
	}
	return input, true
}

func legacyJobOutputFromEvent(data any) (JobFileInput, bool) {
	values, ok := data.(map[string]string)
	if !ok {
		return JobFileInput{}, false
	}
	return parseLegacyJobOutput(values["message"])
}

// emitLegacyJobItem is deliberately active only in the isolated child. This
// keeps direct legacy compatibility calls quiet while giving the durable queue
// an authenticated, credential-free item progress channel.
func emitLegacyJobItem(input JobItemInput) {
	if os.Getenv("AMDL_JOB_CHILD") != "1" {
		return
	}
	if line := encodeLegacyJobItem(input); line != "" {
		_ = writeLegacyJobProtocolLine(os.Stdout, line)
	}
}

// emitLegacyJobOutput is deliberately available only inside the isolated
// child. The parent validates and persists this message before forwarding it
// as a durable file event, so an unrelated media file in the shared root can
// never acquire job ownership merely because it changed while a job ran.
func emitLegacyJobOutput(input JobFileInput) {
	if os.Getenv("AMDL_JOB_CHILD") != "1" {
		return
	}
	if line := encodeLegacyJobOutput(input); line != "" {
		_ = writeLegacyJobProtocolLine(os.Stdout, line)
	}
}
