package main

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/socket"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	"github.com/mdp/qrterminal"

	"bytes"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

type MessageInteraction struct {
	Timestamp time.Time `json:"timestamp"`
	Sender    string    `json:"sender"`
	Content   string    `json:"content"`
	IsFromMe  bool      `json:"is_from_me"`
	ChatJID   string    `json:"chat_jid"`
	ID        string    `json:"id"`
	ChatName  string    `json:"chat_name,omitempty"`
	MediaType string    `json:"media_type,omitempty"`
}

type Chat struct {
	JID             string    `json:"jid"`
	Name            string    `json:"name,omitempty"`
	LastMessageTime time.Time `json:"last_message_time,omitempty"`
	LastMessage     string    `json:"last_message,omitempty"`
	LastSender      string    `json:"last_sender,omitempty"`
	LastIsFromMe    bool      `json:"last_is_from_me,omitempty"`
}

func (c *Chat) IsGroup() bool {
	return strings.HasSuffix(c.JID, "@g.us")
}

type Contact struct {
	PhoneNumber string `json:"phone_number"`
	Name        string `json:"name,omitempty"`
	JID         string `json:"jid"`
}

type MessageContext struct {
	Message MessageInteraction   `json:"message"`
	Before  []MessageInteraction `json:"before"`
	After   []MessageInteraction `json:"after"`
}

type ListMessagesParams struct {
	After, Before     string
	SenderPhoneNumber *string
	ChatJid           *string
	Query             *string
	Limit, Page       int
	IncludeContext    bool
	ContextBefore     int
	ContextAfter      int
}

type Message struct {
	Time      time.Time
	Sender    string
	Content   string
	IsFromMe  bool
	MediaType string
	Filename  string
}

type MessageStore struct {
	db *sql.DB
}

type dbConfig struct {
	User       string
	Pass       string
	Host       string
	Port       string
	IsPostgres bool
}

var isPostgres = false

func getEnv() (*dbConfig, error) {
	user, ok := os.LookupEnv("POSTGRES_USER")
	if !ok {
		return nil, fmt.Errorf("missing POSTGRES_USER")
	}
	pass, ok := os.LookupEnv("POSTGRES_PASS")
	if !ok {
		return nil, fmt.Errorf("missing POSTGRES_PASS")
	}
	host, ok := os.LookupEnv("POSTGRES_HOST")
	if !ok {
		return nil, fmt.Errorf("missing POSTGRES_HOST")
	}
	port, ok := os.LookupEnv("POSTGRES_PORT")
	if !ok {
		return nil, fmt.Errorf("missing POSTGRES_PORT")
	}
	IsPostgres, ok := os.LookupEnv("IS_POSTGRES")
	if !ok {
		IsPostgres = "false"
	}

	isPostgres = IsPostgres == "true"

	return &dbConfig{
		User:       user,
		Pass:       pass,
		Host:       host,
		Port:       port,
		IsPostgres: isPostgres,
	}, nil
}

func openDatabase(dbName string) (*sql.DB, error) {
	if val, ok := os.LookupEnv("IS_POSTGRES"); ok && strings.ToLower(val) == "true" {
		config, err := getEnv()
		if err != nil {
			return nil, fmt.Errorf("missing environment variable")
		}

		connStr := fmt.Sprintf("postgresql://%s:%s@%s:%s/%s?sslmode=disable", config.User, config.Pass, config.Host, config.Port, dbName)
		log.Println("Connecting to postgres")
		return sql.Open("postgres", connStr)
	}

	// Fallback to SQLite
	log.Println("Connecting to sqlite3")
	return sql.Open("sqlite3", "file:store/messages.db?_foreign_keys=on")
}

// NewMessageStore Initialize message store
func NewMessageStore() (*MessageStore, error) {
	if err := os.MkdirAll("store", 0755); err != nil {
		return nil, fmt.Errorf("failed to create store directory: %v", err)
	}

	db, err := openDatabase("whatsapp")
	if err != nil {
		return nil, fmt.Errorf("failed to open message database: %v", err)
	}

	val, ok := os.LookupEnv("IS_POSTGRES")

	var blobType string
	if ok && strings.ToLower(val) == "true" {
		blobType = "BYTEA"
	} else {
		blobType = "BLOB"
	}

	_, err = db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS chats (
			jid TEXT PRIMARY KEY,
			name TEXT,
			last_message_time TIMESTAMP
		);
		
		CREATE TABLE IF NOT EXISTS messages (
			id TEXT,
			chat_jid TEXT,
			sender TEXT,
			content TEXT,
			timestamp TIMESTAMP,
			is_from_me BOOLEAN,
			media_type TEXT,
			filename TEXT,
			url TEXT,
			media_key %s,
			file_sha256 %s,
			file_enc_sha256 %s,
			file_length INTEGER,
			PRIMARY KEY (id, chat_jid),
			FOREIGN KEY (chat_jid) REFERENCES chats(jid)
		);
	`, blobType, blobType, blobType))
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create tables: %v", err)
	}

	return &MessageStore{db: db}, nil
}

// Close the database connection
func (store *MessageStore) Close() error {
	return store.db.Close()
}

// StoreChat Store a chat in the database
func (store *MessageStore) StoreChat(jid, name string, lastMessageTime time.Time) error {
	if isPostgres {
		_, err := store.db.Exec(
			`INSERT INTO chats (jid, name, last_message_time) 
         VALUES ($1, $2, $3)
         ON CONFLICT (jid) DO UPDATE SET 
            name = EXCLUDED.name,
            last_message_time = EXCLUDED.last_message_time`,
			jid, name, lastMessageTime,
		)
		return err
	}
	_, err := store.db.Exec(
		"INSERT OR REPLACE INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		jid, name, lastMessageTime,
	)
	return err
}

// StoreMessage Store a message in the database
func (store *MessageStore) StoreMessage(id, chatJID, sender, content string, timestamp time.Time, isFromMe bool,
	mediaType, filename, url string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) error {
	if content == "" && mediaType == "" {
		return nil
	}

	if !isPostgres {
		_, err := store.db.Exec(
			`INSERT OR REPLACE INTO messages 
		(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length) 
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, chatJID, sender, content, timestamp, isFromMe, mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength,
		)
		return err
	}
	_, err := store.db.Exec(
		`INSERT INTO messages 
    (id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length) 
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
    ON CONFLICT(id, chat_jid) DO UPDATE SET 
    chat_jid = EXCLUDED.chat_jid,
    sender = EXCLUDED.sender,
    content = EXCLUDED.content,
    timestamp = EXCLUDED.timestamp,
    is_from_me = EXCLUDED.is_from_me,
    media_type = EXCLUDED.media_type,
    filename = EXCLUDED.filename,
    url = EXCLUDED.url,
    media_key = EXCLUDED.media_key,
    file_sha256 = EXCLUDED.file_sha256,
    file_enc_sha256 = EXCLUDED.file_enc_sha256,
    file_length = EXCLUDED.file_length`,
		id, chatJID, sender, content, timestamp, isFromMe, mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength,
	)

	return err
}

// GetMessages Get messages from a chat
func (store *MessageStore) GetMessages(chatJID string, limit int) ([]Message, error) {
	var rows *sql.Rows
	var err error

	if isPostgres {
		rows, err = store.db.Query(
			`SELECT sender, content, timestamp, is_from_me, media_type, filename 
					 FROM messages 
					 WHERE chat_jid = $1 
					 ORDER BY timestamp DESC 
					 LIMIT $2`,
			chatJID, limit,
		)
	} else {
		rows, err = store.db.Query(
			"SELECT sender, content, timestamp, is_from_me, media_type, filename FROM messages WHERE chat_jid = ? ORDER BY timestamp DESC LIMIT ?",
			chatJID, limit,
		)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		var timestamp time.Time
		err := rows.Scan(&msg.Sender, &msg.Content, &timestamp, &msg.IsFromMe, &msg.MediaType, &msg.Filename)
		if err != nil {
			return nil, err
		}
		msg.Time = timestamp
		messages = append(messages, msg)
	}

	return messages, nil
}

