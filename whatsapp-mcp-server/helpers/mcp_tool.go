package helpers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// InitMcpTool initializes MCP tool for the MCP server
func InitMcpTool() {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "whatsapp-mcp",
		Version: "v1.0.0",
	}, nil)

	mcp.AddTool[searchContactsInput, any](server, &mcp.Tool{
		Name:        "search_contacts",
		Description: "Search WhatsApp contacts by name or phone number.",
	}, searchContactsHandler)

	mcp.AddTool[listMessagesInput, any](server, &mcp.Tool{
		Name:        "list_messages",
		Description: "Get WhatsApp messages matching specified criteria with optional context around matches.",
	}, listMessagesHandler)

	mcp.AddTool[getMessageContextInput, any](server, &mcp.Tool{
		Name:        "get_message_context",
		Description: "Get surrounding messages (context) around a specific WhatsApp message.",
	}, getMessageContextHandler)

	mcp.AddTool[listChatsInput, any](server, &mcp.Tool{
		Name:        "list_chats",
		Description: "Get list of WhatsApp chats, optionally filtered and sorted.",
	}, listChatsHandler)

	mcp.AddTool[getChatInput, any](server, &mcp.Tool{
		Name:        "get_chat",
		Description: "Get metadata of a specific WhatsApp chat by JID.",
	}, getChatHandler)

	mcp.AddTool[getDirectChatByContactInput, any](server, &mcp.Tool{
		Name:        "get_direct_chat_by_contact",
		Description: "Get direct (1:1) chat metadata by contact phone number.",
	}, getDirectChatByContactHandler)

	mcp.AddTool[getContactChatsInput, any](server, &mcp.Tool{
		Name:        "get_contact_chats",
		Description: "Get all chats involving a specific contact (JID).",
	}, getContactChatsHandler)

	mcp.AddTool[getLastInteractionInput, any](server, &mcp.Tool{
		Name:        "get_last_interaction",
		Description: "Get most recent WhatsApp message involving the contact.",
	}, getLastInteractionHandler)

	mcp.AddTool[sendMessageInput, map[string]any](server, &mcp.Tool{
		Name:        "send_message",
		Description: "Send a text message to a person or group on WhatsApp. For groups use the group JID.",
	}, sendMessageHandler)

	mcp.AddTool[sendFileInput, map[string]any](server, &mcp.Tool{
		Name:        "send_file",
		Description: "Send image, video, document or any file via WhatsApp.",
	}, sendFileHandler)

	mcp.AddTool[sendAudioMessageInput, map[string]any](server, &mcp.Tool{
		Name:        "send_audio_message",
		Description: "Send audio/voice message (converted to Opus .ogg if needed).",
	}, sendAudioMessageHandler)

	mcp.AddTool[downloadMediaInput, map[string]any](server, &mcp.Tool{
		Name:        "download_media",
		Description: "Download media from a WhatsApp message and return local file path.",
	}, downloadMediaHandler)

	mcp.AddTool[getLoginStatusInput, any](server, &mcp.Tool{
		Name:        "get_login_status",
		Description: "Check whether the WhatsApp bridge is connected and logged in. Returns {connected, logged_in, pairing_required}.",
	}, getLoginStatusHandler)

	mcp.AddTool[getPairingQrInput, any](server, &mcp.Tool{
		Name:        "get_pairing_qr",
		Description: "Fetch the WhatsApp pairing QR as a PNG image. Returns image content when pairing is required; returns a text message when the bridge is already logged in or pairing has not started.",
	}, getPairingQrHandler)

	isHttp := strings.ToLower(ReadEnv("IS_HTTP", "false")) == "true" ||
		strings.ToLower(ReadEnv("IS_HTTP", "0")) == "1"

	ctx := context.Background()

	if isHttp {
		addr := ReadEnv("HTTP_BASE_URL", "0.0.0.0:5777")
		slog.Info("Starting WhatsApp MCP HTTP streaming", "addr", addr)

		handler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
			return server
		}, nil)
		if err := http.ListenAndServe(addr, handler); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	} else {
		slog.Info("Starting WhatsApp MCP server in stdio mode")
		if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
			log.Fatalf("stdio failed: %v", err)
		}
	}
}

type searchContactsInput struct {
	Query string `json:"query" mcp:"description:Search term to match against contact names or phone numbers"`
}

