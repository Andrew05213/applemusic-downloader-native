package nativeauth

// UI-only, process-scoped values. Never include these in Snapshot, Diagnostics,
// journals, caches or errors. IDs are decimal strings to preserve uint64 in JS.
import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxInteractionBytes = 256 * 1024

type InteractionButton struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Role        int    `json:"role"`
	Enabled     bool   `json:"enabled"`
	Unsupported int    `json:"unsupported"`
}
type InteractionField struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Type       int    `json:"type"`
	Required   bool   `json:"required"`
	MinBytes   int    `json:"minBytes"`
	MaxBytes   int    `json:"maxBytes"`
	DigitsOnly bool   `json:"digitsOnly"`
}
type Interaction struct {
	ProtocolVersion int                 `json:"protocolVersion"`
	Scope           string              `json:"scope,omitempty"`
	AttemptID       string              `json:"attemptId"`
	RequestID       string              `json:"requestId"`
	ExpiresAfterMs  int                 `json:"expiresAfterMs"`
	Kind            int                 `json:"kind"`
	Unsupported     int                 `json:"unsupported"`
	TextTruncated   bool                `json:"textTruncated"`
	Title           string              `json:"title"`
	Message         string              `json:"message"`
	Buttons         []InteractionButton `json:"buttons"`
	Fields          []InteractionField  `json:"fields"`
}
type InteractionValue struct {
	FieldID string `json:"fieldId"`
	Value   string `json:"value"`
}
type InteractionAnswer struct {
	ProtocolVersion int                `json:"protocolVersion"`
	Scope           string             `json:"scope,omitempty"`
	AttemptID       string             `json:"attemptId"`
	RequestID       string             `json:"requestId"`
	Disposition     int                `json:"disposition"`
	ButtonID        string             `json:"buttonId,omitempty"`
	Values          []InteractionValue `json:"values"`
}

