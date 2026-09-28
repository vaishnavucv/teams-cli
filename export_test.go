package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	api "github.com/fossteams/teams-api/pkg"
	"github.com/fossteams/teams-api/pkg/csa"
)

func exportTestChat(subject string, arrivedAt time.Time) csa.Chat {
	return csa.Chat{
		Id:                 "chat-" + subject,
		ChatType:           "chat",
		MeetingInformation: csa.MeetingInfo{Subject: subject},
		LastMessage: csa.Message{
			ImDisplayName:       "Alice",
			Content:             "last thing said",
			OriginalArrivalTime: api.RFC3339Time(arrivedAt),
		},
	}
}

func exportTestOptions() AppOptions {
	options := defaultAppOptions()
	options.ExportMode = true
	return options
}

func failingFetcher(t *testing.T) messageFetcher {
	return func(context.Context, ConversationTarget) ([]csa.ChatMessage, error) {
		t.Fatal("messages were fetched when they should not have been")
		return nil, nil
	}
}

func TestBuildExportPayloadSkipsHiddenAndDisabledConversations(t *testing.T) {
	at := time.Date(2026, time.March, 27, 9, 0, 0, 0, time.UTC)

	hidden := exportTestChat("hidden", at)
	hidden.Hidden = true
	disabled := exportTestChat("disabled", at)
	disabled.IsDisabled = true

	payload := buildExportPayload(context.Background(), []csa.Chat{
		hidden,
		disabled,
		exportTestChat("visible", at),
	}, "self", "Me", exportTestOptions(), failingFetcher(t))

	if payload.Count != 1 {
		t.Fatalf("expected 1 conversation, got %d", payload.Count)
	}
	if payload.Conversations[0].Title != "visible" {
		t.Fatalf("expected the visible conversation, got %q", payload.Conversations[0].Title)
	}
}

func TestBuildExportPayloadUnreadOnlySkipsReadConversationsBeforeFetching(t *testing.T) {
	at := time.Date(2026, time.March, 27, 9, 0, 0, 0, time.UTC)

	read := exportTestChat("read", at)
	read.IsRead = true
	unread := exportTestChat("unread", at)

	var fetched []string
	options := exportTestOptions()
	options.ExportUnreadOnly = true
	options.ExportMessages = true

	payload := buildExportPayload(context.Background(), []csa.Chat{read, unread}, "self", "Me", options,
		func(_ context.Context, target ConversationTarget) ([]csa.ChatMessage, error) {
			fetched = append(fetched, target.Title)
			return nil, nil
		})

	if payload.Count != 1 || payload.Conversations[0].Title != "unread" {
		t.Fatalf("expected only the unread conversation, got %+v", payload.Conversations)
	}
	// The filter has to run before the fetch, or every skipped conversation
	// still costs a round trip.
	if len(fetched) != 1 || fetched[0] != "unread" {
		t.Fatalf("expected messages to be fetched only for the unread conversation, got %v", fetched)
	}
}

func TestBuildExportPayloadOrdersMostRecentFirstAndAppliesLimit(t *testing.T) {
	base := time.Date(2026, time.March, 27, 9, 0, 0, 0, time.UTC)

	options := exportTestOptions()
	options.ExportLimit = 2

	payload := buildExportPayload(context.Background(), []csa.Chat{
		exportTestChat("oldest", base),
		exportTestChat("newest", base.Add(2*time.Hour)),
		exportTestChat("middle", base.Add(time.Hour)),
	}, "self", "Me", options, failingFetcher(t))

	if payload.Count != 2 {
		t.Fatalf("expected the limit to keep 2 conversations, got %d", payload.Count)
	}
	if got := []string{payload.Conversations[0].Title, payload.Conversations[1].Title}; got[0] != "newest" || got[1] != "middle" {
		t.Fatalf("expected newest then middle, got %v", got)
	}
}

