package main

import "testing"

func TestTextMessageKeepsPaneIndentation(t *testing.T) {
	// The messages pane relies on this exact shape, so the split into
	// messageTextNodes must not change it.
	got := textMessage("<p>first</p><p>second</p>")
	want := "\tfirst\n\tsecond\n"

	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestPlainTextMessageStripsMarkupAndDecoration(t *testing.T) {
	testCases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain text", input: "hello there", want: "hello there"},
		{name: "single element", input: "<p>hello there</p>", want: "hello there"},
		{name: "nested markup", input: "<div><p>hello <b>there</b></p></div>", want: "hello\nthere"},
		{name: "multiple blocks", input: "<p>first</p><p>second</p>", want: "first\nsecond"},
		{name: "empty markup", input: "<div></div>", want: ""},
		{name: "whitespace only", input: "   ", want: ""},
		{name: "empty input", input: "", want: ""},
		{name: "entities are decoded", input: "<p>5 &lt; 6 &amp; 7</p>", want: "5 < 6 & 7"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := plainTextMessage(testCase.input); got != testCase.want {
				t.Fatalf("expected %q, got %q", testCase.want, got)
			}
		})
	}
}
