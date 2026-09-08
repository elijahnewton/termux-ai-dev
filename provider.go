package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxResponseBody caps response reads so a misbehaving or hostile endpoint
// can't exhaust memory on a resource-constrained device.
const maxResponseBody = 10 << 20 // 10 MiB

// maxErrorBody caps how much of an error response body is echoed to the
// terminal.
const maxErrorBody = 500

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
	Index    *int             `json:"index,omitempty"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Type     string         `json:"type"`
	Function ToolDefinition `json:"function"`
}

type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type ChatCompletionRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	Tools     []Tool    `json:"tools,omitempty"`
	MaxTokens int       `json:"max_tokens,omitempty"`
	Stream    bool      `json:"stream,omitempty"`
}

type ChatCompletionResponse struct {
	ID      string    `json:"id"`
	Object  string    `json:"object"`
	Created int64     `json:"created"`
	Choices []Choice  `json:"choices"`
	Error   *APIError `json:"error,omitempty"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	Delta        Message `json:"delta"`
	FinishReason string  `json:"finish_reason"`
}

type APIError struct {
	Message  string                 `json:"message"`
	Type     string                 `json:"type"`
	Param    string                 `json:"param,omitempty"`
	Code     interface{}            `json:"code,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

func (a *Agent) doRequest(ctx context.Context, payload ChatCompletionRequest) (*ChatCompletionResponse, error) {
	if a.cfg.Stream {
		return a.doStreamRequest(ctx, payload)
	}
	return a.doJSONRequest(ctx, payload)
}

func (a *Agent) doJSONRequest(ctx context.Context, payload ChatCompletionRequest) (*ChatCompletionResponse, error) {
	payload.Stream = false
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", a.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	a.setHeaders(req)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBody {
		return nil, fmt.Errorf("response exceeds %d MiB limit", maxResponseBody>>20)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, errorBodyMessage(data))
	}

	var apiResp ChatCompletionResponse
	if err := json.Unmarshal(data, &apiResp); err != nil {
		return nil, fmt.Errorf("invalid JSON from API: %w", err)
	}
	if apiResp.Error != nil {
		return nil, fmt.Errorf("API error: %s", apiResp.Error.Message)
	}
	return &apiResp, nil
}

// doStreamRequest consumes OpenAI-compatible SSE and assembles a full message,
// including streamed tool_calls. Falls back to non-streaming on unsupported.
func (a *Agent) doStreamRequest(ctx context.Context, payload ChatCompletionRequest) (*ChatCompletionResponse, error) {
	payload.Stream = true
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", a.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	a.setHeaders(req)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
		msg := errorBodyMessage(data)
		if resp.StatusCode == http.StatusBadRequest || strings.Contains(strings.ToLower(msg), "stream") {
			out, err := a.doJSONRequest(ctx, payload)
			if err == nil && out != nil && a.onStreamToken != nil && len(out.Choices) > 0 {
				if c := out.Choices[0].Message.Content; c != "" {
					a.onStreamToken(c)
					a.onStreamToken("\n")
				}
			}
			return out, err
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}

	assistant := Message{Role: "assistant"}
	toolIndex := map[int]*ToolCall{}
	finish := ""
	printed := false

	sc := bufio.NewScanner(resp.Body)
	scanBuf := make([]byte, 0, 64*1024)
	sc.Buffer(scanBuf, 2<<20)

	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk ChatCompletionResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			return nil, fmt.Errorf("API error: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.FinishReason != "" {
			finish = ch.FinishReason
		}
		delta := ch.Delta
		if delta.Content != "" {
			assistant.Content += delta.Content
			if a.onStreamToken != nil {
				a.onStreamToken(delta.Content)
				printed = true
			}
		}
		for _, tc := range delta.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			} else if tc.ID != "" {
				found := -1
				for k, v := range toolIndex {
					if v.ID == tc.ID {
						found = k
						break
					}
				}
				if found >= 0 {
					idx = found
				} else {
					idx = nextToolIndex(toolIndex)
				}
			} else if len(toolIndex) > 0 {
				idx = maxToolIndex(toolIndex)
			}

			existing, ok := toolIndex[idx]
			if !ok {
				cp := tc
				if cp.Type == "" {
					cp.Type = "function"
				}
				toolIndex[idx] = &cp
				continue
			}
			if tc.ID != "" {
				existing.ID = tc.ID
			}
			if tc.Type != "" {
				existing.Type = tc.Type
			}
			if tc.Function.Name != "" {
				existing.Function.Name += tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				existing.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if printed && a.onStreamToken != nil {
		a.onStreamToken("\n")
	}

	if len(toolIndex) > 0 {
		max := maxToolIndex(toolIndex)
		for i := 0; i <= max; i++ {
			if tc, ok := toolIndex[i]; ok {
				assistant.ToolCalls = append(assistant.ToolCalls, *tc)
			}
		}
	}

	return &ChatCompletionResponse{
		Choices: []Choice{{
			Index:        0,
			Message:      assistant,
			FinishReason: finish,
		}},
	}, nil
}

func nextToolIndex(m map[int]*ToolCall) int {
	if len(m) == 0 {
		return 0
	}
	return maxToolIndex(m) + 1
}

func maxToolIndex(m map[int]*ToolCall) int {
	max := 0
	for k := range m {
		if k > max {
			max = k
		}
	}
	return max
}

func (a *Agent) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if a.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	}
	for k, v := range a.cfg.ExtraHeaders {
		req.Header.Set(k, v)
	}
}

// errorBodyMessage extracts a readable message from an error response body.
func errorBodyMessage(data []byte) string {
	var wrapper struct {
		Error   *APIError `json:"error"`
		Message string    `json:"message"`
	}
	if err := json.Unmarshal(data, &wrapper); err == nil {
		if wrapper.Error != nil {
			msg := strings.TrimSpace(wrapper.Error.Message)
			if msg != "" {
				if detail := providerErrorDetail(wrapper.Error.Metadata); detail != "" && isGenericProviderMessage(msg) {
					return msg + ": " + detail
				}
				return msg
			}
		}
		if strings.TrimSpace(wrapper.Message) != "" {
			return strings.TrimSpace(wrapper.Message)
		}
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return "(no response body)"
	}
	if len(s) > maxErrorBody {
		s = s[:maxErrorBody] + "...(truncated)"
	}
	return s
}

func isGenericProviderMessage(msg string) bool {
	switch strings.ToLower(strings.TrimSpace(msg)) {
	case "provider returned error", "bad request", "request failed", "invalid request":
		return true
	default:
		return false
	}
}

func providerErrorDetail(metadata map[string]interface{}) string {
	if len(metadata) == 0 {
		return ""
	}
	for _, key := range []string{"raw", "reason", "provider_error", "details", "message"} {
		if v, ok := metadata[key]; ok {
			if s := stringifyMetaValue(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func stringifyMetaValue(v interface{}) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case fmt.Stringer:
		return strings.TrimSpace(x.String())
	case map[string]interface{}, []interface{}:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	default:
		if x == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprintf("%v", x))
	}
}
