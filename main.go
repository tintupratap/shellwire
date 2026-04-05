package main

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// chatContext holds per-chat state.
type chatContext struct {
	id          int64
	shell       string
	env         map[string]string
	cwd         string
	size        [2]int // cols, rows
	silent      bool
	interactive bool
	linkPrev    bool
	command     *command
	editor      *editor
	granted     bool
	dangerMode      bool
	dangerInputCount int
}

var (
	bot      *tgbotapi.BotAPI
	cfg      *Config
	contexts = map[int64]*chatContext{}
	granted  = map[int64]bool{}
	tokens   = map[string]bool{}
	shells   []string
)

func main() {
	cfgPath := filepath.Join(filepath.Dir(os.Args[0]), "config.json")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		// Try current dir
		cfgPath = "config.json"
	}

	var err error
	cfg, err = loadConfig(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Couldn't load config, starting wizard.")
		runWizard(cfgPath)
		cfg, err = loadConfig(cfgPath)
		if err != nil {
			log.Fatal(err)
		}
	}

	shells = getShells()

	bot, err = tgbotapi.NewBotAPI(cfg.AuthToken)
	if err != nil {
		log.Fatal("Failed to connect to Telegram:", err)
	}
	log.Printf("Bot ready: @%s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		go handleUpdate(update)
	}
}

func getOrCreateContext(chatID int64) *chatContext {
	if ctx, ok := contexts[chatID]; ok {
		return ctx
	}
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.Getwd()
	}
	ctx := &chatContext{
		id:    chatID,
		shell: shells[0],
		env:   getSanitizedEnv(),
		cwd:   home,
		size:  [2]int{40, 24},
		silent: true,
	}
	contexts[chatID] = ctx
	return ctx
}

func isAllowed(update tgbotapi.Update) (int64, bool) {
	var chatID, userID int64
	if update.Message != nil {
		chatID = update.Message.Chat.ID
		userID = update.Message.From.ID
	} else if update.EditedMessage != nil {
		chatID = update.EditedMessage.Chat.ID
		userID = update.EditedMessage.From.ID
	} else if update.CallbackQuery != nil {
		chatID = update.CallbackQuery.Message.Chat.ID
		userID = update.CallbackQuery.From.ID
	} else {
		return 0, false
	}

	if chatID == cfg.Owner || userID == cfg.Owner {
		return chatID, true
	}
	if granted[chatID] || granted[userID] {
		if granted[userID] {
			return userID, true
		}
		return chatID, true
	}
	return chatID, false
}

