package relayguard

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

type Result struct {
	Status     int
	Header     http.Header
	Frames     [][]byte
	Usage      *dto.Usage
	Quota      int
	Expires    time.Time
	Bytes      int
	Successful bool
}
type cachedResult struct {
	key    string
	result *Result
}
type Cache struct {
	mu                             sync.Mutex
	entries                        map[string]*list.Element
	lru                            *list.List
	flights                        map[string]*Flight
	bytes                          int
	hits, misses, shared, bypassed uint64
	epoch                          uint64
}
type CacheStats struct {
	Entries  int    `json:"entries"`
	Bytes    int    `json:"bytes"`
	Active   int    `json:"active"`
	Hits     uint64 `json:"hits"`
	Misses   uint64 `json:"misses"`
	Shared   uint64 `json:"shared"`
	Bypassed uint64 `json:"bypassed"`
}

func NewCache() *Cache {
	return &Cache{entries: map[string]*list.Element{}, lru: list.New(), flights: map[string]*Flight{}}
}

// RequestFingerprint preserves every generation parameter, including absent
// versus explicit zero. Prefix matching is an explicit fixed-template policy:
// only message text is shortened; model, roles and all parameters still match.
func RequestFingerprint(body []byte, namespace string, cfg operation_setting.RelayCacheConfig, modelOverride ...string) (string, bool) {
	if len(body) > 4<<20 {
		return "", false
	}
	var request map[string]any
	if common.Unmarshal(body, &request) != nil {
		return "", false
	}
	for _, key := range []string{"tools", "functions", "previous_response_id", "conversation", "background", "modalities", "audio", "cachedContent", "cached_content"} {
		if value, ok := request[key]; ok && value != nil {
			return "", false
		}
	}
	if store, ok := request["store"].(bool); ok && store {
		return "", false
	}
	model, _ := request["model"].(string)
	if model == "" && len(modelOverride) > 0 {
		model = modelOverride[0]
	}
	if !operation_setting.RelayGuardModelMatches(cfg.Models, model) {
		return "", false
	}
	stream, _ := request["stream"].(bool)
	if stream && !cfg.Stream {
		return "", false
	}
	messages, ok := request["messages"].([]any)
	gemini := false
	if !ok {
		if contents, exists := request["contents"].([]any); exists {
			messages = contents
			ok = true
			gemini = true
		}
	}
	if !ok {
		if input, exists := request["input"]; exists {
			if text, ok := input.(string); ok {
				messages = []any{map[string]any{"role": "user", "content": text}}
			} else {
				messages, ok = input.([]any)
				if !ok {
					return "", false
				}
			}
		} else {
			return "", false
		}
	}
	if gemini {
		for _, key := range []string{"systemInstruction", "system_instruction"} {
			if instruction, exists := request[key]; exists && instruction != nil {
				messages = append(messages, instruction)
			}
		}
		for _, key := range []string{"generationConfig", "generation_config"} {
			if generation, ok := request[key].(map[string]any); ok {
				for _, field := range []string{"responseModalities", "response_modalities"} {
					if modalities, exists := generation[field]; exists && modalities != nil {
						values, ok := modalities.([]any)
						if !ok {
							return "", false
						}
						for _, value := range values {
							if value != "TEXT" {
								return "", false
							}
						}
					}
				}
			}
		}
	}
	textChars := 0
	for _, value := range messages {
		message, ok := value.(map[string]any)
		if !ok {
			return "", false
		}
		if role, _ := message["role"].(string); role == "tool" || role == "function" {
			return "", false
		}
		if _, exists := message["tool_calls"]; exists {
			return "", false
		}
		if _, exists := message["function_call"]; exists {
			return "", false
		}
		content, ok := message["content"].(string)
		if !ok {
			parts, ok := message["content"].([]any)
			if gemini {
				parts, ok = message["parts"].([]any)
			}
			if !ok {
				return "", false
			}
			for _, value := range parts {
				part, ok := value.(map[string]any)
				if !ok {
					return "", false
				}
				kind, _ := part["type"].(string)
				if kind != "text" && kind != "input_text" && !(gemini && len(part) == 1) {
					return "", false
				}
				text, ok := part["text"].(string)
				if !ok {
					return "", false
				}
				textChars += utf8.RuneCountInString(text)
			}
		} else {
			textChars += utf8.RuneCountInString(content)
		}
	}
	if textChars == 0 {
		return "", false
	}
	if cfg.Match == "prefix" {
		if gemini {
			return "", false
		}
		// Prefix mode deliberately supports only string messages and is restricted
		// by the model allowlist, which the administrator must explicitly set.
		if len(cfg.Models) == 0 || textChars < cfg.PrefixChars {
			return "", false
		}
		remaining := cfg.PrefixChars
		prefix := make([]any, 0, len(messages))
		for _, value := range messages {
			message := value.(map[string]any)
			text, ok := message["content"].(string)
			if !ok {
				return "", false
			}
			runes := []rune(text)
			n := min(remaining, len(runes))
			message["content"] = string(runes[:n])
			prefix = append(prefix, message)
			remaining -= n
			if remaining == 0 {
				break
			}
		}
		if _, exists := request["messages"]; exists {
			request["messages"] = prefix
		} else {
			request["input"] = prefix
		}
	}
	var rawFields map[string]common.RawMessage
	if common.Unmarshal(body, &rawFields) != nil {
		return "", false
	}
	if cfg.Match == "prefix" {
		for _, name := range []string{"messages", "input"} {
			if _, exists := rawFields[name]; exists {
				encoded, err := common.Marshal(request[name])
				if err != nil {
					return "", false
				}
				rawFields[name] = encoded
			}
		}
	}
	raw, err := common.Marshal(rawFields)
	if err != nil {
		return "", false
	}
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return "", false
	}
	hash := sha256.New()
	hash.Write([]byte(namespace))
	hash.Write([]byte{0})
	hash.Write(canonical)
	return hex.EncodeToString(hash.Sum(nil)), true
}