// GetChats Get all chats
func (store *MessageStore) GetChats() (map[string]time.Time, error) {
	rows, err := store.db.Query("SELECT jid, last_message_time FROM chats ORDER BY last_message_time DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	chats := make(map[string]time.Time)
	for rows.Next() {
		var jid string
		var lastMessageTime time.Time
		err := rows.Scan(&jid, &lastMessageTime)
		if err != nil {
			return nil, err
		}
		chats[jid] = lastMessageTime
	}

	return chats, nil
}

// Extract text content from a message
func extractTextContent(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}

	// Try to get text content
	if text := msg.GetConversation(); text != "" {
		return text
	} else if extendedText := msg.GetExtendedTextMessage(); extendedText != nil {
		return extendedText.GetText()
	}

	return ""
}

// SendMessageResponse represents the response for the send message API
type SendMessageResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// SendMessageRequest represents the request body for the send message API
type SendMessageRequest struct {
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
	MediaPath string `json:"media_path,omitempty"`
}

var clientVersionRegex = regexp.MustCompile(`"client_revision":(\d+),`)

func CustomGetLatestVersion(ctx context.Context, httpClient *http.Client) (*store.WAVersionContainer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, socket.Origin, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	} else if resp.StatusCode != 200 {
		return nil, fmt.Errorf("unexpected response with status %d: %s", resp.StatusCode, data)
	} else if match := clientVersionRegex.FindSubmatch(data); len(match) == 0 {
		return nil, fmt.Errorf("version number not found")
	} else if parsedVer, err := strconv.ParseInt(string(match[1]), 10, 64); err != nil {
		return nil, fmt.Errorf("failed to parse version number: %w", err)
	} else {
		return &store.WAVersionContainer{2, 3000, uint32(parsedVer)}, nil
	}
}

// Function to send a WhatsApp message
func sendWhatsAppMessage(client *whatsmeow.Client, recipient string, message string, mediaPath string) (bool, string) {
	if !client.IsConnected() {
		return false, "Not connected to WhatsApp"
	}

	var recipientJID types.JID
	var err error

	isJID := strings.Contains(recipient, "@")

	if isJID {
		recipientJID, err = types.ParseJID(recipient)
		if err != nil {
			return false, fmt.Sprintf("Error parsing JID: %v", err)
		}
	} else {
		recipientJID = types.JID{
			User:   recipient,
			Server: "s.whatsapp.net", // For personal chats
		}
	}

	msg := &waE2E.Message{}

	if mediaPath != "" {
		mediaData, err := os.ReadFile(mediaPath)
		if err != nil {
			return false, fmt.Sprintf("Error reading media file: %v", err)
		}

		fileExt := strings.ToLower(mediaPath[strings.LastIndex(mediaPath, ".")+1:])
		var mediaType whatsmeow.MediaType
		var mimeType string

		switch fileExt {
		case "jpg", "jpeg":
			mediaType = whatsmeow.MediaImage
			mimeType = "image/jpeg"
		case "png":
			mediaType = whatsmeow.MediaImage
			mimeType = "image/png"
		case "gif":
			mediaType = whatsmeow.MediaImage
			mimeType = "image/gif"
		case "webp":
			mediaType = whatsmeow.MediaImage
			mimeType = "image/webp"

		case "ogg":
			mediaType = whatsmeow.MediaAudio
			mimeType = "audio/ogg; codecs=opus"

		case "mp4":
			mediaType = whatsmeow.MediaVideo
			mimeType = "video/mp4"
		case "avi":
			mediaType = whatsmeow.MediaVideo
			mimeType = "video/avi"
		case "mov":
			mediaType = whatsmeow.MediaVideo
			mimeType = "video/quicktime"

		default:
			mediaType = whatsmeow.MediaDocument
			mimeType = "application/octet-stream"
		}

		resp, err := client.Upload(context.Background(), mediaData, mediaType)
		if err != nil {
			return false, fmt.Sprintf("Error uploading media: %v", err)
		}

		fmt.Println("Media uploaded", resp)

		switch mediaType {
		case whatsmeow.MediaImage:
			msg.ImageMessage = &waE2E.ImageMessage{
				Caption:       proto.String(message),
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
			}
		case whatsmeow.MediaAudio:
			var seconds uint32 = 30
			var waveform []byte = nil

			if strings.Contains(mimeType, "ogg") {
				analyzedSeconds, analyzedWaveform, err := analyzeOggOpus(mediaData)
				if err == nil {
					seconds = analyzedSeconds
					waveform = analyzedWaveform
				} else {
					return false, fmt.Sprintf("Failed to analyze Ogg Opus file: %v", err)
				}
			} else {
				fmt.Printf("Not an Ogg Opus file: %s\n", mimeType)
			}

			msg.AudioMessage = &waE2E.AudioMessage{
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
				Seconds:       proto.Uint32(seconds),
				PTT:           proto.Bool(true),
				Waveform:      waveform,
			}
		case whatsmeow.MediaVideo:
			msg.VideoMessage = &waE2E.VideoMessage{
				Caption:       proto.String(message),
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
			}
		case whatsmeow.MediaDocument:
			msg.DocumentMessage = &waE2E.DocumentMessage{
				Title:         proto.String(mediaPath[strings.LastIndex(mediaPath, "/")+1:]),
				Caption:       proto.String(message),
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
			}
		}
	} else {
		msg.Conversation = proto.String(message)
	}

	_, err = client.SendMessage(context.Background(), recipientJID, msg)

	if err != nil {
		return false, fmt.Sprintf("Error sending message: %v", err)
	}

	return true, fmt.Sprintf("Message sent to %s", recipient)
}

// Extract media info from a message
func extractMediaInfo(msg *waE2E.Message) (mediaType string, filename string, url string, mediaKey []byte, fileSHA256 []byte, fileEncSHA256 []byte, fileLength uint64) {
	if msg == nil {
		return "", "", "", nil, nil, nil, 0
	}

	if img := msg.GetImageMessage(); img != nil {
		return "image", "image_" + time.Now().Format("20060102_150405") + ".jpg",
			img.GetURL(), img.GetMediaKey(), img.GetFileSHA256(), img.GetFileEncSHA256(), img.GetFileLength()
	}

	if vid := msg.GetVideoMessage(); vid != nil {
		return "video", "video_" + time.Now().Format("20060102_150405") + ".mp4",
			vid.GetURL(), vid.GetMediaKey(), vid.GetFileSHA256(), vid.GetFileEncSHA256(), vid.GetFileLength()
	}

	if aud := msg.GetAudioMessage(); aud != nil {
		return "audio", "audio_" + time.Now().Format("20060102_150405") + ".ogg",
			aud.GetURL(), aud.GetMediaKey(), aud.GetFileSHA256(), aud.GetFileEncSHA256(), aud.GetFileLength()
	}

	if doc := msg.GetDocumentMessage(); doc != nil {
		filename := doc.GetFileName()
		if filename == "" {
			filename = "document_" + time.Now().Format("20060102_150405")
		}
		return "document", filename,
			doc.GetURL(), doc.GetMediaKey(), doc.GetFileSHA256(), doc.GetFileEncSHA256(), doc.GetFileLength()
	}

	return "", "", "", nil, nil, nil, 0
}

// Handle regular incoming messages with media support
func handleMessage(client *whatsmeow.Client, messageStore *MessageStore, msg *events.Message, logger waLog.Logger) {
	chatJID := msg.Info.Chat.String()
	sender := msg.Info.Sender.User

	name := GetChatName(client, messageStore, msg.Info.Chat, chatJID, nil, sender, logger)

	err := messageStore.StoreChat(chatJID, name, msg.Info.Timestamp)
	if err != nil {
		logger.Warnf("Failed to store chat: %v", err)
	}

	content := extractTextContent(msg.Message)

	mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength := extractMediaInfo(msg.Message)

	if content == "" && mediaType == "" {
		return
	}

	err = messageStore.StoreMessage(
		msg.Info.ID,
		chatJID,
		sender,
		content,
		msg.Info.Timestamp,
		msg.Info.IsFromMe,
		mediaType,
		filename,
		url,
		mediaKey,
		fileSHA256,
		fileEncSHA256,
		fileLength,
	)

	if err != nil {
		logger.Warnf("Failed to store message: %v", err)
	} else {
		timestamp := msg.Info.Timestamp.Format("2006-01-02 15:04:05")
		direction := "←"
		if msg.Info.IsFromMe {
			direction = "→"
		}

		if mediaType != "" {
			fmt.Printf("[%s] %s %s: [%s: %s] %s\n", timestamp, direction, sender, mediaType, filename, content)
		} else if content != "" {
			fmt.Printf("[%s] %s %s: %s\n", timestamp, direction, sender, content)
		}
	}
}