func handleUpdate(update tgbotapi.Update) {
	// Handle callback queries (keypad)
	if update.CallbackQuery != nil {
		handleCallback(update.CallbackQuery)
		return
	}

	chatID, allowed := isAllowed(update)
	if !allowed {
		if update.Message != nil && update.Message.IsCommand() && update.Message.Command() == "start" {
			sendHTML(bot, update.Message.Chat.ID, "Not authorized to use this bot.")
		}
		return
	}

	ctx := getOrCreateContext(chatID)

	if update.EditedMessage != nil {
		handleEditedMessage(ctx, update.EditedMessage)
		return
	}

	if update.Message == nil {
		return
	}

	msg := update.Message

	// Check token in /start
	if msg.IsCommand() && msg.Command() == "start" {
		arg := msg.CommandArguments()
		if tokens[arg] {
			delete(tokens, arg)
			granted[chatID] = true
			ownerMsg := fmt.Sprintf("Chat <em>%s</em> can now use the bot. To revoke: /revoke %d",
				html.EscapeString(msg.Chat.Title+msg.Chat.FirstName), chatID)
			sendHTML(bot, cfg.Owner, ownerMsg)
		}
	}

	// Reply to bot message = send input
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil && msg.ReplyToMessage.From.ID == bot.Self.ID {
		if msg.Document != nil || msg.Photo != nil {
			handleDownload(ctx, msg)
			return
		}
		if ctx.editor != nil {
			ctx.editor.handleReply(msg.ReplyToMessage.MessageID, msg.MessageID, msg.Text)
			return
		}
		if ctx.command != nil {
			ctx.command.sendInput(msg.Text, false)
			return
		}
		sendHTML(bot, chatID, "No command is running.")
		return
	}

	// Danger terminal mode: every plain message is a command or keystroke
	if ctx.dangerMode && !msg.IsCommand() {
		handleDangerInput(ctx, msg.Text)
		return
	}

	if !msg.IsCommand() {
		return
	}

	cmd := msg.Command()
	args := strings.TrimSpace(msg.CommandArguments())

	switch cmd {
	case "start":
		sendHTML(bot, chatID, startText)

	case "help", "h":
		sendHTML(bot, chatID, helpText)

	case "about":
		sendHTML(bot, chatID, aboutText)

	case "run", "x":
		cmdRun(ctx, args)

	case "r":
		if ctx.command != nil {
			cmdEnter(ctx, args)
		} else {
			cmdRun(ctx, args)
		}

	case "enter", "e":
		cmdEnter(ctx, args)

	case "type", "t":
		if ctx.command == nil {
			sendHTML(bot, chatID, "⚠️ No command is running.")
			return
		}
		if args == "" {
			args = " "
		}
		ctx.command.sendInput(args, true)

	case "control", "ctrl", "c":
		if ctx.command == nil {
			sendHTML(bot, chatID, "⚠️ No command is running.")
			return
		}
		if len(args) != 1 || !isLetter(args[0]) {
			sendHTML(bot, chatID, "Use /ctrl &lt;letter&gt; to send Ctrl+letter to the process.")
			return
		}
		code := strings.ToUpper(args)[0] - 0x40
		ctx.command.sendInput(string([]byte{code}), true)

	case "meta", "m":
		if ctx.command == nil {
			sendHTML(bot, chatID, "⚠️ No command is running.")
			return
		}
		if args == "" {
			ctx.command.toggleMeta()
			sendHTML(bot, chatID, "🔀 Meta (Alt) mode toggled.")
		} else {
			ctx.command.toggleMeta(true)
			ctx.command.sendInput(args, true)
		}

	case "end":
		if ctx.command == nil {
			sendHTML(bot, chatID, "⚠️ No command is running.")
			return
		}
		ctx.command.sendEOF()

	case "cancel", "kill", "q":
		if ctx.command == nil {
			sendHTML(bot, chatID, "⚠️ No command is running.")
			return
		}
		sig := syscall.SIGINT
		if cmd == "kill" {
			sig = syscall.SIGTERM
		}
		if args != "" {
			s := strings.ToUpper(args)
			if !strings.HasPrefix(s, "SIG") {
				s = "SIG" + s
			}
			if resolved, ok := resolveSignal(s); ok {
				sig = resolved
			}
		}
		group := cmd == "cancel" || cmd == "q"
		if err := ctx.command.sendSignal(sig, group); err != nil {
			sendHTML(bot, chatID, "❌ Couldn't send signal.")
		}

	case "redraw", "rd":
		if ctx.command == nil {
			sendHTML(bot, chatID, "⚠️ No command is running.")
			return
		}
		ctx.command.rend.update()

	case "keypad", "k":
		if ctx.command == nil {
			sendHTML(bot, chatID, "⚠️ No command is running.")
			return
		}
		ctx.command.toggleKeypad()

	case "dangerterminal", "dt":
		cmdDangerTerminal(ctx, chatID, args)

	case "status", "s":
		cmdStatus(ctx, chatID)

	case "shell":
		cmdShell(ctx, chatID, args)

	case "cd":
		cmdCd(ctx, chatID, args)

	case "env":
		cmdEnv(ctx, chatID, args)

	case "resize", "rs":
		cmdResize(ctx, chatID, args)

	case "setsilent", "silent":
		b := resolveBoolean(args)
		if b == nil {
			sendHTMLf(bot, chatID, "🔔 Silent mode is currently <b>%s</b>. Use /silent on|off.", boolStr(ctx.silent, "on", "off"))
			return
		}
		ctx.silent = *b
		if ctx.command != nil {
			ctx.command.rend.silent = *b
		}
		if *b {
			sendHTML(bot, chatID, "🔕 Silent mode <b>on</b> — output sent without sound.")
		} else {
			sendHTML(bot, chatID, "🔔 Silent mode <b>off</b> — output will make sound.")
		}

	case "setinteractive", "interactive":
		if ctx.command != nil {
			sendHTML(bot, chatID, "⚠️ Can't change while a command is running.")
			return
		}
		b := resolveBoolean(args)
		if b == nil {
			sendHTMLf(bot, chatID, "🐚 Interactive shell is currently <b>%s</b>. Use /interactive on|off.", boolStr(ctx.interactive, "on", "off"))
			return
		}
		ctx.interactive = *b
		if *b {
			sendHTML(bot, chatID, "🐚 Interactive shell <b>on</b> — aliases and .bashrc will be loaded.")
		} else {
			sendHTML(bot, chatID, "🐚 Interactive shell <b>off</b>.")
		}

	case "setlinkpreviews", "linkpreviews":
		b := resolveBoolean(args)
		if b == nil {
			sendHTMLf(bot, chatID, "🔗 Link previews are currently <b>%s</b>. Use /linkpreviews on|off.", boolStr(ctx.linkPrev, "on", "off"))
			return
		}
		ctx.linkPrev = *b
		if *b {
			sendHTML(bot, chatID, "🔗 Link previews <b>on</b>.")
		} else {
			sendHTML(bot, chatID, "🔗 Link previews <b>off</b>.")
		}

	case "file", "f":
		cmdFile(ctx, chatID, args)

	case "upload", "ul":
		cmdUpload(ctx, chatID, args)

	case "grant", "revoke":
		if chatID != cfg.Owner {
			return
		}
		id, err := strconv.ParseInt(args, 10, 64)
		if err != nil || id == 0 {
			sendHTMLf(bot, chatID, "Use /%s &lt;chat_id&gt;.", cmd)
			return
		}
		if cmd == "grant" {
			granted[id] = true
			sendHTMLf(bot, chatID, "✅ Chat <code>%d</code> can now use this bot. Use /revoke %d to undo.", id, id)
		} else {
			if c, ok := contexts[id]; ok && c.command != nil {
				sendHTML(bot, chatID, "❌ Can't revoke: a command is running in that chat.")
				return
			}
			delete(granted, id)
			delete(contexts, id)
			sendHTMLf(bot, chatID, "🚫 Chat <code>%d</code> has been revoked.", id)
		}

	case "token":
		if chatID != cfg.Owner {
			return
		}
		tok := generateToken()
		tokens[tok] = true
		link := fmt.Sprintf("https://t.me/%s?start=%s", bot.Self.UserName, tok)
		sendHTMLf(bot, chatID, "🔑 One-time access token generated.\nShare this link to grant access:\n%s\n\n<i>Link expires after first use.</i>", link)

	default:
		sendHTMLf(bot, chatID, "❓ Unknown command. Use /help to see all available commands.")
	}
}

