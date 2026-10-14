package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/term"
)

type config struct {
	Token  string
	ChatID int64
}

var tokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "pingme:", err)
		os.Exit(1)
	}
}

func envPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), ".env"), nil
}

func run(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(out, "Usage: pingme <send|setup|agents> [options]\n  send [--env PATH] [--title TITLE] [--silent] MESSAGE\n  send [--env PATH] -  (read stdin)\n  setup [--env PATH]\n  agents  (print instructions for coding agents)\nFlags must precede the message. Credentials default to .env beside the resolved binary.")
		return nil
	}
	if args[0] == "agents" {
		if len(args) != 1 {
			return errors.New("agents takes no arguments")
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		cmd := "'" + strings.ReplaceAll(exe, "'", "'\"'\"'") + "'"
		fmt.Fprintf(out, "Send a Telegram notification to the owner's configured private chat:\n  %s send --title 'Task complete' 'Brief result and any action needed'\nFor multiline text, pipe it to:\n  %s send -\nUse --silent for a notification without sound. Put flags before the message.\nSend only when requested or allowed by the owner's instructions. Never send credentials or sensitive logs.\nDo not read, print, modify, or request the .env file or bot token. The CLI loads credentials itself.\nExit 0 means Telegram accepted the message; exit 1 means failure. Phone delivery depends on Telegram notification settings.\nOn a timeout, delivery is uncertain: do not automatically retry and risk duplicates.\nIf setup is missing, ask the owner to run: %s setup\n", cmd, cmd, cmd)
		return nil
	}
	if args[0] != "send" && args[0] != "setup" {
		return fmt.Errorf("unknown command %q; use --help", args[0])
	}
	def, err := envPath()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("env", def, "path to private .env")
	var title *string
	var silent *bool
	if args[0] == "send" {
		title = fs.String("title", "", "optional title")
		silent = fs.Bool("silent", false, "send without sound")
	}
	if err = fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if args[0] == "setup" {
		if fs.NArg() != 0 {
			return errors.New("setup takes no positional arguments")
		}
		return setup(ctx, *path, out)
	}
	if fs.NArg() != 1 {
		return errors.New("send requires one quoted message or - for stdin")
	}
	message := fs.Arg(0)
	if message == "-" {
		b, err := io.ReadAll(io.LimitReader(in, 64*1024+1))
		if err != nil {
			return errors.New("cannot read message from stdin")
		}
		if len(b) > 64*1024 {
			return errors.New("stdin exceeds 64 KiB")
		}
		message = string(b)
	}
	if strings.TrimSpace(message) == "" {
		return errors.New("message is empty")
	}
	if *title != "" {
		message = *title + "\n\n" + message
	}
	if err = validateMessage(message); err != nil {
		return err
	}
	cfg, err := loadConfig(*path)
	if err != nil {
		return err
	}
	client := newTelegram(cfg.Token)
	if err = client.send(ctx, cfg.ChatID, message, *silent); err != nil {
		return err
	}
	fmt.Fprintln(out, "Notification accepted by Telegram.")
	return nil
}

func validateMessage(s string) error {
	if !utf8.ValidString(s) {
		return errors.New("message must be valid UTF-8")
	}
	if strings.TrimSpace(s) == "" {
		return errors.New("message is empty")
	}
	if len(utf16.Encode([]rune(s))) > 4096 {
		return errors.New("message including title exceeds 4096 UTF-16 units; shorten it")
	}
	return nil
}

func loadConfig(path string) (config, error) {
	var c config
	f, err := os.Open(path)
	if err != nil {
		return c, errors.New("cannot open .env; run pingme setup or use --env PATH")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return c, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return c, errors.New(".env must be a regular file accessible only to its owner (chmod 600)")
	}
	vals := map[string]string{}
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if !ok || (key != "TELEGRAM_BOT_TOKEN" && key != "TELEGRAM_CHAT_ID") {
			return c, errors.New(".env must contain only TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID assignments")
		}
		if _, exists := vals[key]; exists {
			return c, errors.New("duplicate .env setting")
		}
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		vals[key] = val
	}
	if scan.Err() != nil {
		return c, errors.New("cannot parse .env")
	}
	c.Token = vals["TELEGRAM_BOT_TOKEN"]
	if !tokenPattern.MatchString(c.Token) {
		return c, errors.New("invalid or missing TELEGRAM_BOT_TOKEN")
	}
	c.ChatID, err = strconv.ParseInt(vals["TELEGRAM_CHAT_ID"], 10, 64)
	if err != nil || c.ChatID <= 0 {
		return c, errors.New("TELEGRAM_CHAT_ID must be a positive private chat ID")
	}
	return c, nil
}

type telegram struct {
	token string
	base  string
	http  *http.Client
}