// DownloadMediaRequest represents the request body for the download media API
type DownloadMediaRequest struct {
	MessageID string `json:"message_id"`
	ChatJID   string `json:"chat_jid"`
}

// DownloadMediaResponse represents the response for the download media API
type DownloadMediaResponse struct {
	Success  bool   `json:"success"`
	Message  string `json:"message"`
	Filename string `json:"filename,omitempty"`
	Path     string `json:"path,omitempty"`
}

// StoreMediaInfo Store additional media info in the database
func (store *MessageStore) StoreMediaInfo(id, chatJID, url string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) error {
	if isPostgres {
		_, err := store.db.Exec(
			`UPDATE messages 
					 SET url = $1, 
						 media_key = $2, 
						 file_sha256 = $3, 
						 file_enc_sha256 = $4, 
						 file_length = $5 
					 WHERE id = $6 AND chat_jid = $7`,
			url, mediaKey, fileSHA256, fileEncSHA256, fileLength, id, chatJID,
		)
		return err
	}

	_, err := store.db.Exec(
		"UPDATE messages SET url = ?, media_key = ?, file_sha256 = ?, file_enc_sha256 = ?, file_length = ? WHERE id = ? AND chat_jid = ?",
		url, mediaKey, fileSHA256, fileEncSHA256, fileLength, id, chatJID,
	)
	return err
}

// GetMediaInfo Get media info from the database
func (store *MessageStore) GetMediaInfo(id, chatJID string) (string, string, string, []byte, []byte, []byte, uint64, error) {
	var mediaType, filename, url string
	var mediaKey, fileSHA256, fileEncSHA256 []byte
	var fileLength uint64
	var err error

	if isPostgres {
		err = store.db.QueryRow(
			`SELECT media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length 
					 FROM messages 
					 WHERE id = $1 AND chat_jid = $2`,
			id, chatJID,
		).Scan(&mediaType, &filename, &url, &mediaKey, &fileSHA256, &fileEncSHA256, &fileLength)
	} else {
		err = store.db.QueryRow(
			"SELECT media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length FROM messages WHERE id = ? AND chat_jid = ?",
			id, chatJID,
		).Scan(&mediaType, &filename, &url, &mediaKey, &fileSHA256, &fileEncSHA256, &fileLength)
	}

	return mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength, err
}

// MediaDownloader implements the whatsmeow.DownloadableMessage interface
type MediaDownloader struct {
	URL           string
	DirectPath    string
	MediaKey      []byte
	FileLength    uint64
	FileSHA256    []byte
	FileEncSHA256 []byte
	MediaType     whatsmeow.MediaType
}

// GetDirectPath implements the DownloadableMessage interface
func (d *MediaDownloader) GetDirectPath() string {
	return d.DirectPath
}

// GetURL implements the DownloadableMessage interface
func (d *MediaDownloader) GetURL() string {
	return d.URL
}

// GetMediaKey implements the DownloadableMessage interface
func (d *MediaDownloader) GetMediaKey() []byte {
	return d.MediaKey
}

// GetFileLength implements the DownloadableMessage interface
func (d *MediaDownloader) GetFileLength() uint64 {
	return d.FileLength
}

// GetFileSHA256 implements the DownloadableMessage interface
func (d *MediaDownloader) GetFileSHA256() []byte {
	return d.FileSHA256
}

// GetFileEncSHA256 implements the DownloadableMessage interface
func (d *MediaDownloader) GetFileEncSHA256() []byte {
	return d.FileEncSHA256
}

// GetMediaType implements the DownloadableMessage interface
func (d *MediaDownloader) GetMediaType() whatsmeow.MediaType {
	return d.MediaType
}

// Function to download media from a message
func downloadMedia(client *whatsmeow.Client, messageStore *MessageStore, messageID, chatJID string) (bool, string, string, string, error) {
	var mediaType, filename, url string
	var mediaKey, fileSHA256, fileEncSHA256 []byte
	var fileLength uint64
	var err error

	chatDir := fmt.Sprintf("store/%s", strings.ReplaceAll(chatJID, ":", "_"))
	localPath := ""

	mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength, err = messageStore.GetMediaInfo(messageID, chatJID)

	if err != nil {
		if isPostgres {
			err = messageStore.db.QueryRow(
				`SELECT media_type, filename FROM messages WHERE id = $1 AND chat_jid = $2`,
				messageID, chatJID,
			).Scan(&mediaType, &filename)
		} else {
			err = messageStore.db.QueryRow(
				"SELECT media_type, filename FROM messages WHERE id = ? AND chat_jid = ?",
				messageID, chatJID,
			).Scan(&mediaType, &filename)
		}

		if err != nil {
			return false, "", "", "", fmt.Errorf("failed to find message: %v", err)
		}
	}

	if mediaType == "" {
		return false, "", "", "", fmt.Errorf("not a media message")
	}

	if err := os.MkdirAll(chatDir, 0755); err != nil {
		return false, "", "", "", fmt.Errorf("failed to create chat directory: %v", err)
	}

	localPath = fmt.Sprintf("%s/%s", chatDir, filename)

	absPath, err := filepath.Abs(localPath)
	if err != nil {
		return false, "", "", "", fmt.Errorf("failed to get absolute path: %v", err)
	}

	if _, err := os.Stat(localPath); err == nil {
		return true, mediaType, filename, absPath, nil
	}

	if url == "" || len(mediaKey) == 0 || len(fileSHA256) == 0 || len(fileEncSHA256) == 0 || fileLength == 0 {
		return false, "", "", "", fmt.Errorf("incomplete media information for download")
	}

	fmt.Printf("Attempting to download media for message %s in chat %s...\n", messageID, chatJID)

	directPath := extractDirectPathFromURL(url)

	var waMediaType whatsmeow.MediaType
	switch mediaType {
	case "image":
		waMediaType = whatsmeow.MediaImage
	case "video":
		waMediaType = whatsmeow.MediaVideo
	case "audio":
		waMediaType = whatsmeow.MediaAudio
	case "document":
		waMediaType = whatsmeow.MediaDocument
	default:
		return false, "", "", "", fmt.Errorf("unsupported media type: %s", mediaType)
	}

	downloader := &MediaDownloader{
		URL:           url,
		DirectPath:    directPath,
		MediaKey:      mediaKey,
		FileLength:    fileLength,
		FileSHA256:    fileSHA256,
		FileEncSHA256: fileEncSHA256,
		MediaType:     waMediaType,
	}

	mediaData, err := client.Download(context.Background(), downloader)
	if err != nil {
		return false, "", "", "", fmt.Errorf("failed to download media: %v", err)
	}

	if err := os.WriteFile(localPath, mediaData, 0644); err != nil {
		return false, "", "", "", fmt.Errorf("failed to save media file: %v", err)
	}

	fmt.Printf("Successfully downloaded %s media to %s (%d bytes)\n", mediaType, absPath, len(mediaData))
	return true, mediaType, filename, absPath, nil
}

func extractDirectPathFromURL(url string) string {
	// The direct path is typically in the URL, we need to extract it
	// Example URL: https://mmg.whatsapp.net/v/t62.7118-24/13812002_698058036224062_3424455886509161511_n.enc?ccb=11-4&oh=...

	parts := strings.SplitN(url, ".net/", 2)
	if len(parts) < 2 {
		return url // Return original URL if parsing fails
	}

	pathPart := parts[1]

	pathPart = strings.SplitN(pathPart, "?", 2)[0]

	return "/" + pathPart
}