func handleEditedMessage(ctx *chatContext, msg *tgbotapi.Message) {
	if ctx.editor != nil && msg.ReplyToMessage != nil {
		ctx.editor.handleEdit(msg.MessageID, msg.Text)
	}
}

func handleCallback(query *tgbotapi.CallbackQuery) {
	chatID := query.Message.Chat.ID
	ctx, ok := contexts[chatID]
	if !ok || ctx.command == nil {
		bot.Request(tgbotapi.NewCallback(query.ID, "")) //nolint
		return
	}

	var data struct {
		T string `json:"t"`
		B int    `json:"b"`
	}
	if err := json.Unmarshal([]byte(query.Data), &data); err == nil {
		ctx.command.handleKeypadCallback(data.T, data.B)
	}
	bot.Request(tgbotapi.NewCallback(query.ID, "")) //nolint
}

func cmdRun(ctx *chatContext, args string) {
	if args == "" {
		sendHTML(bot, ctx.id, "💡 Use /run &lt;command&gt; to execute something.\n\nExamples:\n<code>/run htop</code>\n<code>/run ls -la</code>\n<code>/run bash</code>")
		return
	}
	if ctx.command != nil {
		sendHTML(bot, ctx.id, "⚠️ A command is already running. Use /cancel to stop it first.")
		return
	}
	ctx.editor = nil

	log.Printf("Chat %d: running %q", ctx.id, args)
	c, err := newCommand(bot, ctx, args)
	if err != nil {
		sendHTMLf(bot, ctx.id, "❌ Failed to start: %s", html.EscapeString(err.Error()))
		return
	}
	ctx.command = c
	go func() {
		<-c.exitCh
		ctx.command = nil
	}()
}

func cmdEnter(ctx *chatContext, args string) {
	if ctx.command == nil {
		sendHTML(bot, ctx.id, "No command is running.")
		return
	}
	ctx.command.sendInput(args, false)
}

