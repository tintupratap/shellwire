package main

import (
	"fmt"
	"html"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// keyDef maps a key name to its escape sequence(s).
type keyDef struct {
	label          string
	content        string
	appKeypadContent string
}

var keyDefs = map[string]keyDef{
	"esc":       {label: "ESC", content: "\x1b"},
	"tab":       {label: "⇥", content: "\t"},
	"enter":     {label: "⏎", content: "\r"},
	"backspace": {label: "↤", content: "\x7f"},
	"space":     {label: "␣", content: " "},
	"up":        {label: "↑", content: "\x1b[A", appKeypadContent: "\x1bOA"},
	"down":      {label: "↓", content: "\x1b[B", appKeypadContent: "\x1bOB"},
	"right":     {label: "→", content: "\x1b[C", appKeypadContent: "\x1bOC"},
	"left":      {label: "←", content: "\x1b[D", appKeypadContent: "\x1bOD"},
	"insert":    {label: "INS", content: "\x1b[2~"},
	"del":       {label: "DEL", content: "\x1b[3~"},
	"home":      {label: "⇱", content: "\x1bOH"},
	"end":       {label: "⇲", content: "\x1bOF"},
	"prevPage":  {label: "⇈", content: "\x1b[5~"},
	"nextPage":  {label: "⇊", content: "\x1b[6~"},
}

var keypadLayout = [][]string{
	{"esc", "up", "backspace", "del"},
	{"left", "space", "right", "home"},
	{"tab", "down", "enter", "end"},
}

// command manages a running PTY process for a chat.
type command struct {
	mu          sync.Mutex
	bot         *tgbotapi.BotAPI
	chatID      int64
	ctx         *chatContext
	cmdStr      string
	ptmx        *os.File
	term        *termState
	rend        *renderer
	initialMsg  *editedMessage
	startTime   time.Time
	interacted  bool
	metaActive  bool
	exitCh      chan struct{}
	keypadMsgID int // 0 = not shown
	keypadToken string
	buttons     []keyDef
	keyboard    [][]tgbotapi.InlineKeyboardButton
}

func newCommand(bot *tgbotapi.BotAPI, ctx *chatContext, cmdStr string) (*command, error) {
	args := []string{"-c", cmdStr}
	if ctx.interactive {
		args = []string{"-ic", cmdStr}
	}
	return spawnCommand(bot, ctx, cmdStr, ctx.shell, args)
}

// newInteractiveShell spawns the shell directly with -i (no -c wrapper).
// Used by danger terminal for a true persistent interactive session.
func newInteractiveShell(bot *tgbotapi.BotAPI, ctx *chatContext) (*command, error) {
	return spawnCommand(bot, ctx, ctx.shell, ctx.shell, []string{"-i"})
}

func spawnCommand(bot *tgbotapi.BotAPI, ctx *chatContext, label, shell string, args []string) (*command, error) {
	term := newTermState(ctx.size[0], ctx.size[1])
	rend := newRenderer(bot, ctx.id, term, ctx.silent, &ctx.dangerMode)

	c := &command{
		bot:       bot,
		chatID:    ctx.id,
		ctx:       ctx,
		cmdStr:    label,
		term:      term,
		rend:      rend,
		startTime: time.Now(),
		exitCh:    make(chan struct{}),
	}
	c.initKeypad()

	initText := "<strong>$ " + html.EscapeString(label) + "</strong>"
	em, err := newEditedMessage(bot, ctx.id, initText, ctx.silent, nil)
	if err != nil {
		return nil, fmt.Errorf("send initial message: %w", err)
	}
	c.initialMsg = em

	cmd := exec.Command(shell, args...)
	cmd.Dir = ctx.cwd
	cmd.Env = envToSlice(ctx.env)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(ctx.size[0]), Rows: uint16(ctx.size[1])})
	if err != nil {
		return nil, fmt.Errorf("spawn pty: %w", err)
	}
	c.ptmx = ptmx

	go c.readLoop()
	return c, nil
}

func (c *command) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := c.ptmx.Read(buf)
		if n > 0 {
			c.mu.Lock()
			c.interacted = true
			c.mu.Unlock()
			c.term.write(buf[:n])
			c.rend.update()
			c.updateInitialMsg()
		}
		if err != nil {
			break
		}
	}
	c.onExit()
}