// Start a REST API server to expose the WhatsApp client functionality
func startRESTServer(client *whatsmeow.Client, messageStore *MessageStore, port int) {
	http.HandleFunc("/api/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req SendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}

		if req.Recipient == "" {
			http.Error(w, "Recipient is required", http.StatusBadRequest)
			return
		}

		if req.Message == "" && req.MediaPath == "" {
			http.Error(w, "Message or media path is required", http.StatusBadRequest)
			return
		}

		fmt.Println("Received request to send message", req.Message, req.MediaPath)

		success, message := sendWhatsAppMessage(client, req.Recipient, req.Message, req.MediaPath)
		fmt.Println("Message sent", success, message)
		w.Header().Set("Content-Type", "application/json")

		if !success {
			w.WriteHeader(http.StatusInternalServerError)
		}

		json.NewEncoder(w).Encode(SendMessageResponse{
			Success: success,
			Message: message,
		})
	})

	// Handler for downloading media
	http.HandleFunc("/api/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req DownloadMediaRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}

		if req.MessageID == "" || req.ChatJID == "" {
			http.Error(w, "Message ID and Chat JID are required", http.StatusBadRequest)
			return
		}

		success, mediaType, filename, path, err := downloadMedia(client, messageStore, req.MessageID, req.ChatJID)

		w.Header().Set("Content-Type", "application/json")

		if !success || err != nil {
			errMsg := "Unknown error"
			if err != nil {
				errMsg = err.Error()
			}

			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(DownloadMediaResponse{
				Success: false,
				Message: fmt.Sprintf("Failed to download media: %s", errMsg),
			})
			return
		}

		json.NewEncoder(w).Encode(DownloadMediaResponse{
			Success:  true,
			Message:  fmt.Sprintf("Successfully downloaded %s media", mediaType),
			Filename: filename,
			Path:     path,
		})
	})

	// List recent chats
	http.HandleFunc("/api/chats", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		q := r.URL.Query().Get("q")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		sortBy := r.URL.Query().Get("sort")

		if limit < 1 {
			limit = 30
		}
		if page < 0 {
			page = 0
		}

		var queryPtr *string
		if q != "" {
			queryPtr = &q
		}

		chats, err := messageStore.ListChats(queryPtr, limit, page, true, sortBy)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"chats": chats,
			"count": len(chats),
		})
	})

	// Get single chat
	http.HandleFunc("/api/chats/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		jid := strings.TrimPrefix(r.URL.Path, "/api/chats/")
		if jid == "" {
			http.Error(w, "Missing chat JID", http.StatusBadRequest)
			return
		}

		chat, err := messageStore.GetChat(jid, true)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if chat == nil {
			http.Error(w, "Chat not found", http.StatusNotFound)
			return
		}

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"chat": chat,
		})
	})

	// List messages (very flexible)
	http.HandleFunc("/api/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		params := ListMessagesParams{
			Limit:          50,
			Page:           0,
			IncludeContext: false,
			ContextBefore:  5,
			ContextAfter:   5,
		}

		q := r.URL.Query()
		if after := q.Get("after"); after != "" {
			params.After = after
		}
		if before := q.Get("before"); before != "" {
			params.Before = before
		}
		if chat := q.Get("chat"); chat != "" {
			params.ChatJid = &chat
		}
		if sender := q.Get("sender"); sender != "" {
			params.SenderPhoneNumber = &sender
		}
		if search := q.Get("search"); search != "" {
			params.Query = &search
		}
		if lim := q.Get("limit"); lim != "" {
			if v, err := strconv.Atoi(lim); err == nil && v > 0 {
				params.Limit = v
			}
		}
		if pg := q.Get("page"); pg != "" {
			if v, err := strconv.Atoi(pg); err == nil && v >= 0 {
				params.Page = v
			}
		}
		if ctx := q.Get("context"); ctx == "true" {
			params.IncludeContext = true
		}

		result, err := messageStore.ListMessages(params)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"result": result,
		})
	})

	// Get message + context
	http.HandleFunc("/api/messages/context/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		messageID := strings.TrimPrefix(r.URL.Path, "/api/messages/context/")
		if messageID == "" {
			http.Error(w, "Missing message ID", http.StatusBadRequest)
			return
		}

		before, _ := strconv.Atoi(r.URL.Query().Get("before"))
		after, _ := strconv.Atoi(r.URL.Query().Get("after"))
		if before == 0 {
			before = 6
		}
		if after == 0 {
			after = 6
		}

		ctx, err := messageStore.GetMessageContext(messageID, before, after)
		if err != nil {
			respondError(w, http.StatusNotFound, err.Error())
			return
		}

		respondJSON(w, http.StatusOK, ctx)
	})

	// Search contacts
	http.HandleFunc("/api/contacts/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		query := r.URL.Query().Get("q")
		if query == "" {
			http.Error(w, "Missing search query (?q=...)", http.StatusBadRequest)
			return
		}

		contacts, err := messageStore.SearchContacts(query)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"contacts": contacts,
		})
	})

	// GET /api/contacts/:phone/chat
	// Find the 1:1 (direct) chat for a given phone number
	http.HandleFunc("/api/direct-contacts/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/api/direct-contacts/")
		parts := strings.Split(path, "/")
		if len(parts) < 2 || parts[1] != "chat" {
			http.Error(w, "Invalid path. Use /api/direct-contacts/{phone}/chat", http.StatusBadRequest)
			return
		}

		phone := parts[0]
		if phone == "" {
			http.Error(w, "Phone number is required", http.StatusBadRequest)
			return
		}

		chat, err := messageStore.GetDirectChatByContact(phone)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if chat == nil {
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "No direct chat found for this phone number",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"chat": chat,
		})
	})

	// GET /api/contacts/:jid/chats
	// List all chats where this contact (by JID) appears as sender or in group
	http.HandleFunc("/api/contacts/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/api/contacts/")
		parts := strings.Split(path, "/")
		if len(parts) < 2 || parts[1] != "chats" {
			// Skip if not this endpoint (previous handler already took /chat)
			return
		}

		jid := parts[0]
		if jid == "" {
			http.Error(w, "JID is required", http.StatusBadRequest)
			return
		}

		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if limit <= 0 {
			limit = 20
		}
		if page < 0 {
			page = 0
		}

		chats, err := messageStore.GetContactChats(jid, limit, page)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"chats": chats,
			"count": len(chats),
		})
	})

	serverAddr := fmt.Sprintf(":%d", port)
	fmt.Printf("Starting REST API server on %s...\n", serverAddr)

	go func() {
		if err := http.ListenAndServe(serverAddr, nil); err != nil {
			fmt.Printf("REST API server error: %v\n", err)
		}
	}()
}

func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, status int, msg string) {
	respondJSON(w, status, map[string]string{
		"error": msg,
	})
}

// GetChatName determines the appropriate name for a chat based on JID and other info
func GetChatName(client *whatsmeow.Client, messageStore *MessageStore, jid types.JID, chatJID string, conversation interface{}, sender string, logger waLog.Logger) string {
	var existingName string
	err := messageStore.db.QueryRow("SELECT name FROM chats WHERE jid = ?", chatJID).Scan(&existingName)
	if err == nil && existingName != "" {
		logger.Infof("Using existing chat name for %s: %s", chatJID, existingName)
		return existingName
	}

	var name string

	if jid.Server == "g.us" {
		logger.Infof("Getting name for group: %s", chatJID)

		if conversation != nil {
			var displayName, convName *string
			v := reflect.ValueOf(conversation)
			if v.Kind() == reflect.Ptr && !v.IsNil() {
				v = v.Elem()

				if displayNameField := v.FieldByName("DisplayName"); displayNameField.IsValid() && displayNameField.Kind() == reflect.Ptr && !displayNameField.IsNil() {
					dn := displayNameField.Elem().String()
					displayName = &dn
				}

				if nameField := v.FieldByName("Name"); nameField.IsValid() && nameField.Kind() == reflect.Ptr && !nameField.IsNil() {
					n := nameField.Elem().String()
					convName = &n
				}
			}

			if displayName != nil && *displayName != "" {
				name = *displayName
			} else if convName != nil && *convName != "" {
				name = *convName
			}
		}

		if name == "" {
			groupInfo, err := client.GetGroupInfo(context.Background(), jid)
			if err == nil && groupInfo.Name != "" {
				name = groupInfo.Name
			} else {
				name = fmt.Sprintf("Group %s", jid.User)
			}
		}

		logger.Infof("Using group name: %s", name)
	} else {
		logger.Infof("Getting name for contact: %s", chatJID)

		contact, err := client.Store.Contacts.GetContact(context.Background(), jid)
		if err == nil && contact.FullName != "" {
			name = contact.FullName
		} else if sender != "" {
			name = sender
		} else {
			name = jid.User
		}

		logger.Infof("Using contact name: %s", name)
	}

	return name
}

