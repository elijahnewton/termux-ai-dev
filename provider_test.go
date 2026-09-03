package main

import "testing"

func TestErrorBodyMessageIncludesProviderMetadataDetail(t *testing.T) {
	body := []byte(`{"error":{"message":"Provider returned error","metadata":{"raw":"unsupported parameter: tools"}}}`)
	got := errorBodyMessage(body)
	want := "Provider returned error: unsupported parameter: tools"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestErrorBodyMessageUsesTopLevelMessage(t *testing.T) {
	body := []byte(`{"message":"bad request from upstream"}`)
	got := errorBodyMessage(body)
	if got != "bad request from upstream" {
		t.Fatalf("got %q", got)
	}
}

func TestErrorBodyMessageFallsBackToRawBody(t *testing.T) {
	body := []byte("plain error body")
	got := errorBodyMessage(body)
	if got != "plain error body" {
		t.Fatalf("got %q", got)
	}
}