func cmdDangerTerminal(ctx *chatContext, chatID int64, args string) {
	b := resolveBoolean(args)
	if b == nil {
		state := boolStr(ctx.dangerMode, "ON 🔴", "OFF")
		sendHTMLf(bot, chatID, "⚠️ <b>Danger Terminal</b> is %s\nUse /dt on|off", state)
		return
	}
	ctx.dangerMode = *b
	if *b {
		// Start a persistent interactive shell if none is running.
		// Spawn it directly (not via -c) so it's a true login/interactive shell.
		if ctx.command == nil {
			c, err := newInteractiveShell(bot, ctx)
			if err != nil {
				sendHTMLf(bot, chatID, "❌ Failed to start shell: %s", html.EscapeString(err.Error()))
				ctx.dangerMode = false
				return
			}
			ctx.command = c
			go func() {
				<-c.exitCh
				ctx.command = nil
				if ctx.dangerMode {
					ctx.dangerMode = false
					sendHTML(bot, chatID, "💀 <b>Shell exited.</b> Danger Terminal OFF.")
				}
			}()
		}
		ctx.command.showKeypad()
		sendHTML(bot, chatID, "⚠️ <b>Danger Terminal ON</b> — persistent shell started.\n\nEvery message is sent directly to the shell. <code>cd</code>, <code>export</code>, aliases all work.\n\nSpecial keys: <code>[ctrl+c]</code> <code>[ctrl+d]</code> <code>[ctrl+z]</code> <code>[ctrl+l]</code> <code>[up]</code> <code>[down]</code> <code>[left]</code> <code>[right]</code> <code>[tab]</code> <code>[esc]</code>\n\nUse /dt off to exit.")
	} else {
		ctx.dangerMode = false
		if ctx.command != nil {
			ctx.command.hideKeypad()
			// Kill the persistent shell that danger terminal started
			ctx.command.ptmx.Close()
		}
		sendHTML(bot, chatID, "✅ <b>Danger Terminal OFF</b>")
	}
}

// dangerSpecialKeys maps [token] → escape sequence
var dangerSpecialKeys = map[string]string{
	"ctrl+c": "\x03", "ctrl+d": "\x04", "ctrl+z": "\x1a",
	"ctrl+l": "\x0c", "ctrl+a": "\x01", "ctrl+e": "\x05",
	"ctrl+k": "\x0b", "ctrl+u": "\x15", "ctrl+w": "\x17",
	"ctrl+r": "\x12", "ctrl+b": "\x02", "ctrl+f": "\x06",
	"ctrl+p": "\x10", "ctrl+n": "\x0e",
	"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
	"tab": "\t", "esc": "\x1b", "enter": "\r", "backspace": "\x7f",
	"del": "\x1b[3~", "home": "\x1bOH", "end": "\x1bOF",
	"pageup": "\x1b[5~", "pgup": "\x1b[5~",
	"pagedown": "\x1b[6~", "pgdn": "\x1b[6~",
}

func handleDangerInput(ctx *chatContext, text string) {
	if ctx.command == nil {
		return // shell not ready yet
	}

	// Bump terminal message to bottom every 5 inputs so it stays visible
	ctx.dangerInputCount++
	if ctx.dangerInputCount%5 == 0 {
		ctx.command.rend.bump()
	}

	type chunk struct {
		data      string
		noNewline bool
	}
	var chunks []chunk
	remaining := text
	for remaining != "" {
		open := strings.Index(remaining, "[")
		if open == -1 {
			chunks = append(chunks, chunk{data: remaining})
			break
		}
		if open > 0 {
			chunks = append(chunks, chunk{data: remaining[:open]})
		}
		close := strings.Index(remaining[open:], "]")
		if close == -1 {
			chunks = append(chunks, chunk{data: remaining[open:]})
			break
		}
		token := strings.ToLower(remaining[open+1 : open+close])
		if seq, ok := dangerSpecialKeys[token]; ok {
			chunks = append(chunks, chunk{data: seq, noNewline: true})
		} else {
			chunks = append(chunks, chunk{data: remaining[open : open+close+1]})
		}
		remaining = remaining[open+close+1:]
	}

	for _, c := range chunks {
		ctx.command.sendInput(c.data, c.noNewline)
	}
}