// Handle history sync events
func handleHistorySync(client *whatsmeow.Client, messageStore *MessageStore, historySync *events.HistorySync, logger waLog.Logger) {
	fmt.Printf("Received history sync event with %d conversations\n", len(historySync.Data.Conversations))

	syncedCount := 0
	for _, conversation := range historySync.Data.Conversations {
		if conversation.ID == nil {
			continue
		}

		chatJID := *conversation.ID

		jid, err := types.ParseJID(chatJID)
		if err != nil {
			logger.Warnf("Failed to parse JID %s: %v", chatJID, err)
			continue
		}

		name := GetChatName(client, messageStore, jid, chatJID, conversation, "", logger)

		messages := conversation.Messages
		if len(messages) > 0 {
			latestMsg := messages[0]
			if latestMsg == nil || latestMsg.Message == nil {
				continue
			}

			timestamp := time.Time{}
			if ts := latestMsg.Message.GetMessageTimestamp(); ts != 0 {
				timestamp = time.Unix(int64(ts), 0)
			} else {
				continue
			}

			messageStore.StoreChat(chatJID, name, timestamp)

			for _, msg := range messages {
				if msg == nil || msg.Message == nil {
					continue
				}

				var content string
				if msg.Message.Message != nil {
					if conv := msg.Message.Message.GetConversation(); conv != "" {
						content = conv
					} else if ext := msg.Message.Message.GetExtendedTextMessage(); ext != nil {
						content = ext.GetText()
					}
				}

				var mediaType, filename, url string
				var mediaKey, fileSHA256, fileEncSHA256 []byte
				var fileLength uint64

				if msg.Message.Message != nil {
					mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength = extractMediaInfo(msg.Message.Message)
				}

				logger.Infof("Message content: %v, Media Type: %v", content, mediaType)

				if content == "" && mediaType == "" {
					continue
				}

				var sender string
				isFromMe := false
				if msg.Message.Key != nil {
					if msg.Message.Key.FromMe != nil {
						isFromMe = *msg.Message.Key.FromMe
					}
					if !isFromMe && msg.Message.Key.Participant != nil && *msg.Message.Key.Participant != "" {
						sender = *msg.Message.Key.Participant
					} else if isFromMe {
						sender = client.Store.ID.User
					} else {
						sender = jid.User
					}
				} else {
					sender = jid.User
				}

				msgID := ""
				if msg.Message.Key != nil && msg.Message.Key.ID != nil {
					msgID = *msg.Message.Key.ID
				}

				timestamp := time.Time{}
				if ts := msg.Message.GetMessageTimestamp(); ts != 0 {
					timestamp = time.Unix(int64(ts), 0)
				} else {
					continue
				}

				err = messageStore.StoreMessage(
					msgID,
					chatJID,
					sender,
					content,
					timestamp,
					isFromMe,
					mediaType,
					filename,
					url,
					mediaKey,
					fileSHA256,
					fileEncSHA256,
					fileLength,
				)
				if err != nil {
					logger.Warnf("Failed to store history message: %v", err)
				} else {
					syncedCount++
					if mediaType != "" {
						logger.Infof("Stored message: [%s] %s -> %s: [%s: %s] %s",
							timestamp.Format("2006-01-02 15:04:05"), sender, chatJID, mediaType, filename, content)
					} else {
						logger.Infof("Stored message: [%s] %s -> %s: %s",
							timestamp.Format("2006-01-02 15:04:05"), sender, chatJID, content)
					}
				}
			}
		}
	}

	fmt.Printf("History sync complete. Stored %d messages.\n", syncedCount)
}

// Request history sync from the server
func requestHistorySync(client *whatsmeow.Client) {
	if client == nil {
		fmt.Println("Client is not initialized. Cannot request history sync.")
		return
	}

	if !client.IsConnected() {
		fmt.Println("Client is not connected. Please ensure you are connected to WhatsApp first.")
		return
	}

	if client.Store.ID == nil {
		fmt.Println("Client is not logged in. Please scan the QR code first.")
		return
	}

	historyMsg := client.BuildHistorySyncRequest(nil, 100)
	if historyMsg == nil {
		fmt.Println("Failed to build history sync request.")
		return
	}

	_, err := client.SendMessage(context.Background(), types.JID{
		Server: "s.whatsapp.net",
		User:   "status",
	}, historyMsg)

	if err != nil {
		fmt.Printf("Failed to request history sync: %v\n", err)
	} else {
		fmt.Println("History sync requested. Waiting for server response...")
	}
}

// analyzeOggOpus tries to extract duration and generate a simple waveform from an Ogg Opus file
func analyzeOggOpus(data []byte) (duration uint32, waveform []byte, err error) {
	// Try to detect if this is a valid Ogg file by checking for the "OggS" signature
	// at the beginning of the file
	if len(data) < 4 || string(data[0:4]) != "OggS" {
		return 0, nil, fmt.Errorf("not a valid Ogg file (missing OggS signature)")
	}

	// Parse Ogg pages to find the last page with a valid granule position
	var lastGranule uint64
	var sampleRate uint32 = 48000 // Default Opus sample rate
	var preSkip uint16 = 0
	var foundOpusHead bool

	for i := 0; i < len(data); {
		if i+27 >= len(data) {
			break
		}

		if string(data[i:i+4]) != "OggS" {
			i++
			continue
		}

		granulePos := binary.LittleEndian.Uint64(data[i+6 : i+14])
		pageSeqNum := binary.LittleEndian.Uint32(data[i+18 : i+22])
		numSegments := int(data[i+26])

		if i+27+numSegments >= len(data) {
			break
		}
		segmentTable := data[i+27 : i+27+numSegments]

		pageSize := 27 + numSegments
		for _, segLen := range segmentTable {
			pageSize += int(segLen)
		}

		if !foundOpusHead && pageSeqNum <= 1 {
			pageData := data[i : i+pageSize]
			headPos := bytes.Index(pageData, []byte("OpusHead"))
			if headPos >= 0 && headPos+12 < len(pageData) {
				headPos += 8
				if headPos+12 <= len(pageData) {
					preSkip = binary.LittleEndian.Uint16(pageData[headPos+10 : headPos+12])
					sampleRate = binary.LittleEndian.Uint32(pageData[headPos+12 : headPos+16])
					foundOpusHead = true
					fmt.Printf("Found OpusHead: sampleRate=%d, preSkip=%d\n", sampleRate, preSkip)
				}
			}
		}

		if granulePos != 0 {
			lastGranule = granulePos
		}

		i += pageSize
	}

	if !foundOpusHead {
		fmt.Println("Warning: OpusHead not found, using default values")
	}

	if lastGranule > 0 {
		durationSeconds := float64(lastGranule-uint64(preSkip)) / float64(sampleRate)
		duration = uint32(math.Ceil(durationSeconds))
		fmt.Printf("Calculated Opus duration from granule: %f seconds (lastGranule=%d)\n",
			durationSeconds, lastGranule)
	} else {
		fmt.Println("Warning: No valid granule position found, using estimation")
		durationEstimate := float64(len(data)) / 2000.0
		duration = uint32(durationEstimate)
	}

	if duration < 1 {
		duration = 1
	} else if duration > 300 {
		duration = 300
	}

	waveform = placeholderWaveform(duration)

	fmt.Printf("Ogg Opus analysis: size=%d bytes, calculated duration=%d sec, waveform=%d bytes\n",
		len(data), duration, len(waveform))

	return duration, waveform, nil
}

