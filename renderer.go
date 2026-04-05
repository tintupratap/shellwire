package main

import (
	"fmt"
	"html"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// editedMessage tracks a Telegram message and serialises edits.
type editedMessage struct {
	mu      sync.Mutex
	bot     *tgbotapi.BotAPI
	chatID  int64
	msgID   int
	last    string
	pending string
	busy    bool
	markup  *tgbotapi.InlineKeyboardMarkup // attached inline keyboard, nil = none
}

func newEditedMessage(bot *tgbotapi.BotAPI, chatID int64, text string, silent bool, markup *tgbotapi.InlineKeyboardMarkup) (*editedMessage, error) {
	m := tgbotapi.NewMessage(chatID, text)
	m.ParseMode = "HTML"
	m.DisableNotification = silent
	m.DisableWebPagePreview = true
	if markup != nil {
		m.ReplyMarkup = *markup
	}
	sent, err := bot.Send(m)
	if err != nil {
		return nil, err
	}
	return &editedMessage{bot: bot, chatID: chatID, msgID: sent.MessageID, last: text, markup: markup}, nil
}

func (e *editedMessage) edit(text string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if text == e.last {
		return
	}
	e.last = text
	if e.busy {
		e.pending = text
		return
	}
	e.busy = true
	go e.flush(text)
}

func (e *editedMessage) flush(text string) {
	ed := tgbotapi.NewEditMessageText(e.chatID, e.msgID, text)
	ed.ParseMode = "HTML"
	ed.DisableWebPagePreview = true
	e.mu.Lock()
	if e.markup != nil {
		ed.ReplyMarkup = e.markup
	}
	e.mu.Unlock()
	e.bot.Send(ed) //nolint

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending != "" && e.pending != text {
		next := e.pending
		e.pending = ""
		go e.flush(next)
	} else {
		e.pending = ""
		e.busy = false
	}
}

// renderer maintains exactly ONE Telegram message per command and keeps editing it.
type renderer struct {
	mu         sync.Mutex
	bot        *tgbotapi.BotAPI
	chatID     int64
	state      *termState
	silent     bool
	dangerMode *bool
	markup     *tgbotapi.InlineKeyboardMarkup // keypad attached to terminal message
	msg        *editedMessage
	timer      *time.Timer
}

const renderDelay = 500 * time.Millisecond

func newRenderer(bot *tgbotapi.BotAPI, chatID int64, state *termState, silent bool, dangerMode *bool) *renderer {
	return &renderer{bot: bot, chatID: chatID, state: state, silent: silent, dangerMode: dangerMode}
}

// setMarkup attaches or detaches the inline keyboard from the terminal message.
func (r *renderer) setMarkup(markup *tgbotapi.InlineKeyboardMarkup) {
	r.mu.Lock()
	r.markup = markup
	if r.msg != nil {
		r.msg.mu.Lock()
		r.msg.markup = markup
		r.msg.mu.Unlock()
	}
	r.mu.Unlock()
	// Force an immediate edit to apply the new markup
	r.update()
}

// bump deletes the current terminal message so the next render sends a fresh one at the bottom.
func (r *renderer) bump() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.msg == nil {
		return
	}
	del := tgbotapi.NewDeleteMessage(r.chatID, r.msg.msgID)
	r.bot.Send(del) //nolint
	r.msg = nil
}

// update schedules a render; coalesces rapid updates.
func (r *renderer) update() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timer != nil {
		r.timer.Reset(renderDelay)
	} else {
		r.timer = time.AfterFunc(renderDelay, r.render)
	}
}

func (r *renderer) render() {
	r.mu.Lock()
	r.timer = nil
	r.mu.Unlock()

	snap := r.state.snapshot()
	text := r.decorateText(renderSnapshot(snap))

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.msg == nil {
		em, err := newEditedMessage(r.bot, r.chatID, text, r.silent, r.markup)
		if err == nil {
			r.msg = em
		}
	} else {
		r.msg.edit(text)
	}
}

func (r *renderer) decorateText(text string) string {
	if r.dangerMode != nil && *r.dangerMode {
		return "⚠️ <b>DANGER TERMINAL</b>\n" + text
	}
	return text
}

// flushFinal forces an immediate final render (called on process exit).
func (r *renderer) flushFinal() {
	r.mu.Lock()
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.mu.Unlock()

	snap := r.state.snapshot()
	text := r.decorateText(renderSnapshot(snap))

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.msg == nil {
		newEditedMessage(r.bot, r.chatID, text, r.silent, r.markup) //nolint
	} else {
		r.msg.edit(text)
	}
}

