package main

import (
	"encoding/json"
	"testing"
)

func TestNormalizeToolCallArguments_ExecuteCommandString(t *testing.T) {
	got := normalizeToolCallArguments("execute_command", `"ls -la"`)
	if got != `{"command":"ls -la"}` {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeToolCallArguments_ObjectPreserved(t *testing.T) {
	got := normalizeToolCallArguments("apply_search_replace", `{"path":"a.txt","search":"a","replace":"b"}`)
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("result is not valid JSON object: %v", err)
	}
	if m["path"] != "a.txt" {
		t.Fatalf("got path %v", m["path"])
	}
}

func TestNormalizeToolCallArguments_InvalidJSONWrapped(t *testing.T) {
	got := normalizeToolCallArguments("apply_search_replace", `{path:"a.txt"}`)
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("result is not valid JSON object: %v", err)
	}
	if _, ok := m["_raw"]; !ok {
		t.Fatalf("expected _raw key, got %v", m)
	}
}