func placeholderWaveform(duration uint32) []byte {
	const waveformLength = 64
	waveform := make([]byte, waveformLength)

	source := rand.NewSource(int64(duration))
	rng := rand.New(source)

	baseAmplitude := 35.0
	frequencyFactor := float64(min(int(duration), 120)) / 30.0

	for i := range waveform {
		pos := float64(i) / float64(waveformLength)

		val := baseAmplitude * math.Sin(pos*math.Pi*frequencyFactor*8)
		val += (baseAmplitude / 2) * math.Sin(pos*math.Pi*frequencyFactor*16)

		val += (rng.Float64() - 0.5) * 15

		fadeInOut := math.Sin(pos * math.Pi)
		val = val * (0.7 + 0.3*fadeInOut)

		val = val + 50

		if val < 0 {
			val = 0
		} else if val > 100 {
			val = 100
		}

		waveform[i] = byte(val)
	}

	return waveform
}

func (store *MessageStore) GetSenderName(senderJID string) string {
	var name string
	var err error

	if isPostgres {
		err = store.db.QueryRow(
			"SELECT name FROM chats WHERE jid = $1 LIMIT 1",
			senderJID,
		).Scan(&name)

		if err == nil && name != "" {
			return name
		}

		phonePart := senderJID
		if idx := strings.Index(senderJID, "@"); idx > 0 {
			phonePart = senderJID[:idx]
		}

		err = store.db.QueryRow(
			"SELECT name FROM chats WHERE jid LIKE $1 LIMIT 1",
			"%"+phonePart+"%",
		).Scan(&name)

		if err == nil && name != "" {
			return name
		}

		return senderJID
	}

	err = store.db.QueryRow(
		"SELECT name FROM chats WHERE jid = ? LIMIT 1",
		senderJID,
	).Scan(&name)

	if err == nil && name != "" {
		return name
	}

	phonePart := senderJID
	if idx := strings.Index(senderJID, "@"); idx > 0 {
		phonePart = senderJID[:idx]
	}

	err = store.db.QueryRow(
		"SELECT name FROM chats WHERE jid LIKE ? LIMIT 1",
		"%"+phonePart+"%",
	).Scan(&name)

	if err == nil && name != "" {
		return name
	}

	return senderJID
}

func (store *MessageStore) FormatMessage(msg MessageInteraction, showChatInfo bool) string {
	var sb strings.Builder

	ts := msg.Timestamp.Format("2006-01-02 15:04:05")

	if showChatInfo && msg.ChatName != "" {
		sb.WriteString(fmt.Sprintf("[%s] Chat: %s ", ts, msg.ChatName))
	} else {
		sb.WriteString(fmt.Sprintf("[%s] ", ts))
	}

	prefix := ""
	if msg.MediaType != "" {
		prefix = fmt.Sprintf("[%s - Message ID: %s - Chat JID: %s] ", msg.MediaType, msg.ID, msg.ChatJID)
	}

	senderName := "Me"
	if !msg.IsFromMe {
		senderName = store.GetSenderName(msg.Sender)
	}

	sb.WriteString(fmt.Sprintf("From: %s: %s%s\n", senderName, prefix, msg.Content))
	return sb.String()
}

func (store *MessageStore) FormatMessagesList(messages []MessageInteraction, showChatInfo bool) string {
	if len(messages) == 0 {
		return "No messages to display.\n"
	}
	var sb strings.Builder
	for _, m := range messages {
		sb.WriteString(store.FormatMessage(m, showChatInfo))
	}
	return sb.String()
}

func (store *MessageStore) ListMessages(s ListMessagesParams) (string, error) {
	var args []any
	var where []string

	placeholder := func(n int) string {
		if isPostgres {
			return fmt.Sprintf("$%d", n)
		}
		return "?"
	}

	q := `
        SELECT 
            m.timestamp, m.sender, c.name, m.content, m.is_from_me, 
            c.jid, m.id, m.media_type
        FROM messages m
        JOIN chats c ON m.chat_jid = c.jid
    `

	if s.After != "" {
		t, err := time.Parse(time.RFC3339, s.After)
		if err != nil {
			return "", fmt.Errorf("invalid after format: %w", err)
		}
		where = append(where, "m.timestamp > "+placeholder(len(args)+1))
		args = append(args, t)
	}

	if s.Before != "" {
		t, err := time.Parse(time.RFC3339, s.Before)
		if err != nil {
			return "", fmt.Errorf("invalid before format: %w", err)
		}
		where = append(where, "m.timestamp < "+placeholder(len(args)+1))
		args = append(args, t)
	}

	if s.SenderPhoneNumber != nil && *s.SenderPhoneNumber != "" {
		where = append(where, "m.sender = "+placeholder(len(args)+1))
		args = append(args, *s.SenderPhoneNumber)
	}

	if s.ChatJid != nil && *s.ChatJid != "" {
		where = append(where, "m.chat_jid = "+placeholder(len(args)+1))
		args = append(args, *s.ChatJid)
	}

	if s.Query != nil && *s.Query != "" {
		where = append(where, "LOWER(m.content) LIKE LOWER("+placeholder(len(args)+1)+")")
		args = append(args, "%"+*s.Query+"%")
	}

	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}

	q += " ORDER BY m.timestamp DESC"
	q += " LIMIT " + placeholder(len(args)+1)
	args = append(args, s.Limit)

	q += " OFFSET " + placeholder(len(args)+1)
	args = append(args, s.Page*s.Limit)

	rows, err := store.db.Query(q, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var msgs []MessageInteraction
	for rows.Next() {
		var (
			ts        time.Time
			sender    string
			chatName  sql.NullString
			content   string
			isFromMe  bool
			cJid      string
			id        string
			mediaType sql.NullString
		)

		if err := rows.Scan(&ts, &sender, &chatName, &content, &isFromMe, &cJid, &id, &mediaType); err != nil {
			log.Printf("scan error: %v", err)
			continue
		}

		m := MessageInteraction{
			Timestamp: ts,
			Sender:    sender,
			Content:   content,
			IsFromMe:  isFromMe,
			ChatJID:   cJid,
			ID:        id,
		}

		if chatName.Valid {
			m.ChatName = chatName.String
		}
		if mediaType.Valid {
			m.MediaType = mediaType.String
		}

		msgs = append(msgs, m)
	}

	if s.IncludeContext && len(msgs) > 0 {
		var all []MessageInteraction
		for _, m := range msgs {
			ctx, err := store.GetMessageContext(m.ID, s.ContextBefore, s.ContextAfter)
			if err != nil {
				log.Printf("context error for %s: %v", m.ID, err)
				continue
			}
			all = append(all, ctx.Before...)
			all = append(all, ctx.Message)
			all = append(all, ctx.After...)
		}
		return store.FormatMessagesList(all, true), nil
	}

	return store.FormatMessagesList(msgs, true), nil
}