// renderSnapshot converts a screen snapshot to Telegram HTML.
// - Cursor shown as 🔸 at cursor position (when visible)
// - Reverse-video cells (selection/highlight) wrapped in <b>...</b>
// - Bold cells wrapped in <b>
// - Code-looking lines wrapped in <code>
func renderSnapshot(s screenSnapshot) string {
	// Trim trailing blank rows
	end := len(s.lines)
	for end > 0 && isBlankLine(s.lines[end-1]) {
		end--
	}
	if end == 0 {
		return "<em>(empty)</em>"
	}

	var sb strings.Builder
	allEmpty := true

	for y := 0; y < end; y++ {
		if y > 0 {
			sb.WriteByte('\n')
		}
		line := s.lines[y]
		hasCursor := s.cursorVis && y == s.curY

		// Build the line as segments of same-attribute runs
		type segment struct {
			text string
			attr cellAttr
		}
		var segs []segment
		var cur strings.Builder
		var curAttr cellAttr = 255 // sentinel

		flushSeg := func(a cellAttr) {
			if cur.Len() > 0 {
				segs = append(segs, segment{cur.String(), a})
				cur.Reset()
			}
		}

		for x := 0; x < len(line.cells); x++ {
			c := line.cells[x]
			a := c.attr

			if hasCursor && x == s.curX {
				flushSeg(curAttr)
				segs = append(segs, segment{"🔸", attrReverse})
				curAttr = 255
				continue
			}

			if a != curAttr {
				flushSeg(curAttr)
				curAttr = a
			}
			cur.WriteRune(c.ch)
		}
		flushSeg(curAttr)

		// Trim trailing spaces only from non-highlighted segments.
		// Reverse-video trailing spaces are the selection bar background — keep them
		// but replace with ░ so Telegram doesn't collapse them.
		for i := len(segs) - 1; i >= 0; i-- {
			seg := &segs[i]
			if seg.attr&attrReverse != 0 {
				// Replace trailing spaces with light shade block so highlight is visible
				trimmed := strings.TrimRight(seg.text, " ")
				trailingSpaces := len(seg.text) - len(trimmed)
				if trailingSpaces > 0 {
					seg.text = trimmed + strings.Repeat("░", trailingSpaces)
				}
				break // stop trimming once we hit a highlighted segment
			}
			seg.text = strings.TrimRight(seg.text, " ")
			if seg.text == "" {
				segs = append(segs[:i], segs[i+1:]...)
			} else {
				break
			}
		}

		if len(segs) == 0 {
			continue
		}
		allEmpty = false

		// Check if whole line looks like code (for <code> wrapping)
		// Only wrap in <code> if no reverse/bold segments (those need inline tags)
		wholeLineText := ""
		hasInlineAttrs := false
		for _, seg := range segs {
			wholeLineText += seg.text
			if seg.attr != 0 {
				hasInlineAttrs = true
			}
		}
		useCode := !hasInlineAttrs && looksLikeCode(wholeLineText)

		if useCode {
			sb.WriteString("<code>")
			sb.WriteString(html.EscapeString(wholeLineText))
			sb.WriteString("</code>")
		} else {
			for _, seg := range segs {
				escaped := html.EscapeString(seg.text)
				if seg.attr&attrReverse != 0 {
					// Reverse video = selection highlight → bold + underline in Telegram
					sb.WriteString("<b><u>")
					sb.WriteString(escaped)
					sb.WriteString("</u></b>")
				} else if seg.attr&attrBold != 0 {
					sb.WriteString("<b>")
					sb.WriteString(escaped)
					sb.WriteString("</b>")
				} else if seg.attr&attrUnder != 0 {
					sb.WriteString("<u>")
					sb.WriteString(escaped)
					sb.WriteString("</u>")
				} else {
					sb.WriteString(escaped)
				}
			}
		}
	}

	if allEmpty {
		return "<em>(empty)</em>"
	}

	result := sb.String()
	if utf8.RuneCountInString(result) > 4000 {
		runes := []rune(result)
		result = string(runes[:4000]) + "\n<em>...truncated</em>"
	}
	return result
}

func isBlankLine(l screenLine) bool {
	for _, c := range l.cells {
		if c.ch != ' ' && c.ch != 0 {
			return false
		}
	}
	return true
}

func looksLikeCode(s string) bool {
	if strings.Contains(s, "   ") {
		return true
	}
	count := 0
	for _, c := range s {
		if strings.ContainsRune(`-_,:;<>()/\~|'"=^`, c) {
			count++
			if count >= 4 {
				return true
			}
		} else {
			count = 0
		}
	}
	return false
}

func sendHTML(bot *tgbotapi.BotAPI, chatID int64, text string) {
	m := tgbotapi.NewMessage(chatID, text)
	m.ParseMode = "HTML"
	m.DisableWebPagePreview = true
	bot.Send(m) //nolint
}

func sendHTMLf(bot *tgbotapi.BotAPI, chatID int64, format string, args ...any) {
	sendHTML(bot, chatID, fmt.Sprintf(format, args...))
}