func interactionID(s string) bool {
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && n != 0 && strconv.FormatUint(n, 10) == s
}
func interactionText(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func ValidInteraction(q *Interaction) bool {
	if q == nil {
		return true
	}
	if q.ProtocolVersion != 1 || !interactionID(q.AttemptID) || !interactionID(q.RequestID) || q.Kind < 1 || q.Kind > 6 || q.Unsupported < 0 || q.Unsupported > 5 || q.ExpiresAfterMs < 1 || q.ExpiresAfterMs > 300000 || !interactionText(q.Title, 1024) || !interactionText(q.Message, 8192) || len(q.Buttons) > 16 || len(q.Fields) > 16 {
		return false
	}
	total := len(q.Title) + len(q.Message)
	seen := map[string]bool{}
	for _, b := range q.Buttons {
		if !interactionID(b.ID) || seen[b.ID] || !interactionText(b.Label, 512) || b.Role < 1 || b.Role > 4 || b.Unsupported < 0 || b.Unsupported > 5 {
			return false
		}
		seen[b.ID] = true
		total += len(b.Label)
	}
	seen = map[string]bool{}
	for _, f := range q.Fields {
		if !interactionID(f.ID) || seen[f.ID] || !interactionText(f.Label, 512) || f.Type < 1 || f.Type > 7 || f.MinBytes < 0 || f.MaxBytes < f.MinBytes || f.MaxBytes > 4096 {
			return false
		}
		seen[f.ID] = true
		total += len(f.Label)
	}
	return total <= 24576
}
func ValidInteractionAnswer(q *Interaction, a InteractionAnswer) bool {
	if q == nil || a.ProtocolVersion != 1 || a.Scope != q.Scope || a.AttemptID != q.AttemptID || a.RequestID != q.RequestID || len(a.Values) > 16 {
		return false
	}
	if a.Disposition == 2 || a.Disposition == 3 {
		return a.ButtonID == "" && len(a.Values) == 0
	}
	if a.Disposition != 1 || q.Kind == 6 || q.Unsupported != 0 {
		return false
	}
	var button *InteractionButton
	for i := range q.Buttons {
		b := &q.Buttons[i]
		if b.ID == a.ButtonID && b.Enabled && b.Unsupported == 0 {
			button = b
		}
	}
	if button == nil {
		return false
	}
	if button.Role == 3 {
		return len(a.Values) == 0
	}
	seen := map[string]bool{}
	total := 0
	for _, v := range a.Values {
		var field *InteractionField
		for i := range q.Fields {
			if q.Fields[i].ID == v.FieldID {
				field = &q.Fields[i]
			}
		}
		if field == nil || seen[v.FieldID] || field.Type == 6 || !interactionText(v.Value, field.MaxBytes) || len(v.Value) < field.MinBytes || field.Required && len(v.Value) == 0 {
			return false
		}
		if field.DigitsOnly && strings.Trim(v.Value, "0123456789") != "" {
			return false
		}
		seen[v.FieldID] = true
		total += len(v.Value)
	}
	for _, f := range q.Fields {
		if f.Required && !seen[f.ID] {
			return false
		}
	}
	return total <= 16384
}
func DecodeInteraction(raw string) (*Interaction, error) {
	var q Interaction
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > MaxInteractionBytes || d.Decode(&q) != nil || d.Decode(new(any)) != io.EOF || q.Scope != "" || !ValidInteraction(&q) {
		return nil, ErrInvalidInput
	}
	return &q, nil
}

type interactionKey struct{}
type InteractionHandler func(context.Context, *Interaction) (InteractionAnswer, error)

func WithInteractionHandler(ctx context.Context, fn InteractionHandler) context.Context {
	return context.WithValue(ctx, interactionKey{}, fn)
}
func RequestInteraction(ctx context.Context, q *Interaction) (InteractionAnswer, error) {
	fn, _ := ctx.Value(interactionKey{}).(InteractionHandler)
	if fn == nil {
		return InteractionAnswer{}, ErrInvalidInput
	}
	return fn(ctx, q)
}

type pendingInteraction struct {
	request  Interaction
	deadline time.Time
	answer   chan InteractionAnswer
}

func (m *Manager) awaitInteraction(ctx context.Context, generation uint64, q *Interaction) (InteractionAnswer, error) {
	if q == nil || !ValidInteraction(q) {
		return InteractionAnswer{}, ErrInvalidInput
	}
	m.mu.Lock()
	if generation != m.generation || !m.busy || m.pendingInteraction != nil {
		m.mu.Unlock()
		return InteractionAnswer{}, ErrCancelled
	}
	copy := *q
	copy.Buttons = append([]InteractionButton{}, q.Buttons...)
	copy.Fields = append([]InteractionField{}, q.Fields...)
	copy.Scope = m.interactionScope
	pending := &pendingInteraction{request: copy, deadline: time.Now().Add(time.Duration(q.ExpiresAfterMs) * time.Millisecond), answer: make(chan InteractionAnswer, 1)}
	m.pendingInteraction = pending
	m.state = Snapshot{State: "needs_interaction"}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.pendingInteraction == pending {
			m.pendingInteraction = nil
		}
		m.mu.Unlock()
	}()
	timer := time.NewTimer(time.Until(pending.deadline))
	defer timer.Stop()
	select {
	case a := <-pending.answer:
		return a, nil
	case <-ctx.Done():
		return InteractionAnswer{}, ErrCancelled
	case <-timer.C:
		return InteractionAnswer{}, context.DeadlineExceeded
	}
}
func (m *Manager) Interaction() *Interaction {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.pendingInteraction
	if p == nil || !m.busy || m.state.State != "needs_interaction" || !time.Now().Before(p.deadline) {
		return nil
	}
	q := p.request
	q.ExpiresAfterMs = int(time.Until(p.deadline).Milliseconds())
	if q.ExpiresAfterMs < 1 {
		return nil
	}
	q.Buttons = append([]InteractionButton{}, q.Buttons...)
	q.Fields = append([]InteractionField{}, q.Fields...)
	return &q
}
func (m *Manager) SubmitInteraction(a InteractionAnswer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.pendingInteraction
	if p == nil || !m.busy || m.state.State != "needs_interaction" || !time.Now().Before(p.deadline) || !ValidInteractionAnswer(&p.request, a) {
		return ErrInvalidInput
	}
	select {
	case p.answer <- a:
		m.state = Snapshot{State: "authenticating"}
		return nil
	default:
		return ErrBusy
	}
}
