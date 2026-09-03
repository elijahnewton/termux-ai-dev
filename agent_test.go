package main

import "testing"

// TestSanitizeToolCallsRepairsEmptyArguments covers the case that produced:
//   HTTP 400: invalid tool call provided in messages[N].tool_calls[0]:
//   tool arguments must be a stringified JSON object
// A provider that returns "" (or JSON null, decoded by Go into "") for
// function.arguments must not have that empty string replayed verbatim on
// the next turn.
func TestSanitizeToolCallsRepairsEmptyArguments(t *testing.T) {
	msg := &Message{
		Role: "assistant",
		ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "execute_command", Arguments: ""}},
		},
	}
	sanitizeToolCalls(msg)
	if got := msg.ToolCalls[0].Function.Arguments; got != "{}" {
		t.Fatalf("got arguments %q, want %q", got, "{}")
	}
}

func TestSanitizeToolCallsRepairsLiteralNull(t *testing.T) {
	msg := &Message{
		ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "execute_command", Arguments: "null"}},
		},
	}
	sanitizeToolCalls(msg)
	if got := msg.ToolCalls[0].Function.Arguments; got != "{}" {
		t.Fatalf("got arguments %q, want %q", got, "{}")
	}
}

func TestSanitizeToolCallsRepairsTruncatedJSON(t *testing.T) {
	msg := &Message{
		ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "execute_command", Arguments: `{"command": "ls -la`}},
		},
	}
	sanitizeToolCalls(msg)
	if got := msg.ToolCalls[0].Function.Arguments; got != "{}" {
		t.Fatalf("got arguments %q, want %q", got, "{}")
	}
}

func TestSanitizeToolCallsLeavesValidArgumentsUntouched(t *testing.T) {
	want := `{"command":"ls -la"}`
	msg := &Message{
		ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "execute_command", Arguments: want}},
		},
	}
	sanitizeToolCalls(msg)
	if got := msg.ToolCalls[0].Function.Arguments; got != want {
		t.Fatalf("got arguments %q, want %q (should be untouched)", got, want)
	}
}

func TestSanitizeToolCallsFillsMissingIDAndType(t *testing.T) {
	msg := &Message{
		ToolCalls: []ToolCall{
			{Function: ToolCallFunction{Name: "execute_command", Arguments: "{}"}},
		},
	}
	sanitizeToolCalls(msg)
	tc := msg.ToolCalls[0]
	if tc.ID == "" {
		t.Fatal("expected a non-empty id to be generated")
	}
	if tc.Type != "function" {
		t.Fatalf("got type %q, want %q", tc.Type, "function")
	}
}