func newTelegram(token string) *telegram {
	return &telegram{token: token, base: "https://api.telegram.org", http: &http.Client{Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}}
}
func (t *telegram) call(ctx context.Context, method string, body any, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", t.base+"/bot"+t.token+"/"+method, bytes.NewReader(data))
	if err != nil {
		return errors.New("cannot create Telegram request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return errors.New("Telegram request failed or timed out; delivery may be uncertain (no automatic retry)")
	}
	defer resp.Body.Close()
	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil || len(b) > 1024*1024 {
		return errors.New("cannot read Telegram response")
	}
	if json.Unmarshal(b, &envelope) != nil {
		return fmt.Errorf("invalid Telegram response (HTTP %d)", resp.StatusCode)
	}
	if !envelope.OK || resp.StatusCode != 200 {
		desc := strings.ReplaceAll(envelope.Description, t.token, "[redacted]")
		if len(desc) > 500 {
			desc = desc[:500]
		}
		if envelope.Parameters.RetryAfter > 0 {
			return fmt.Errorf("Telegram rate limit: wait %d seconds before trying again", envelope.Parameters.RetryAfter)
		}
		return fmt.Errorf("Telegram rejected request (HTTP %d): %s", resp.StatusCode, desc)
	}
	if result != nil {
		if json.Unmarshal(envelope.Result, result) != nil {
			return errors.New("invalid Telegram result")
		}
	}
	return nil
}
func (t *telegram) send(ctx context.Context, id int64, text string, silent bool) error {
	return t.call(ctx, "sendMessage", map[string]any{"chat_id": id, "text": text, "disable_notification": silent, "link_preview_options": map[string]bool{"is_disabled": true}}, nil)
}

func setup(ctx context.Context, path string, out io.Writer) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("setup requires an interactive terminal")
	}
	// Restore terminal echo even when interrupted during the hidden token prompt.
	old, err := term.GetState(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(os.Stdin.Fd()), old)
	fmt.Fprintln(out, "Create a dedicated bot at https://t.me/BotFather using /newbot.\nPaste its token below (input is hidden; do not share it with agents).")
	fmt.Fprint(out, "Bot token: ")
	type tokenResult struct {
		b   []byte
		err error
	}
	ch := make(chan tokenResult, 1)
	go func() { b, e := term.ReadPassword(int(os.Stdin.Fd())); ch <- tokenResult{b, e} }()
	var token string
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return errors.New("cannot read token")
		}
		token = strings.TrimSpace(string(r.b))
	}
	fmt.Fprintln(out)
	if !tokenPattern.MatchString(token) {
		return errors.New("invalid bot token format")
	}
	client := newTelegram(token)
	var bot struct {
		Username string `json:"username"`
	}
	if err = client.call(ctx, "getMe", map[string]any{}, &bot); err != nil {
		return err
	}
	var webhook struct {
		URL string `json:"url"`
	}
	if err = client.call(ctx, "getWebhookInfo", map[string]any{}, &webhook); err != nil {
		return err
	}
	if webhook.URL != "" {
		return errors.New("bot has an active webhook; use a dedicated bot (setup will not remove it)")
	}
	var nonce [8]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	code := "pingme-" + hex.EncodeToString(nonce[:])
	fmt.Fprintf(out, "Open https://t.me/%s on your phone, tap Start, and send this exact message:\n\n  %s\n\nWaiting up to three minutes for your private chat...\n", bot.Username, code)
	wait, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var offset int64
	var id int64
	for id == 0 {
		if wait.Err() != nil {
			return errors.New("setup canceled or timed out before pairing")
		}
		var updates []struct {
			ID      int64 `json:"update_id"`
			Message *struct {
				Text string `json:"text"`
				Chat struct {
					ID   int64  `json:"id"`
					Type string `json:"type"`
				} `json:"chat"`
			} `json:"message"`
		}
		if err = client.call(wait, "getUpdates", map[string]any{"offset": offset, "timeout": 20, "allowed_updates": []string{"message"}}, &updates); err != nil {
			return err
		}
		for _, u := range updates {
			offset = u.ID + 1
			if u.Message != nil && u.Message.Text == code && u.Message.Chat.Type == "private" {
				id = u.Message.Chat.ID
				break
			}
		}
	}
	fmt.Fprintf(out, "Paired private chat %d. Save credentials to %s and send a test notification? [y/N]: ", id, path)
	reader := bufio.NewReader(os.Stdin)
	type confirmation struct {
		answer string
		err    error
	}
	answers := make(chan confirmation, 1)
	go func() { a, e := reader.ReadString('\n'); answers <- confirmation{a, e} }()
	var answer string
	select {
	case <-ctx.Done():
		return ctx.Err()
	case a := <-answers:
		if a.err != nil {
			return errors.New("cannot read confirmation")
		}
		answer = a.answer
	}
	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		fmt.Fprintln(out, "Canceled; credentials were not saved.")
		return nil
	}
	if err = saveConfig(path, config{Token: token, ChatID: id}); err != nil {
		return err
	}
	fmt.Fprintln(out, "Credentials saved with permissions 600.")
	if err = client.send(ctx, id, "pingme is ready. Coding agents can now send notifications to this chat.", false); err != nil {
		return fmt.Errorf("credentials saved, but test notification failed: %w", err)
	}
	fmt.Fprintln(out, "Test notification accepted. Run pingme agents for agent instructions.")
	return nil
}

func saveConfig(path string, c config) error {
	// Refuse symlinks and preserve an existing configuration as an owner-only backup.
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("refusing to replace a non-regular .env")
		}
		backup := path + ".bak." + strconv.FormatInt(time.Now().UnixNano(), 10)
		f, e := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		b, e := os.ReadFile(path)
		if e == nil {
			_, e = f.Write(b)
		}
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".env.tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = fmt.Fprintf(f, "TELEGRAM_BOT_TOKEN=%s\nTELEGRAM_CHAT_ID=%d\n", c.Token, c.ChatID)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
