package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	teams_api "github.com/fossteams/teams-api"
	"github.com/fossteams/teams-api/pkg/csa"
)

// The TUI was the only way this program ever showed anything, which makes it
// unusable by anything that is not a person at a keyboard: a cron job, a
// script, a monitoring check. `export` prints the same data the tree renders,
// on stdout, as JSON.
//
// It shares the fetch path with the UI rather than reimplementing it.
// fetchMessages needs only a client, a message limit and an http client, and
// appLogger falls back to discardLogger when there is none, so a bare AppState
// with three fields set is enough to reuse it — no tview, no terminal, and no
// second copy of the retry and endpoint logic to drift out of step.

// messageFetcher is the one part of an export that needs the network. Taking it
// as a parameter keeps the shaping, filtering and ordering below testable
// without a client, which is where the behaviour worth testing actually lives.
type messageFetcher func(ctx context.Context, target ConversationTarget) ([]csa.ChatMessage, error)

type exportMessage struct {
	From string `json:"from,omitempty"`
	At   string `json:"at,omitempty"`
	Text string `json:"text"`
}

type exportConversation struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Type       string          `json:"type,omitempty"`
	IsOneOnOne bool            `json:"is_one_on_one"`
	IsRead     bool            `json:"is_read"`
	LastAt     string          `json:"last_message_at,omitempty"`
	LastFrom   string          `json:"last_message_from,omitempty"`
	LastText   string          `json:"last_message,omitempty"`
	Messages   []exportMessage `json:"messages,omitempty"`
	// Set when a conversation was reached but its messages were not. The
	// conversation still appears, because one unreachable chat should not cost
	// the caller the other ninety.
	Error string `json:"error,omitempty"`
}

type exportPayload struct {
	GeneratedAt   string               `json:"generated_at"`
	Me            string               `json:"me,omitempty"`
	Count         int                  `json:"conversation_count"`
	Conversations []exportConversation `json:"conversations"`
	// Named so a reader can tell "no messages" from "messages were not asked
	// for": absent is not the same as empty, and a summary built from this
	// should not have to guess which it got.
	WithMessages bool `json:"includes_messages"`
}

func runExport(w io.Writer, options AppOptions) error {
	client, err := teams_api.New()
	if err != nil {
		return fmt.Errorf("unable to create teams client: %v", err)
	}

	conversations, err := client.GetConversations()
	if err != nil {
		return fmt.Errorf("unable to fetch conversations: %v", err)
	}

	// Titles for one-to-one chats are derived from the other participants, so
	// without an identity every such chat is named after everyone in it,
	// including the user. A failure here is not fatal: unnamed chats are far
	// better than no export.
	var selfMri, selfDisplayName string
	if me, err := client.GetMe(); err == nil && me != nil {
		selfMri = me.Mri
		selfDisplayName = me.DisplayName
	}

	// teamsClient lives on the embedded TeamsState, not on AppState itself.
	state := &AppState{
		TeamsState:   TeamsState{teamsClient: client},
		messageLimit: options.MessageLimit,
		httpClient:   newMessageHTTPClient(),
	}

	ctx := context.Background()
	if options.ExportTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, options.ExportTimeout)
		defer cancel()
	}

	payload := buildExportPayload(ctx, conversations.Chats, selfMri, selfDisplayName, options, state.fetchMessages)
	payload.Me = selfDisplayName

	return writeExport(w, payload)
}

func buildExportPayload(ctx context.Context, chats []csa.Chat, selfMri, selfDisplayName string, options AppOptions, fetch messageFetcher) exportPayload {
	out := exportPayload{
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		WithMessages: options.ExportMessages,
		// Never nil: a caller decoding this should get [] for "no conversations"
		// rather than null, which reads as a missing field.
		Conversations: []exportConversation{},
	}

	for _, chat := range chats {
		if chat.Hidden || chat.IsDisabled {
			continue
		}
		// Filter before fetching, not after: fetching messages for a
		// conversation about to be dropped is a round trip per chat for
		// nothing, and there can be a hundred of them.
		if options.ExportUnreadOnly && chat.IsRead {
			continue
		}

		title := resolveChatTitle(chat, selfMri, selfDisplayName)
		row := exportConversation{
			ID:         chat.Id,
			Title:      title,
			Type:       chat.ChatType,
			IsOneOnOne: chat.IsOneOnOne,
			IsRead:     chat.IsRead,
			LastFrom:   strings.TrimSpace(chat.LastMessage.ImDisplayName),
			LastText:   plainTextMessage(chat.LastMessage.Content),
		}
		if at := time.Time(chat.LastMessage.OriginalArrivalTime); !at.IsZero() {
			row.LastAt = at.UTC().Format(time.RFC3339)
		}

		if options.ExportMessages && fetch != nil {
			messages, err := fetch(ctx, ConversationTarget{ID: chat.Id, Title: title})
			if err != nil {
				row.Error = err.Error()
			} else {
				for _, m := range messages {
					text := plainTextMessage(m.Content)
					if strings.TrimSpace(text) == "" {
						continue
					}
					message := exportMessage{
						From: strings.TrimSpace(m.ImDisplayName),
						Text: text,
					}
					if at := time.Time(m.OriginalArrivalTime); !at.IsZero() {
						message.At = at.UTC().Format(time.RFC3339)
					}
					row.Messages = append(row.Messages, message)
				}
			}
		}

		out.Conversations = append(out.Conversations, row)
	}

	// Most recent first: anything reading this wants the new thing at the top,
	// and the API's own ordering is its own business. RFC 3339 in a fixed zone
	// sorts correctly as a string, and empty timestamps sort last.
	sort.SliceStable(out.Conversations, func(i, j int) bool {
		return out.Conversations[i].LastAt > out.Conversations[j].LastAt
	})

	if options.ExportLimit > 0 && len(out.Conversations) > options.ExportLimit {
		out.Conversations = out.Conversations[:options.ExportLimit]
	}
	out.Count = len(out.Conversations)

	return out
}

func writeExport(w io.Writer, payload exportPayload) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	// Message bodies are prose, and < for every stripped tag makes the
	// output unreadable for the humans who will inevitably grep it.
	encoder.SetEscapeHTML(false)
	return encoder.Encode(payload)
}
