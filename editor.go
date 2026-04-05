package main

import (
	"fmt"
	"html"
	"os"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// editor implements a simple select-replace file editor via Telegram.
type editor struct {
	mu      sync.Mutex
	bot     *tgbotapi.BotAPI
	chatID  int64
	file    string
	content string
	msg     *editedMessage
	chunks  map[int]chunk // replyMsgID -> chunk
}

type chunk struct {
	offset int
	text   string
}

func newEditor(bot *tgbotapi.BotAPI, chatID int64, file string) (*editor, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	content := string(data)
	if len(content) > 1500 || len(strings.Split(content, "\n")) > 50 {
		return nil, fmt.Errorf("file is too long to edit via Telegram")
	}

	e := &editor{
		bot:     bot,
		chatID:  chatID,
		file:    file,
		content: content,
		chunks:  make(map[int]chunk),
	}

	text := e.render()
	em, err := newEditedMessage(bot, chatID, text, false, nil)
	if err != nil {
		return nil, err
	}
	e.msg = em
	return e, nil
}

func (e *editor) render() string {
	if strings.TrimSpace(e.content) == "" {
		return "<em>(empty file)</em>"
	}
	return "<pre>" + html.EscapeString(e.content) + "</pre>"
}

// handleReply is called when the user replies to the editor message.
func (e *editor) handleReply(replyToID, msgID int, text string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if replyToID != e.msg.msgID {
		return
	}
	idx := strings.Index(e.content, text)
	if idx == -1 {
		sendHTML(e.bot, e.chatID, "Substring not found.")
		return
	}
	if strings.Count(e.content, text) > 1 {
		sendHTML(e.bot, e.chatID, "Multiple instances found; be more specific.")
		return
	}
	e.chunks[msgID] = chunk{offset: idx, text: text}
}

// handleEdit is called when the user edits a message that was used to select a chunk.
func (e *editor) handleEdit(msgID int, newText string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	ch, ok := e.chunks[msgID]
	if !ok {
		return false
	}
	if newText == "" {
		sendHTML(e.bot, e.chatID, "Empty replacement not allowed.")
		return true
	}
	end := ch.offset + len(ch.text)
	e.content = e.content[:ch.offset] + newText + e.content[end:]
	// Update chunk
	e.chunks[msgID] = chunk{offset: ch.offset, text: newText}

	if err := os.WriteFile(e.file, []byte(e.content), 0644); err != nil {
		sendHTMLf(e.bot, e.chatID, "Couldn't save file: %s", html.EscapeString(err.Error()))
		return true
	}
	e.msg.edit(e.render())
	return true
}