func (store *MessageStore) GetMessageContext(messageID string, before, after int) (MessageContext, error) {
	placeholder := func(n int) string {
		if isPostgres {
			return fmt.Sprintf("$%d", n)
		}
		return "?"
	}

	// --- Fetch the target message ---
	q := `
        SELECT m.timestamp, m.sender, c.name, m.content, m.is_from_me,
               c.jid, m.id, m.media_type, m.chat_jid
        FROM messages m
        JOIN chats c ON m.chat_jid = c.jid
        WHERE m.id = ` + placeholder(1)

	var (
		ts       time.Time
		sender   string
		chatName sql.NullString
		content  string
		isFromMe bool
		chatJid  string
		id       string
		media    sql.NullString
		chatJid2 string
	)

	row := store.db.QueryRow(q, messageID)
	if err := row.Scan(&ts, &sender, &chatName, &content, &isFromMe, &chatJid, &id, &media, &chatJid2); err != nil {
		if err == sql.ErrNoRows {
			return MessageContext{}, fmt.Errorf("message not found: %s", messageID)
		}
		return MessageContext{}, err
	}

	target := MessageInteraction{
		Timestamp: ts,
		Sender:    sender,
		Content:   content,
		IsFromMe:  isFromMe,
		ChatJID:   chatJid,
		ID:        id,
	}
	if chatName.Valid {
		target.ChatName = chatName.String
	}
	if media.Valid {
		target.MediaType = media.String
	}

	qBefore := `
        SELECT m.timestamp, m.sender, c.name, m.content, m.is_from_me,
               c.jid, m.id, m.media_type
        FROM messages m
        JOIN chats c ON m.chat_jid = c.jid
        WHERE m.chat_jid = ` + placeholder(1) + `
          AND m.timestamp < ` + placeholder(2) + `
        ORDER BY m.timestamp DESC
        LIMIT ` + placeholder(3)

	bRows, err := store.db.Query(qBefore, chatJid2, ts, before)
	if err != nil {
		return MessageContext{}, err
	}
	defer bRows.Close()

	var beforeMsgs []MessageInteraction
	for bRows.Next() {
		var (
			bts      time.Time
			bsender  sql.NullString
			bname    sql.NullString
			bcontent string
			bif      bool
			bjid     string
			bid      string
			bmedia   sql.NullString
		)

		if err := bRows.Scan(&bts, &bsender, &bname, &bcontent, &bif, &bjid, &bid, &bmedia); err != nil {
			continue
		}

		m := MessageInteraction{
			Timestamp: bts,
			Sender:    bsender.String,
			Content:   bcontent,
			IsFromMe:  bif,
			ChatJID:   bjid,
			ID:        bid,
		}
		if bname.Valid {
			m.ChatName = bname.String
		}
		if bmedia.Valid {
			m.MediaType = bmedia.String
		}
		beforeMsgs = append(beforeMsgs, m)
	}

	qAfter := `
        SELECT m.timestamp, m.sender, c.name, m.content, m.is_from_me,
               c.jid, m.id, m.media_type
        FROM messages m
        JOIN chats c ON m.chat_jid = c.jid
        WHERE m.chat_jid = ` + placeholder(1) + `
          AND m.timestamp > ` + placeholder(2) + `
        ORDER BY m.timestamp ASC
        LIMIT ` + placeholder(3)

	aRows, err := store.db.Query(qAfter, chatJid2, ts, after)
	if err != nil {
		return MessageContext{}, err
	}
	defer aRows.Close()

	var afterMsgs []MessageInteraction
	for aRows.Next() {
		var (
			ats      time.Time
			asender  sql.NullString
			aname    sql.NullString
			acontent string
			aif      bool
			ajid     string
			aid      string
			amedia   sql.NullString
		)

		if err := aRows.Scan(&ats, &asender, &aname, &acontent, &aif, &ajid, &aid, &amedia); err != nil {
			continue
		}

		m := MessageInteraction{
			Timestamp: ats,
			Sender:    asender.String,
			Content:   acontent,
			IsFromMe:  aif,
			ChatJID:   ajid,
			ID:        aid,
		}
		if aname.Valid {
			m.ChatName = aname.String
		}
		if amedia.Valid {
			m.MediaType = amedia.String
		}
		afterMsgs = append(afterMsgs, m)
	}

	return MessageContext{
		Message: target,
		Before:  beforeMsgs,
		After:   afterMsgs,
	}, nil
}

func (store *MessageStore) ListChats(
	query *string,
	limit, page int,
	includeLastMessage bool,
	sortBy string,
) ([]Chat, error) {

	placeholder := func(n int) string {
		if isPostgres {
			return fmt.Sprintf("$%d", n)
		}
		return "?"
	}

	q := `
        SELECT 
            c.jid, c.name, c.last_message_time,
            m.content AS last_message,
            m.sender AS last_sender,
            m.is_from_me AS last_is_from_me
        FROM chats c
    `

	if includeLastMessage {
		q += `
            LEFT JOIN messages m 
            ON c.jid = m.chat_jid 
            AND c.last_message_time = m.timestamp
        `
	}

	var args []any
	var where []string

	if query != nil && *query != "" {
		if isPostgres {
			where = append(where, "(LOWER(c.name) LIKE LOWER("+placeholder(len(args)+1)+") OR c.jid LIKE "+placeholder(len(args)+2)+")")
		} else {
			where = append(where, "(LOWER(c.name) LIKE LOWER(?) OR c.jid LIKE ?)")
		}
		args = append(args, "%"+*query+"%", "%"+*query+"%")
	}

	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}

	order := "c.last_message_time DESC"
	if sortBy == "name" {
		order = "c.name ASC"
	}
	q += " ORDER BY " + order

	q += " LIMIT " + placeholder(len(args)+1)
	args = append(args, limit)

	q += " OFFSET " + placeholder(len(args)+2)
	args = append(args, page*limit)

	rows, err := store.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chats []Chat
	for rows.Next() {
		var (
			jid         string
			name        sql.NullString
			lastMsgTime sql.NullTime
			lastMsg     sql.NullString
			lastSender  sql.NullString
			lastFromMe  sql.NullBool
		)

		if err := rows.Scan(&jid, &name, &lastMsgTime, &lastMsg, &lastSender, &lastFromMe); err != nil {
			log.Printf("list_chats scan error: %v", err)
			continue
		}

		c := Chat{JID: jid}

		if name.Valid {
			c.Name = name.String
		}
		if lastMsgTime.Valid {
			c.LastMessageTime = lastMsgTime.Time
		}
		if lastMsg.Valid {
			c.LastMessage = lastMsg.String
		}
		if lastSender.Valid {
			c.LastSender = lastSender.String
		}
		if lastFromMe.Valid {
			c.LastIsFromMe = lastFromMe.Bool
		}

		chats = append(chats, c)
	}

	return chats, nil
}

func (store *MessageStore) SearchContacts(query string) ([]Contact, error) {
	placeholder := func(n int) string {
		if isPostgres {
			return fmt.Sprintf("$%d", n)
		}
		return "?"
	}

	q := `
        SELECT DISTINCT their_jid, first_name
        FROM whatsmeow_contacts
        WHERE (LOWER(first_name) LIKE LOWER(` + placeholder(1) + `)
           OR LOWER(their_jid) LIKE LOWER(` + placeholder(2) + `))
          AND their_jid NOT LIKE '%@g.us'
        ORDER BY first_name, their_jid
        LIMIT 50
    `

	args := []any{"%" + query + "%", "%" + query + "%"}

	rows, err := store.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contacts []Contact

	for rows.Next() {
		var jid, name sql.NullString

		if err := rows.Scan(&jid, &name); err != nil {
			continue
		}
		if !jid.Valid {
			continue
		}

		phone := strings.Split(jid.String, "@")[0]

		c := Contact{
			PhoneNumber: phone,
			JID:         jid.String,
		}
		if name.Valid {
			c.Name = name.String
		}

		contacts = append(contacts, c)
	}

	return contacts, nil
}

func (store *MessageStore) GetContactChats(jid string, limit, page int) ([]Chat, error) {
	placeholder := func(n int) string {
		if isPostgres {
			return fmt.Sprintf("$%d", n)
		}
		return "?"
	}

	q := `
        SELECT DISTINCT
            c.jid, c.name, c.last_message_time,
            m.content AS last_message,
            m.sender AS last_sender,
            m.is_from_me AS last_is_from_me
        FROM chats c
        JOIN messages m ON c.jid = m.chat_jid
        WHERE m.sender = ` + placeholder(1) + ` 
           OR c.jid = ` + placeholder(2) + `
        ORDER BY c.last_message_time DESC
        LIMIT ` + placeholder(3) + `
        OFFSET ` + placeholder(4)

	args := []any{jid, jid, limit, page * limit}

	rows, err := store.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chats []Chat

	for rows.Next() {
		var (
			cjid    string
			name    sql.NullString
			lmt     sql.NullTime
			lmsg    sql.NullString
			lsender sql.NullString
			lfromme sql.NullBool
		)

		if err := rows.Scan(&cjid, &name, &lmt, &lmsg, &lsender, &lfromme); err != nil {
			continue
		}

		c := Chat{JID: cjid}

		if name.Valid {
			c.Name = name.String
		}
		if lmt.Valid {
			c.LastMessageTime = lmt.Time
		}
		if lmsg.Valid {
			c.LastMessage = lmsg.String
		}
		if lsender.Valid {
			c.LastSender = lsender.String
		}
		if lfromme.Valid {
			c.LastIsFromMe = lfromme.Bool
		}

		chats = append(chats, c)
	}

	return chats, nil
}