// Raw numeric literals are retained: large integer seeds must never collide
// through float64 rounding. Object keys and insignificant whitespace normalize.
func canonicalJSON(raw []byte) ([]byte, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, errors.New("empty JSON")
	}
	switch raw[0] {
	case '{':
		var object map[string]common.RawMessage
		if err := common.Unmarshal(raw, &object); err != nil {
			return nil, err
		}
		for key, value := range object {
			normalized, err := canonicalJSON(value)
			if err != nil {
				return nil, err
			}
			object[key] = normalized
		}
		return common.Marshal(object)
	case '[':
		var array []common.RawMessage
		if err := common.Unmarshal(raw, &array); err != nil {
			return nil, err
		}
		for i, value := range array {
			normalized, err := canonicalJSON(value)
			if err != nil {
				return nil, err
			}
			array[i] = normalized
		}
		return common.Marshal(array)
	case '"':
		var value string
		if err := common.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		return common.Marshal(value)
	default:
		return raw, nil
	}
}

func (c *Cache) pruneLocked(cfg operation_setting.RelayCacheConfig, now time.Time) {
	for key, entry := range c.entries {
		if !entry.Value.(*cachedResult).result.Expires.After(now) {
			c.removeLocked(key, entry)
		}
	}
	for c.lru.Len() > cfg.MaxEntries || c.bytes > cfg.MaxMemoryMB<<20 {
		entry := c.lru.Back()
		if entry == nil {
			break
		}
		c.removeLocked(entry.Value.(*cachedResult).key, entry)
	}
}
func (c *Cache) removeLocked(key string, entry *list.Element) {
	c.bytes -= entry.Value.(*cachedResult).result.Bytes
	c.lru.Remove(entry)
	delete(c.entries, key)
}

// Acquire subscribes before starting the producer, so the first SSE event
// cannot race the first consumer. An oversize response stops accepting new
// subscribers while existing consumers continue receiving bounded queues.
func (c *Cache) Acquire(key string, cfg operation_setting.RelayCacheConfig, now time.Time) (*Result, *Flight, *Subscription, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(cfg, now)
	if entry := c.entries[key]; entry != nil {
		c.hits++
		c.lru.MoveToFront(entry)
		return entry.Value.(*cachedResult).result, nil, nil, false
	}
	if cfg.Coalesce {
		if flight := c.flights[key]; flight != nil {
			if sub := flight.subscribe(cfg.MaxSubscribers); sub != nil {
				c.shared++
				return nil, flight, sub, false
			}
			c.bypassed++
			return nil, nil, nil, false
		}
	}
	reservedBytes := cfg.MaxResponseKB << 10
	for _, flight := range c.flights {
		reservedBytes += flight.maxBytes
	}
	if len(c.flights) >= 256 || reservedBytes > cfg.MaxMemoryMB<<20 {
		c.bypassed++
		return nil, nil, nil, false
	}
	for c.bytes+reservedBytes > cfg.MaxMemoryMB<<20 {
		entry := c.lru.Back()
		if entry == nil {
			break
		}
		c.removeLocked(entry.Value.(*cachedResult).key, entry)
	}
	flight := newFlight(cfg.MaxResponseKB << 10)
	sub := flight.subscribe(cfg.MaxSubscribers)
	if cfg.Coalesce {
		c.flights[key] = flight
	} else {
		c.flights[fmt.Sprintf("%s:%p", key, flight)] = flight
	}
	flight.cacheEpoch = c.epoch
	c.misses++
	return nil, flight, sub, true
}