func TestBuildExportPayloadKeepsConversationWhenMessageFetchFails(t *testing.T) {
	base := time.Date(2026, time.March, 27, 9, 0, 0, 0, time.UTC)

	options := exportTestOptions()
	options.ExportMessages = true

	payload := buildExportPayload(context.Background(), []csa.Chat{
		exportTestChat("broken", base.Add(time.Hour)),
		exportTestChat("fine", base),
	}, "self", "Me", options,
		func(_ context.Context, target ConversationTarget) ([]csa.ChatMessage, error) {
			if target.Title == "broken" {
				return nil, errors.New("429 too many requests")
			}
			return []csa.ChatMessage{
				testChatMessage("m1", "Bob", "hello", base),
			}, nil
		})

	// One unreachable conversation must not remove it from the export, and must
	// not take the reachable ones with it.
	if payload.Count != 2 {
		t.Fatalf("expected both conversations, got %d", payload.Count)
	}
	broken := payload.Conversations[0]
	if broken.Title != "broken" {
		t.Fatalf("expected the failing conversation first, got %q", broken.Title)
	}
	if !strings.Contains(broken.Error, "429") {
		t.Fatalf("expected the fetch error to be reported, got %q", broken.Error)
	}
	if len(broken.Messages) != 0 {
		t.Fatalf("expected no messages for the failing conversation, got %d", len(broken.Messages))
	}
	if len(payload.Conversations[1].Messages) != 1 {
		t.Fatalf("expected the healthy conversation to keep its message, got %d", len(payload.Conversations[1].Messages))
	}
}

func TestBuildExportPayloadSkipsEmptyMessageBodies(t *testing.T) {
	base := time.Date(2026, time.March, 27, 9, 0, 0, 0, time.UTC)

	options := exportTestOptions()
	options.ExportMessages = true

	payload := buildExportPayload(context.Background(), []csa.Chat{exportTestChat("chat", base)}, "self", "Me", options,
		func(context.Context, ConversationTarget) ([]csa.ChatMessage, error) {
			return []csa.ChatMessage{
				testChatMessage("m1", "Bob", "<div></div>", base),
				testChatMessage("m2", "Bob", "  ", base),
				testChatMessage("m3", "Bob", "<p>real content</p>", base.Add(time.Minute)),
			}, nil
		})

	messages := payload.Conversations[0].Messages
	if len(messages) != 1 {
		t.Fatalf("expected only the message with content, got %d", len(messages))
	}
	if messages[0].Text != "real content" {
		t.Fatalf("expected the HTML to be stripped, got %q", messages[0].Text)
	}
	if !payload.WithMessages {
		t.Fatal("expected includes_messages to record that messages were requested")
	}
}

func TestBuildExportPayloadDoesNotFetchWithoutWithMessages(t *testing.T) {
	base := time.Date(2026, time.March, 27, 9, 0, 0, 0, time.UTC)

	payload := buildExportPayload(context.Background(), []csa.Chat{exportTestChat("chat", base)}, "self", "Me",
		exportTestOptions(), failingFetcher(t))

	if payload.WithMessages {
		t.Fatal("expected includes_messages to be false")
	}
	if len(payload.Conversations[0].Messages) != 0 {
		t.Fatal("expected no messages")
	}
	if payload.Conversations[0].LastText != "last thing said" {
		t.Fatalf("expected the conversation preview to survive, got %q", payload.Conversations[0].LastText)
	}
}

func TestWriteExportEmitsEmptyListRatherThanNull(t *testing.T) {
	payload := buildExportPayload(context.Background(), nil, "self", "Me", exportTestOptions(), nil)

	var buf strings.Builder
	if err := writeExport(&buf, payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A consumer decoding this should not have to tell null from [].
	if !strings.Contains(buf.String(), `"conversations": []`) {
		t.Fatalf("expected an empty conversation list, got %s", buf.String())
	}

	var decoded exportPayload
	if err := json.Unmarshal([]byte(buf.String()), &decoded); err != nil {
		t.Fatalf("export output is not valid JSON: %v", err)
	}
	if decoded.Count != 0 {
		t.Fatalf("expected conversation_count 0, got %d", decoded.Count)
	}
	if decoded.GeneratedAt == "" {
		t.Fatal("expected generated_at to be set")
	}
}

func TestWriteExportDoesNotEscapeHTMLInMessageText(t *testing.T) {
	payload := exportPayload{
		Conversations: []exportConversation{{
			ID:    "chat-1",
			Title: "Alice & Bob",
			Messages: []exportMessage{{
				Text: "5 < 6 & 7 > 6",
			}},
		}},
	}

	var buf strings.Builder
	if err := writeExport(&buf, payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// encoding/json escapes <, > and & as \\uXXXX by default, which makes prose
	// unreadable for the humans who will grep this. Assert the characters
	// survive literally.
	if !strings.Contains(buf.String(), "5 < 6 & 7 > 6") {
		t.Fatalf("expected unescaped text, got %s", buf.String())
	}
	if !strings.Contains(buf.String(), "Alice & Bob") {
		t.Fatalf("expected unescaped title, got %s", buf.String())
	}
}