type listMessagesInput struct {
	After             *string `mcp:"description:ISO-8601 formatted string"`
	Before            *string `json:"before,omitempty" jsonschema:"description:ISO-8601 formatted string"`
	SenderPhoneNumber *string `json:"sender_phone_number,omitempty"`
	ChatJid           *string `json:"chat_jid,omitempty"`
	Query             *string `json:"query,omitempty" jsonschema:"description:Search term in message content"`
	Limit             int     `json:"limit" jsonschema:"default:20"`
	Page              int     `json:"page" jsonschema:"default:0"`
	IncludeContext    bool    `json:"include_context" jsonschema:"default:true"`
	ContextBefore     int     `json:"context_before" jsonschema:"default:1"`
	ContextAfter      int     `json:"context_after" jsonschema:"default:1"`
}

type getMessageContextInput struct {
	MessageID string `json:"message_id" jsonschema:"description:The ID of the message"`
	Before    int    `json:"before" jsonschema:"default:5"`
	After     int    `json:"after" jsonschema:"default:5"`
}

type listChatsInput struct {
	Query              *string `json:"query,omitempty"`
	Limit              int     `json:"limit" jsonschema:"default:20"`
	Page               int     `json:"page" jsonschema:"default:0"`
	IncludeLastMessage bool    `json:"include_last_message" jsonschema:"default:true"`
	SortBy             string  `json:"sort_by" jsonschema:"default:last_active,enum:last_active|name"`
}

type getChatInput struct {
	ChatJid            string `json:"chat_jid" jsonschema:"description:The JID of the chat"`
	IncludeLastMessage bool   `json:"include_last_message" jsonschema:"default:true"`
}

type getDirectChatByContactInput struct {
	SenderPhoneNumber string `json:"sender_phone_number" jsonschema:"description:Phone number with country code"`
}

type getContactChatsInput struct {
	Jid   string `json:"jid"`
	Limit int    `json:"limit" jsonschema:"default:20"`
	Page  int    `json:"page" jsonschema:"default:0"`
}

type getLastInteractionInput struct {
	Jid string `json:"jid" jsonschema:"description:The contact's JID"`
}

type sendMessageInput struct {
	Recipient string `json:"recipient" jsonschema:"description:Phone number (no +) or group JID like 123@g.us"`
	Message   string `json:"message"`
}

type sendFileInput struct {
	Recipient string `json:"recipient"`
	MediaPath string `json:"media_path" jsonschema:"description:Absolute path to the file"`
}

type sendAudioMessageInput struct {
	Recipient string `json:"recipient"`
	MediaPath string `json:"media_path" jsonschema:"description:Absolute path to audio file"`
}

type downloadMediaInput struct {
	MessageID string `json:"message_id"`
	ChatJid   string `json:"chat_jid"`
}

type getLoginStatusInput struct{}

type getPairingQrInput struct{}

func callAPI(method, path string, body any) ([]byte, error) {
	token, err := GetOrRefreshJwtToken()
	if err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(b)
	}

	fullURL := fmt.Sprintf("%s%s", apiBaseURL, path)
	req, err := http.NewRequest(method, fullURL, reqBody)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	client := &http.Client{Timeout: apiTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(data))
	}

	return data, nil
}

func sendMessageHandler(
	ctx context.Context,
	req *mcp.CallToolRequest,
	in sendMessageInput,
) (*mcp.CallToolResult, map[string]any, error) {
	if in.Recipient == "" || in.Message == "" {
		return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{
					&mcp.TextContent{Text: "recipient and message are required"},
				},
			}, map[string]any{
				"success": false,
				"error":   "recipient and message are required",
			}, nil
	}

	payload := map[string]any{
		"recipient": in.Recipient,
		"message":   in.Message,
	}

	data, err := callAPI(http.MethodPost, "/send", payload)
	if err != nil {
		return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{
					&mcp.TextContent{Text: err.Error()},
				},
			}, map[string]any{
				"success": false,
				"error":   err.Error(),
			}, nil
	}

	var resp map[string]any
	if err := json.Unmarshal(data, &resp); err != nil {
		return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{
					&mcp.TextContent{Text: "failed to parse API response"},
				},
			}, map[string]any{
				"success": false,
				"error":   "failed to parse API response",
			}, nil
	}

	return &mcp.CallToolResult{}, resp, nil
}