func cmdStatus(ctx *chatContext, chatID int64) {
	var sb strings.Builder
	sb.WriteString("📊 <b>Status</b>\n\n")

	// Process state
	if ctx.editor != nil {
		fmt.Fprintf(&sb, "📝 <b>Editing:</b> <code>%s</code>\n", html.EscapeString(ctx.editor.file))
	} else if ctx.command != nil {
		fmt.Fprintf(&sb, "▶️ <b>Running:</b> <code>%s</code>\n", html.EscapeString(ctx.command.cmdStr))
	} else {
		sb.WriteString("💤 <b>Idle</b> — no command running\n")
	}

	sb.WriteString("\n<b>Environment</b>\n")
	fmt.Fprintf(&sb, "🐚 Shell: <code>%s</code>\n", html.EscapeString(ctx.shell))
	fmt.Fprintf(&sb, "📁 Directory: <code>%s</code>\n", html.EscapeString(ctx.cwd))
	fmt.Fprintf(&sb, "📐 Terminal: <code>%dx%d</code>\n", ctx.size[0], ctx.size[1])

	sb.WriteString("\n<b>Settings</b>\n")
	fmt.Fprintf(&sb, "🔕 Silent: %s\n", boolStr(ctx.silent, "✅ on", "❌ off"))
	fmt.Fprintf(&sb, "🐚 Interactive shell: %s\n", boolStr(ctx.interactive, "✅ on", "❌ off"))
	fmt.Fprintf(&sb, "🔗 Link previews: %s\n", boolStr(ctx.linkPrev, "✅ on", "❌ off"))
	fmt.Fprintf(&sb, "⚠️ Danger terminal: %s\n", boolStr(ctx.dangerMode, "🔴 ON", "⚫ off"))

	if chatID == cfg.Owner {
		sb.WriteString("\n<b>Access</b>\n")
		ids := make([]string, 0)
		for id := range granted {
			ids = append(ids, fmt.Sprintf("<code>%d</code>", id))
		}
		if len(ids) > 0 {
			fmt.Fprintf(&sb, "👥 Granted: %s", strings.Join(ids, ", "))
		} else {
			sb.WriteString("👥 No extra chats granted. Use /grant or /token.")
		}
	}
	sendHTML(bot, chatID, sb.String())
}

func cmdShell(ctx *chatContext, chatID int64, arg string) {
	if arg != "" {
		if ctx.command != nil {
			sendHTML(bot, chatID, "⚠️ Can't change shell while a command is running.")
			return
		}
		ctx.shell = arg
		sendHTMLf(bot, chatID, "🐚 Shell changed to <code>%s</code>.", html.EscapeString(arg))
	} else {
		var others []string
		for _, s := range shells {
			if s != ctx.shell {
				others = append(others, "<code>"+html.EscapeString(s)+"</code>")
			}
		}
		content := "🐚 Current shell: <code>" + html.EscapeString(ctx.shell) + "</code>"
		if len(others) > 0 {
			content += "\n\nAvailable shells:\n" + strings.Join(others, "\n")
		}
		sendHTML(bot, chatID, content)
	}
}

func cmdCd(ctx *chatContext, chatID int64, arg string) {
	if arg != "" {
		if ctx.command != nil {
			sendHTML(bot, chatID, "⚠️ Can't change directory while a command is running.")
			return
		}
		newDir := arg
		if !filepath.IsAbs(newDir) {
			newDir = filepath.Join(ctx.cwd, newDir)
		}
		if _, err := os.ReadDir(newDir); err != nil {
			sendHTMLf(bot, chatID, "Error: %s", html.EscapeString(err.Error()))
			return
		}
		ctx.cwd = newDir
	}
	sendHTMLf(bot, chatID, "📁 Now at: <code>%s</code>", html.EscapeString(ctx.cwd))
}

func cmdEnv(ctx *chatContext, chatID int64, arg string) {
	if arg == "" {
		sendHTML(bot, chatID, "Use /env &lt;name&gt; to read, /env &lt;name&gt;=&lt;value&gt; to set, /env &lt;name&gt;= to unset.")
		return
	}
	idx := strings.IndexAny(arg, "= ")
	if idx != -1 {
		if ctx.command != nil {
			sendHTML(bot, chatID, "⚠️ Can't change environment while a command is running.")
			return
		}
		key := strings.TrimSpace(arg[:idx])
		val := arg[idx+1:]
		if val == "" {
			delete(ctx.env, key)
		} else {
			ctx.env[key] = val
		}
	}
	key := arg
	if idx != -1 {
		key = strings.TrimSpace(arg[:idx])
	}
	if v, ok := ctx.env[key]; ok {
		sendHTML(bot, chatID, html.EscapeString(key+"="+v))
	} else {
		sendHTML(bot, chatID, html.EscapeString(key)+" unset")
	}
}

