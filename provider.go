package main

import (
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
    body, err := json.Marshal(payload)
    if err != nil {
        return nil, err
    }

    req, err := http.NewRequestWithContext(ctx, "POST", a.cfg.Endpoint, bytes.NewReader(body))
    if err != nil {
        return nil, err
    }
    req.Header.Set("Content-Type", "application/json")
    if a.cfg.APIKey != "" {
        req.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
    }
    for k, v := range a.cfg.ExtraHeaders {
        req.Header.Set(k, v)
    }

    resp, err := a.client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    // Read at most maxResponseBody+1 bytes: the extra byte distinguishes
    // "hit the cap" from "response was exactly cap-sized" (a silently
    // truncated body would otherwise fail JSON decoding confusingly).
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

// errorBodyMessage extracts a readable message from an error response body.
// Most OpenAI-compatible providers return {"error": {"message": ...}} on
// failure; fall back to the raw (truncated) body otherwise.
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