func searchContactsHandler(
	ctx context.Context,
	req *mcp.CallToolRequest,
	in searchContactsInput,
) (*mcp.CallToolResult, any, error) {
	if in.Query == "" {
		return ErrResult("query is required"), nil, nil
	}

	data, err := callAPI(http.MethodGet, "/contacts/search?q="+in.Query, nil)
	if err != nil {
		return ErrResult(err.Error()), nil, nil
	}

	var result struct {
		Contacts []map[string]any `json:"contacts"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return ErrResult("invalid response format"), nil, nil
	}

	return OkResult(result.Contacts), nil, nil
}

// list_messages (similar for all read/list tools that return slices/maps)
func listMessagesHandler(
	ctx context.Context,
	req *mcp.CallToolRequest,
	in listMessagesInput,
) (*mcp.CallToolResult, any, error) {
	q := ""
	if in.After != nil {
		q += "&after=" + *in.After
	}
	if in.Before != nil {
		q += "&before=" + *in.Before
	}
	if in.SenderPhoneNumber != nil {
		q += "&sender=" + *in.SenderPhoneNumber
	}
	if in.ChatJid != nil {
		q += "&chat=" + *in.ChatJid
	}
	if in.Query != nil {
		q += "&search=" + *in.Query
	}
	q += fmt.Sprintf("&limit=%d&page=%d", in.Limit, in.Page)
	if in.IncludeContext {
		q += "&context=true"
	}

	data, err := callAPI(http.MethodGet, "/messages?"+strings.TrimPrefix(q, "&"), nil)
	if err != nil {
		return ErrResult(err.Error()), nil, nil
	}

	// The /messages endpoint returns plain text → we return it as string
	return OkResult(string(data)), nil, nil
}

// download_media (same idea)
func downloadMediaHandler(
	ctx context.Context,
	req *mcp.CallToolRequest,
	in downloadMediaInput,
) (*mcp.CallToolResult, map[string]any, error) {
	path, err := DownloadMedia(in.MessageID, in.ChatJid)
	if err != nil || path == "" {
		msg := "failed to download media"
		if err != nil {
			msg += ": " + err.Error()
		}
		return &mcp.CallToolResult{}, map[string]any{
			"success": false,
			"message": msg,
		}, nil
	}
	return &mcp.CallToolResult{}, map[string]any{
		"success":   true,
		"message":   "Media downloaded successfully",
		"file_path": path,
	}, nil
}

func getMessageContextHandler(
	ctx context.Context,
	req *mcp.CallToolRequest,
	in getMessageContextInput,
) (*mcp.CallToolResult, any, error) {
	if in.MessageID == "" {
		return ErrResult("message_id is required"), nil, nil
	}

	path := fmt.Sprintf("/messages/context/%s?before=%d&after=%d",
		in.MessageID, in.Before, in.After)

	data, err := callAPI(http.MethodGet, path, nil)
	if err != nil {
		return ErrResult(err.Error()), nil, nil
	}

	var ctxData map[string]any
	if err := json.Unmarshal(data, &ctxData); err != nil {
		return ErrResult("invalid context response"), nil, nil
	}

	return OkResult(ctxData), nil, nil
}

func listChatsHandler(
	ctx context.Context,
	req *mcp.CallToolRequest,
	in listChatsInput,
) (*mcp.CallToolResult, any, error) {
	q := fmt.Sprintf("?limit=%d&page=%d", in.Limit, in.Page)
	if in.Query != nil && *in.Query != "" {
		q += "&q=" + *in.Query
	}
	if in.SortBy != "" {
		q += "&sort=" + in.SortBy
	}

	data, err := callAPI(http.MethodGet, "/chats"+q, nil)
	if err != nil {
		return ErrResult(err.Error()), nil, nil
	}

	var result struct {
		Chats []map[string]any `json:"chats"`
		Count int              `json:"count"`
	}
	_ = json.Unmarshal(data, &result) // best effort

	return OkResult(result.Chats), nil, nil
}

func getChatHandler(
	ctx context.Context,
	req *mcp.CallToolRequest,
	in getChatInput,
) (*mcp.CallToolResult, any, error) {
	if in.ChatJid == "" {
		return ErrResult("chat_jid is required"), nil, nil
	}

	data, err := callAPI(http.MethodGet, "/chats/"+in.ChatJid, nil)
	if err != nil {
		return ErrResult(err.Error()), nil, nil
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return ErrResult("invalid chat response"), nil, nil
	}

	return OkResult(result["chat"]), nil, nil
}

func getDirectChatByContactHandler(
	ctx context.Context,
	req *mcp.CallToolRequest,
	in getDirectChatByContactInput,
) (*mcp.CallToolResult, any, error) {
	if in.SenderPhoneNumber == "" {
		return ErrResult("sender_phone_number is required"), nil, nil
	}

	// GET /api/direct-contacts/{phone}/chat
	path := fmt.Sprintf("/direct-contacts/%s/chat", in.SenderPhoneNumber)

	data, err := callAPI(http.MethodGet, path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{
					&mcp.TextContent{Text: "No direct (1:1) chat found for this phone number"},
				},
			}, nil, nil
		}
		return ErrResult(err.Error()), nil, nil
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return ErrResult("failed to parse chat response"), nil, nil
	}

	chat, ok := result["chat"]
	if !ok {
		return ErrResult("chat object missing in response"), nil, nil
	}

	return &mcp.CallToolResult{}, chat, nil
}

func getContactChatsHandler(
	ctx context.Context,
	req *mcp.CallToolRequest,
	in getContactChatsInput,
) (*mcp.CallToolResult, any, error) {
	if in.Jid == "" {
		return ErrResult("jid is required"), nil, nil
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	page := in.Page
	if page < 0 {
		page = 0
	}

	// GET /api/contacts/{jid}/chats?limit=...&page=...
	path := fmt.Sprintf("/contacts/%s/chats?limit=%d&page=%d", in.Jid, limit, page)

	data, err := callAPI(http.MethodGet, path, nil)
	if err != nil {
		return ErrResult(err.Error()), nil, nil
	}

	var result struct {
		Chats []map[string]any `json:"chats"`
		Count int              `json:"count"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return ErrResult("failed to parse chats response"), nil, nil
	}

	return &mcp.CallToolResult{}, result.Chats, nil
}