// Telegram-optimised presets:
// mobile: ~40 cols (monospace font fits ~40 chars), 24 rows (fits in one screen scroll)
// desktop: ~80 cols (Telegram desktop is wider), 35 rows
const (
	mobilePreset  = "40 24"
	desktopPreset = "80 35"
)

func cmdResize(ctx *chatContext, chatID int64, arg string) {
	switch strings.TrimSpace(strings.ToLower(arg)) {
	case "mobile":
		arg = mobilePreset
	case "desktop":
		arg = desktopPreset
	}

	var cols, rows int
	arg = strings.NewReplacer("x", " ", ",", " ", ";", " ").Replace(arg)
	fmt.Sscanf(strings.TrimSpace(arg), "%d %d", &cols, &rows)
	if cols <= 0 || rows <= 0 {
		sendHTML(bot, chatID, "Use /resize &lt;cols&gt; &lt;rows&gt;, /resize mobile, or /resize desktop.")
		return
	}
	ctx.size = [2]int{cols, rows}
	if ctx.command != nil {
		ctx.command.resize(cols, rows)
	}
	sendHTMLf(bot, chatID, "Terminal resized to %dx%d.", cols, rows)
}

func cmdFile(ctx *chatContext, chatID int64, arg string) {
	if arg == "" {
		sendHTML(bot, chatID, "Use /file &lt;path&gt; to view/edit a file.")
		return
	}
	if ctx.command != nil {
		sendHTML(bot, chatID, "A command is running.")
		return
	}
	path := arg
	if !filepath.IsAbs(path) {
		path = filepath.Join(ctx.cwd, path)
	}
	e, err := newEditor(bot, chatID, path)
	if err != nil {
		sendHTMLf(bot, chatID, "Couldn't open file: %s", html.EscapeString(err.Error()))
		return
	}
	ctx.editor = e
}

// fileUploadMap tracks uploaded file paths by message ID for download.
var fileUploadMap = map[int]string{}

func cmdUpload(ctx *chatContext, chatID int64, arg string) {
	if arg == "" {
		sendHTML(bot, chatID, "Use /upload &lt;file&gt;.")
		return
	}
	path := arg
	if !filepath.IsAbs(path) {
		path = filepath.Join(ctx.cwd, path)
	}
	f, err := os.Open(path)
	if err != nil {
		sendHTMLf(bot, chatID, "Couldn't open file: %s", html.EscapeString(err.Error()))
		return
	}
	defer f.Close()

	doc := tgbotapi.NewDocument(chatID, tgbotapi.FileReader{Name: filepath.Base(path), Reader: f})
	sent, err := bot.Send(doc)
	if err != nil {
		sendHTMLf(bot, chatID, "Couldn't send file: %s", html.EscapeString(err.Error()))
		return
	}
	fileUploadMap[sent.MessageID] = path
}

func handleDownload(ctx *chatContext, msg *tgbotapi.Message) {
	path, ok := fileUploadMap[msg.ReplyToMessage.MessageID]
	if !ok {
		return
	}
	var fileID string
	if msg.Document != nil {
		fileID = msg.Document.FileID
	} else if len(msg.Photo) > 0 {
		fileID = msg.Photo[len(msg.Photo)-1].FileID
	}
	if fileID == "" {
		return
	}
	url, err := bot.GetFileDirectURL(fileID)
	if err != nil {
		sendHTMLf(bot, ctx.id, "Couldn't get file URL: %s", html.EscapeString(err.Error()))
		return
	}
	// Download and write
	go func() {
		resp, err := http.Get(url) //nolint
		if err != nil {
			sendHTMLf(bot, ctx.id, "Download failed: %s", html.EscapeString(err.Error()))
			return
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			sendHTMLf(bot, ctx.id, "Read failed: %s", html.EscapeString(err.Error()))
			return
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			sendHTMLf(bot, ctx.id, "Write failed: %s", html.EscapeString(err.Error()))
			return
		}
		sendHTMLf(bot, ctx.id, "File written: %s", html.EscapeString(path))
	}()
}

// helpers

