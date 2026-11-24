// Package processing defines the anonymous, bounded summary returned on explicit diagnostic export.
package processing

const Limit = 64

type Totals struct {
	Requests  uint64 `json:"requests"`
	Bytes     uint64 `json:"bytes"`
	Completed uint64 `json:"completed_bytes"`
	Wait      uint64 `json:"wait_ns"`
	Create    uint64 `json:"create_ns"`
	Copy      uint64 `json:"copy_ns"`
	SDK       uint64 `json:"sdk_ns"`
	Held      uint64 `json:"held_ns"`
	Rate      uint64 `json:"bytes_per_second"`
}
type Summary struct {
	End   string `json:"end"`
	First Totals `json:"first"`
	Reuse Totals `json:"reuse"`
}
type Snapshot struct {
	Seen    uint64    `json:"seen"`
	Streams []Summary `json:"streams"`
}