func getLastInteractionHandler(
	ctx context.Context,
	req *mcp.CallToolRequest,
	in getLastInteractionInput,
) (*mcp.CallToolResult, any, error) {
	if in.Jid == "" {
		return ErrResult("jid is required"), nil, nil
	}

	// We simulate it by asking for 1 message from that sender
	data, err := callAPI(http.MethodGet, "/messages?sender="+in.Jid+"&limit=1", nil)
	if err != nil {
		return ErrResult(err.Error()), nil, nil
	}

	return OkResult(string(data)), nil, nil
}

func sendFileHandler(ctx context.Context,
	req *mcp.CallToolRequest,
	in sendFileInput) (*mcp.CallToolResult, map[string]any, error) {

	if in.MediaPath == "" {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "media_path is required"}},
		}, nil, nil
	}

	absPath, err := filepath.Abs(in.MediaPath)
	if err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("invalid path: %v", err)}},
		}, nil, nil
	}

	success, msg := SendFile(in.Recipient, absPath)

	resultData := map[string]any{"success": success, "message": msg}

	return &mcp.CallToolResult{IsError: !success}, resultData, nil
}

func sendAudioMessageHandler(ctx context.Context,
	req *mcp.CallToolRequest,
	in sendAudioMessageInput) (*mcp.CallToolResult, map[string]any, error) {

	success, msg := SendAudioVoiceMessage(in.Recipient, in.MediaPath)

	resultData := map[string]any{"success": success, "message": msg}

	return &mcp.CallToolResult{IsError: !success}, resultData, nil
}

func getLoginStatusHandler(
	ctx context.Context,
	req *mcp.CallToolRequest,
	_ getLoginStatusInput,
) (*mcp.CallToolResult, any, error) {
	data, err := callAPI(http.MethodGet, "/auth/status", nil)
	if err != nil {
		return ErrResult(fmt.Sprintf("failed to fetch login status: %v", err)), nil, nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil, nil
}

func getPairingQrHandler(
	ctx context.Context,
	req *mcp.CallToolRequest,
	_ getPairingQrInput,
) (*mcp.CallToolResult, any, error) {
	token, err := GetOrRefreshJwtToken()
	if err != nil {
		return ErrResult(fmt.Sprintf("authentication failed: %v", err)), nil, nil
	}
	httpReq, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/auth/pairing-qr", apiBaseURL), nil)
	if err != nil {
		return ErrResult(fmt.Sprintf("failed to build request: %v", err)), nil, nil
	}
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	client := &http.Client{Timeout: apiTimeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return ErrResult(fmt.Sprintf("request failed: %v", err)), nil, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.ImageContent{Data: body, MIMEType: "image/png"}},
		}, nil, nil
	case http.StatusGone:
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "No pairing QR available. The bridge is either already logged in or has not started the pairing flow yet."}},
		}, nil, nil
	default:
		return ErrResult(fmt.Sprintf("unexpected status %d: %s", resp.StatusCode, string(body))), nil, nil
	}
}