func boolStr(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func resolveBoolean(s string) *bool {
	m := map[string]bool{
		"yes": true, "no": false, "y": true, "n": false,
		"on": true, "off": false, "true": true, "false": false,
		"enable": true, "disable": false, "1": true, "0": false,
	}
	v, ok := m[strings.ToLower(strings.TrimSpace(s))]
	if !ok {
		return nil
	}
	return &v
}

func resolveSignal(name string) (syscall.Signal, bool) {
	sigMap := map[string]syscall.Signal{
		"SIGHUP": syscall.SIGHUP, "SIGINT": syscall.SIGINT, "SIGQUIT": syscall.SIGQUIT,
		"SIGKILL": syscall.SIGKILL, "SIGTERM": syscall.SIGTERM, "SIGSTOP": syscall.SIGSTOP,
		"SIGCONT": syscall.SIGCONT, "SIGUSR1": syscall.SIGUSR1, "SIGUSR2": syscall.SIGUSR2,
		"SIGPIPE": syscall.SIGPIPE, "SIGALRM": syscall.SIGALRM,
	}
	s, ok := sigMap[name]
	return s, ok
}

const startText = `🖥️ <b>Welcome to Shell Bot!</b>

Run shell commands right from Telegram — including fully interactive TUI apps like <code>htop</code>, <code>vim</code>, <code>nano</code>, <code>micro</code>, <code>bash</code> and more.

<b>Quick start:</b>
‣ /run htop — launch htop
‣ /run bash — open an interactive shell
‣ /keypad — show arrow keys &amp; special keys
‣ /dt on — danger terminal (every message = input)

Use /help for all commands, /about for details.`

const helpText = `📖 <b>Shell Bot — Commands</b>

<b>Running commands</b>
/run &lt;cmd&gt; — execute a command  <i>(alias: /x)</i>
/r — run or send input (smart shortcut)
/enter &lt;text&gt; — send text + Enter  <i>(alias: /e)</i>
/type &lt;text&gt; — send text without Enter  <i>(alias: /t)</i>
/end — send EOF / Ctrl+D
/cancel — send Ctrl+C to process group
/kill — send SIGTERM to process  <i>(or /q)</i>

<b>Special keys</b>
/ctrl &lt;letter&gt; — send Ctrl+letter  <i>(alias: /c)</i>
/meta [key] — send Alt+key (or toggle Alt mode)  <i>(alias: /m)</i>
/keypad — toggle arrow key pad  <i>(alias: /k)</i>
/redraw — force screen repaint  <i>(alias: /rd)</i>

<b>Danger Terminal</b>
/dt on|off — every message becomes input/command
  Special keys inline: <code>[ctrl+c]</code> <code>[up]</code> <code>[tab]</code> <code>[esc]</code> etc.

<b>Settings</b>
/status — show current state  <i>(alias: /s)</i>
/shell [name] — view or change shell
/cd [path] — change working directory
/env [name[=value]] — view or set env variable
/resize mobile|desktop|&lt;cols rows&gt;  <i>(alias: /rs)</i>
/silent on|off — toggle silent output
/interactive on|off — toggle interactive shell
/linkpreviews on|off — toggle link previews

<b>Files</b>
/file &lt;path&gt; — view or edit a text file  <i>(alias: /f)</i>
/upload &lt;path&gt; — send file to Telegram  <i>(alias: /ul)</i>

<b>Access (owner only)</b>
/grant &lt;id&gt; — allow a chat to use this bot
/revoke &lt;id&gt; — remove access
/token — generate one-time invite link

/about — about this bot`

const aboutText = `🤖 <b>Shell Bot</b> — Go Edition

A Telegram bot that gives you a full interactive terminal on your server, accessible from your phone.

<b>Features:</b>
‣ Full VT100/ANSI terminal emulator
‣ Interactive TUI apps (htop, vim, nano, micro…)
‣ Selection &amp; cursor visible in output
‣ Arrow keys via inline keypad
‣ Danger Terminal mode for rapid input
‣ File view &amp; edit via Telegram messages
‣ File upload &amp; download
‣ Per-chat shell, cwd, env, terminal size
‣ Multi-user access control with tokens

<b>Terminal presets:</b>
/resize mobile → 40×24 (phone optimised)
/resize desktop → 80×35 (desktop optimised)

Built in pure Go. Original Node.js project by Alba Mendez.`
