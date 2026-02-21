package helpers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Message struct {
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
	Message Message   `json:"message"`
	Before  []Message `json:"before"`
	After   []Message `json:"after"`
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

func SendMessage(recipient, message string) (bool, string) {
	if recipient == "" {
		return false, "Recipient must be provided"
	}

	payload := map[string]string{
		"recipient": recipient,
		"message":   message,
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", apiBaseURL+"/send", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return false, "Request error: " + err.Error()
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Sprintf("HTTP %d - %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, "Failed to parse response"
	}

	success, _ := result["success"].(bool)
	msg, _ := result["message"].(string)
	if msg == "" {
		msg = "Unknown response"
	}

	return success, msg
}

func SendFile(recipient, mediaPath string) (bool, string) {
	if recipient == "" {
		return false, "Recipient must be provided"
	}
	if mediaPath == "" {
		return false, "Media path must be provided"
	}
	if _, err := os.Stat(mediaPath); os.IsNotExist(err) {
		return false, "Media file not found: " + mediaPath
	}

	payload := map[string]string{
		"recipient":  recipient,
		"media_path": mediaPath,
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", apiBaseURL+"/send", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return false, "Request error: " + err.Error()
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Sprintf("HTTP %d - %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, "Failed to parse response"
	}

	success, _ := result["success"].(bool)
	msg, _ := result["message"].(string)
	if msg == "" {
		msg = "Unknown response"
	}

	return success, msg
}

func SendAudioVoiceMessage(recipient, mediaPath string) (bool, string) {
	if recipient == "" {
		return false, "Recipient must be provided"
	}
	if mediaPath == "" {
		return false, "Media path must be provided"
	}
	if _, err := os.Stat(mediaPath); os.IsNotExist(err) {
		return false, "Media file not found: " + mediaPath
	}

	finalPath := mediaPath
	if !strings.HasSuffix(strings.ToLower(mediaPath), ".ogg") {
		converted, err := ConvertToOpusOggTemp(mediaPath)
		if err != nil {
			return false, "Audio conversion failed (ffmpeg required?): " + err.Error()
		}
		finalPath = converted
		defer func(name string) {
			err := os.Remove(name)
			if err != nil {

			}
		}(finalPath)
	}

	payload := map[string]string{
		"recipient":  recipient,
		"media_path": finalPath,
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", apiBaseURL+"/send", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return false, "Request error: " + err.Error()
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Sprintf("HTTP %d - %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, "Failed to parse response"
	}

	success, _ := result["success"].(bool)
	msg, _ := result["message"].(string)
	if msg == "" {
		msg = "Unknown response"
	}

	return success, msg
}

func DownloadMedia(messageID, chatJID string) (string, error) {
	payload := map[string]string{
		"message_id": messageID,
		"chat_jid":   chatJID,
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", apiBaseURL+"/download", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d - %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if success, ok := result["success"].(bool); !ok || !success {
		msg, _ := result["message"].(string)
		if msg == "" {
			msg = "Unknown error"
		}
		return "", errors.New(msg)
	}

	path, ok := result["path"].(string)
	if !ok || path == "" {
		return "", errors.New("no path returned")
	}

	log.Printf("Media downloaded: %s", path)
	return path, nil
}