func (store *MessageStore) GetLastInteraction(jid string) (string, error) {
	placeholder := func(n int) string {
		if isPostgres {
			return fmt.Sprintf("$%d", n)
		}
		return "?"
	}

	q := `
        SELECT 
            m.timestamp, m.sender, c.name, m.content, m.is_from_me,
            c.jid, m.id, m.media_type
        FROM messages m
        JOIN chats c ON m.chat_jid = c.jid
        WHERE m.sender = ` + placeholder(1) + `
           OR c.jid = ` + placeholder(2) + `
        ORDER BY m.timestamp DESC
        LIMIT 1
    `

	row := store.db.QueryRow(q, jid, jid)

	var (
		ts        time.Time
		sender    string
		chatName  sql.NullString
		content   string
		isFromMe  bool
		chatJid   string
		id        string
		mediaType sql.NullString
	)

	if err := row.Scan(&ts, &sender, &chatName, &content, &isFromMe, &chatJid, &id, &mediaType); err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}

	msg := MessageInteraction{
		Timestamp: ts,
		Sender:    sender,
		Content:   content,
		IsFromMe:  isFromMe,
		ChatJID:   chatJid,
		ID:        id,
	}

	if chatName.Valid {
		msg.ChatName = chatName.String
	}
	if mediaType.Valid {
		msg.MediaType = mediaType.String
	}

	return store.FormatMessage(msg, true), nil
}

func (store *MessageStore) GetChat(chatJID string, includeLastMessage bool) (*Chat, error) {
	placeholder := func(n int) string {
		if isPostgres {
			return fmt.Sprintf("$%d", n)
		}
		return "?"
	}

	q := `
        SELECT 
            c.jid, c.name, c.last_message_time,
            m.content AS last_message,
            m.sender AS last_sender,
            m.is_from_me AS last_is_from_me
        FROM chats c
    `

	if includeLastMessage {
		q += `
            LEFT JOIN messages m 
            ON c.jid = m.chat_jid 
            AND c.last_message_time = m.timestamp
        `
	}

	q += " WHERE c.jid = " + placeholder(1)

	row := store.db.QueryRow(q, chatJID)

	var (
		jid     string
		name    sql.NullString
		lmt     sql.NullTime
		lmsg    sql.NullString
		lsender sql.NullString
		lfromme sql.NullBool
	)

	if err := row.Scan(&jid, &name, &lmt, &lmsg, &lsender, &lfromme); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	c := Chat{JID: jid}

	if name.Valid {
		c.Name = name.String
	}
	if lmt.Valid {
		c.LastMessageTime = lmt.Time
	}
	if lmsg.Valid {
		c.LastMessage = lmsg.String
	}
	if lsender.Valid {
		c.LastSender = lsender.String
	}
	if lfromme.Valid {
		c.LastIsFromMe = lfromme.Bool
	}

	return &c, nil
}

func (store *MessageStore) GetDirectChatByContact(phone string) (*Chat, error) {
	placeholder := func(n int) string {
		if isPostgres {
			return fmt.Sprintf("$%d", n)
		}
		return "?"
	}

	q := `
       SELECT 
           c.jid, c.name, c.last_message_time,
           m.content AS last_message,
           m.sender AS last_sender,
           m.is_from_me AS last_is_from_me
       FROM chats c
       LEFT JOIN messages m 
           ON c.jid = m.chat_jid 
          AND c.last_message_time = m.timestamp
       WHERE c.jid LIKE ` + placeholder(1) + `
         AND c.jid NOT LIKE '%@g.us'
       LIMIT 1
    `

	arg := "%" + phone + "%"

	row := store.db.QueryRow(q, arg)

	var (
		jid     string
		name    sql.NullString
		lmt     sql.NullTime
		lmsg    sql.NullString
		lsender sql.NullString
		lfromme sql.NullBool
	)

	if err := row.Scan(&jid, &name, &lmt, &lmsg, &lsender, &lfromme); err != nil {
		return nil, err
	}

	return &Chat{
		JID:             jid,
		Name:            name.String,
		LastMessageTime: lmt.Time,
		LastMessage:     lmsg.String,
		LastSender:      lsender.String,
		LastIsFromMe:    lfromme.Bool,
	}, nil
}

func main() {
	logger := waLog.Stdout("Client", "INFO", true)
	logger.Infof("Starting WhatsApp client...")

	dbLog := waLog.Stdout("Database", "INFO", true)

	if err := os.MkdirAll("store", 0755); err != nil {
		logger.Errorf("Failed to create store directory: %v", err)
		return
	}

	config, err := getEnv()
	if err != nil {
		return
	}

	dialect := "sqlite3"
	connStr := "file:store/whatsapp.db?_foreign_keys=on"

	if config.IsPostgres {
		dialect = "postgres"
		connStr = fmt.Sprintf("postgresql://%s:%s@%s:%s/%s?sslmode=disable", config.User,
			config.Pass, config.Host, config.Port, "whatsapp")
	}

	container, err := sqlstore.New(context.Background(), dialect, connStr, dbLog)
	if err != nil {
		logger.Errorf("Failed to connect to database: %v", err)
		return
	}

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		if err == sql.ErrNoRows {
			deviceStore = container.NewDevice()
			logger.Infof("Created new device")
		} else {
			logger.Errorf("Failed to get device: %v", err)
			return
		}
	}

	version, err := CustomGetLatestVersion(context.Background(), nil)
	if err != nil {
		logger.Errorf("Failed to retrieve current WhatsApp Web client Version")
	} else {
		store.SetWAVersion(*version)
		logger.Infof("WhatsApp Web Client Version: %d.%d.%d\n", version[0], version[1], version[2])
	}
	client := whatsmeow.NewClient(deviceStore, logger)
	if client == nil {
		logger.Errorf("Failed to create WhatsApp client")
		return
	}

	store.SetOSInfo("Linux", store.GetWAVersion())
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_CHROME.Enum()

	messageStore, err := NewMessageStore()
	if err != nil {
		logger.Errorf("Failed to initialize message store: %v", err)
		return
	}
	defer messageStore.Close()

	client.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			handleMessage(client, messageStore, v, logger)

		case *events.HistorySync:
			handleHistorySync(client, messageStore, v, logger)

		case *events.Connected:
			logger.Infof("Connected to WhatsApp")

		case *events.LoggedOut:
			logger.Warnf("Device logged out, please scan QR code to log in again")
		}
	})

	connected := make(chan bool, 1)

	if client.Store.ID == nil {
		qrChan, _ := client.GetQRChannel(context.Background())
		err = client.Connect()
		if err != nil {
			logger.Errorf("Failed to connect: %v", err)
			return
		}

		for evt := range qrChan {
			if evt.Event == "code" {
				fmt.Println("\nScan this QR code with your WhatsApp app:")
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			} else if evt.Event == "success" {
				connected <- true
				break
			}
		}

		select {
		case <-connected:
			fmt.Println("\nSuccessfully connected and authenticated!")
		case <-time.After(3 * time.Minute):
			logger.Errorf("Timeout waiting for QR code scan")
			return
		}
	} else {
		err = client.Connect()
		if err != nil {
			logger.Errorf("Failed to connect: %v", err)
			return
		}
		connected <- true
	}

	time.Sleep(2 * time.Second)

	if !client.IsConnected() {
		logger.Errorf("Failed to establish stable connection")
		return
	}

	fmt.Println("\n✓ Connected to WhatsApp! Type 'help' for commands.")

	startRESTServer(client, messageStore, 8080)

	exitChan := make(chan os.Signal, 1)
	signal.Notify(exitChan, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("REST server is running. Press Ctrl+C to disconnect and exit.")

	<-exitChan

	fmt.Println("Disconnecting...")
	client.Disconnect()
}