func (c *command) updateInitialMsg() {
	title := c.term.Title
	var text string
	if title != "" {
		text = "<strong>" + html.EscapeString(title) + "</strong>\n<strong>$ " + html.EscapeString(c.cmdStr) + "</strong>"
	} else {
		text = "<strong>$ " + html.EscapeString(c.cmdStr) + "</strong>"
	}
	c.initialMsg.edit(text)
}

func (c *command) onExit() {
	c.rend.flushFinal()
	c.mu.Lock()
	interacted := c.interacted
	c.mu.Unlock()

	elapsed := time.Since(c.startTime)
	if elapsed < 2*time.Second && !interacted {
		// Short-lived clean exit: add tick to initial message
		c.initialMsg.edit("✅ <strong>$ " + html.EscapeString(c.cmdStr) + "</strong>")
	} else {
		sendHTML(c.bot, c.chatID, "✅ <strong>Exited</strong> correctly.")
	}
	close(c.exitCh)
}

func (c *command) sendInput(text string, noTerminate bool) {
	c.mu.Lock()
	c.interacted = true
	meta := c.metaActive
	c.metaActive = false
	c.mu.Unlock()

	text = strings.ReplaceAll(text, "\n", "\r")
	if !noTerminate {
		text += "\r"
	}
	if meta {
		text = "\x1b" + text
	}
	c.ptmx.WriteString(text) //nolint
}

func (c *command) sendSignal(sig syscall.Signal, group bool) error {
	// Get PID from ptmx
	pid, err := getPTYPid(c.ptmx)
	if err != nil {
		return err
	}
	if group {
		return syscall.Kill(-pid, sig)
	}
	return syscall.Kill(pid, sig)
}

func (c *command) sendEOF() {
	// Send Ctrl+D
	c.ptmx.Write([]byte{4}) //nolint
}

func (c *command) resize(cols, rows int) {
	c.mu.Lock()
	c.interacted = true
	c.mu.Unlock()
	c.term.resize(cols, rows)
	pty.Setsize(c.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}) //nolint
	c.rend.update()
}

func (c *command) toggleMeta(val ...bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(val) > 0 {
		c.metaActive = val[0]
	} else {
		c.metaActive = !c.metaActive
	}
}

func (c *command) initKeypad() {
	c.keypadToken = fmt.Sprintf("%d-%d", c.chatID, time.Now().UnixNano())
	c.buttons = nil
	c.keyboard = make([][]tgbotapi.InlineKeyboardButton, len(keypadLayout))
	for r, row := range keypadLayout {
		c.keyboard[r] = make([]tgbotapi.InlineKeyboardButton, len(row))
		for col, name := range row {
			k := keyDefs[name]
			idx := len(c.buttons)
			c.buttons = append(c.buttons, k)
			data := fmt.Sprintf(`{"t":"%s","b":%d}`, c.keypadToken, idx)
			c.keyboard[r][col] = tgbotapi.NewInlineKeyboardButtonData(k.label, data)
		}
	}
}

func (c *command) showKeypad() {
	if c.keypadMsgID != 0 {
		return
	}
	c.keypadMsgID = 1 // mark as shown
	km := tgbotapi.NewInlineKeyboardMarkup(c.keyboard...)
	c.rend.setMarkup(&km)
}

func (c *command) hideKeypad() {
	if c.keypadMsgID == 0 {
		return
	}
	c.keypadMsgID = 0
	c.rend.setMarkup(nil)
}

func (c *command) toggleKeypad() {
	if c.keypadMsgID != 0 {
		c.hideKeypad()
	} else {
		c.showKeypad()
	}
}

func (c *command) handleKeypadCallback(token string, btnIdx int) bool {
	if token != c.keypadToken {
		return false
	}
	if btnIdx < 0 || btnIdx >= len(c.buttons) {
		return false
	}
	btn := c.buttons[btnIdx]
	content := btn.content
	if btn.appKeypadContent != "" && c.term.appKeypad {
		content = btn.appKeypadContent
	}
	c.ptmx.WriteString(content) //nolint
	c.mu.Lock()
	c.interacted = true
	c.mu.Unlock()
	return true
}