func (c *Cache) Complete(key string, flight *Flight, cfg operation_setting.RelayCacheConfig, result *Result, successful bool) {
	result.Successful = successful
	c.mu.Lock()
	defer c.mu.Unlock()
	frames, size, overflow := flight.finish(result)
	for flightKey, value := range c.flights {
		if value == flight {
			delete(c.flights, flightKey)
		}
	}
	if !successful || overflow || flight.cacheEpoch != c.epoch {
		return
	}
	result.Frames = frames
	result.Bytes = size
	result.Expires = time.Now().Add(time.Duration(cfg.TTLSeconds) * time.Second)
	if old := c.entries[key]; old != nil {
		c.removeLocked(key, old)
	}
	c.entries[key] = c.lru.PushFront(&cachedResult{key, result})
	c.bytes += size
	c.pruneLocked(cfg, time.Now())
	reservedBytes := 0
	for _, active := range c.flights {
		reservedBytes += active.maxBytes
	}
	for c.bytes+reservedBytes > cfg.MaxMemoryMB<<20 {
		entry := c.lru.Back()
		if entry == nil {
			break
		}
		c.removeLocked(entry.Value.(*cachedResult).key, entry)
	}
}
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]*list.Element{}
	c.lru.Init()
	c.bytes = 0
	c.epoch++
}
func (c *Cache) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for key, e := range c.entries {
		if !e.Value.(*cachedResult).result.Expires.After(now) {
			c.removeLocked(key, e)
		}
	}
	return CacheStats{c.lru.Len(), c.bytes, len(c.flights), c.hits, c.misses, c.shared, c.bypassed}
}

type Flight struct {
	mu               sync.Mutex
	header           http.Header
	status           int
	frames           [][]byte
	bytes, maxBytes  int
	overflow, closed bool
	subs             map[*Subscription]bool
	ready, done      chan struct{}
	result           *Result
	cancel           context.CancelFunc
	cacheEpoch       uint64
}
type Subscription struct {
	flight  *Flight
	Prefix  [][]byte
	Queue   chan []byte
	stopped chan struct{}
	queued  int
	once    sync.Once
}

func newFlight(maxBytes int) *Flight {
	return &Flight{maxBytes: maxBytes, subs: map[*Subscription]bool{}, ready: make(chan struct{}), done: make(chan struct{})}
}
func (f *Flight) subscribe(maxSubscribers int) *Subscription {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.overflow || f.closed || len(f.subs) >= maxSubscribers {
		return nil
	}
	s := &Subscription{flight: f, Prefix: append([][]byte(nil), f.frames...), Queue: make(chan []byte, 256), stopped: make(chan struct{})}
	f.subs[s] = true
	return s
}
func (f *Flight) SetCancel(cancel context.CancelFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancel = cancel
	if len(f.subs) == 0 {
		cancel()
	}
}
func (f *Flight) Publish(status int, header http.Header, frame []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	if f.header == nil {
		f.header = header.Clone()
		f.status = status
		close(f.ready)
	}
	if len(frame) == 0 {
		return
	}
	frame = bytes.Clone(frame)
	if !f.overflow {
		if f.bytes+len(frame) > f.maxBytes {
			f.overflow = true
			f.frames = nil
		} else {
			f.frames = append(f.frames, frame)
			f.bytes += len(frame)
		}
	}
	for s := range f.subs {
		// A single large HTTP write must still reach a waiting consumer even
		// when it exceeds cache capacity. Only accumulated backlog is limited.
		if s.queued > 0 && s.queued+len(frame) > 2<<20 {
			close(s.stopped)
			delete(f.subs, s)
			continue
		}
		select {
		case s.Queue <- frame:
			s.queued += len(frame)
		default:
			close(s.stopped)
			delete(f.subs, s)
		}
	}
	if len(f.subs) == 0 && f.cancel != nil {
		f.cancel()
	}
}
func (f *Flight) finish(result *Result) ([][]byte, int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.header == nil {
		f.header = result.Header.Clone()
		f.status = result.Status
		close(f.ready)
	}
	f.closed = true
	f.result = result
	close(f.done)
	return f.frames, f.bytes, f.overflow
}
func (s *Subscription) Headers(ctx context.Context) (int, http.Header, error) {
	select {
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case <-s.stopped:
		return 0, nil, errors.New("stream consumer is too slow")
	case <-s.flight.ready:
	}
	s.flight.mu.Lock()
	defer s.flight.mu.Unlock()
	return s.flight.status, s.flight.header.Clone(), nil
}
func (s *Subscription) Next(ctx context.Context) ([]byte, bool, error) {
	select {
	case <-s.stopped:
		return nil, false, errors.New("stream consumer is too slow")
	default:
	}
	if len(s.Prefix) > 0 {
		frame := s.Prefix[0]
		s.Prefix = s.Prefix[1:]
		return frame, true, nil
	}
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case <-s.stopped:
		return nil, false, errors.New("stream consumer is too slow")
	case frame := <-s.Queue:
		s.flight.mu.Lock()
		s.queued -= len(frame)
		s.flight.mu.Unlock()
		return frame, true, nil
	case <-s.flight.done:
		select {
		case frame := <-s.Queue:
			s.flight.mu.Lock()
			s.queued -= len(frame)
			s.flight.mu.Unlock()
			return frame, true, nil
		default:
			return nil, false, nil
		}
	}
}
func (s *Subscription) Result() *Result {
	s.flight.mu.Lock()
	defer s.flight.mu.Unlock()
	return s.flight.result
}
func (s *Subscription) Close() {
	s.once.Do(func() {
		f := s.flight
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.subs, s)
		if len(f.subs) == 0 && !f.closed && f.cancel != nil {
			f.cancel()
		}
	})
}
