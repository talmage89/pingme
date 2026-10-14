package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSendPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/bot123:fake/sendMessage" {
			t.Errorf("unexpected request")
		}
		var v map[string]any
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			t.Error(err)
		}
		if v["chat_id"] != float64(42) || v["text"] != "hello <literal>" || v["disable_notification"] != true {
			t.Errorf("bad payload: %v", v)
		}
		if _, ok := v["parse_mode"]; ok {
			t.Error("plain text must not set parse_mode")
		}
		io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer server.Close()
	client := newTelegram("123:fake")
	client.base = server.URL
	if err := client.send(context.Background(), 42, "hello <literal>", true); err != nil {
		t.Fatal(err)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, errors.New("secret URL: " + r.URL.String())
}
func TestTransportErrorsHideToken(t *testing.T) {
	client := newTelegram("123:secret")
	client.http.Transport = failingTransport{}
	err := client.send(context.Background(), 42, "hello", false)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("token leaked or missing error: %v", err)
	}
}
func TestAPIErrors(t *testing.T) {
	for _, tc := range []struct {
		body string
		want string
	}{{`{"ok":false,"description":"invalid 123:secret"}`, "[redacted]"}, {`{"ok":false,"parameters":{"retry_after":15}}`, "15 seconds"}, {`not json`, "invalid Telegram response"}} {
		t.Run(tc.want, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429); io.WriteString(w, tc.body) }))
			defer server.Close()
			client := newTelegram("123:secret")
			client.base = server.URL
			err := client.send(context.Background(), 42, "hello", false)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "123:secret") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
func TestMessageLimits(t *testing.T) {
	for _, s := range []string{"", " \n", string([]byte{0xff}), strings.Repeat("x", 4097), strings.Repeat("😀", 2049)} {
		if validateMessage(s) == nil {
			t.Error("accepted invalid message")
		}
	}
	if err := validateMessage(strings.Repeat("😀", 2048)); err != nil {
		t.Fatal(err)
	}
}
func TestConfigBackupAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	first := config{Token: "123:old", ChatID: 42}
	second := config{Token: "123:new", ChatID: 43}
	if err := saveConfig(path, first); err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, second); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig(path)
	if err != nil || got != second {
		t.Fatalf("%v %v", got, err)
	}
	backups, _ := filepath.Glob(path + ".bak.*")
	if len(backups) != 1 {
		t.Fatal("missing backup")
	}
	old, err := loadConfig(backups[0])
	if err != nil || old != first {
		t.Fatal("invalid backup")
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = loadConfig(path); err == nil {
		t.Fatal("accepted publicly readable credentials")
	}
}
func TestConfigRejectsSymlinkReplacement(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	os.WriteFile(target, []byte("keep"), 0600)
	path := filepath.Join(dir, ".env")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if saveConfig(path, config{Token: "123:fake", ChatID: 42}) == nil {
		t.Fatal("replaced symlink")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "keep" {
		t.Fatal("modified target")
	}
}
func TestMalformedConfigHidesSecrets(t *testing.T) {
	for _, s := range []string{"SECRET=123:secret", "TELEGRAM_BOT_TOKEN=123:secret\nTELEGRAM_BOT_TOKEN=456:other", "TELEGRAM_BOT_TOKEN=123:secret\nTELEGRAM_CHAT_ID=-1"} {
		path := filepath.Join(t.TempDir(), ".env")
		os.WriteFile(path, []byte(s), 0600)
		_, err := loadConfig(path)
		if err == nil || strings.Contains(err.Error(), "123:secret") {
			t.Fatalf("bad error: %v", err)
		}
	}
}
func TestAgentsWithoutConfig(t *testing.T) {
	var out strings.Builder
	if err := run(context.Background(), []string{"agents"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"send --title", "send -", "Do not read", "do not automatically"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("missing %q", s)
		}
	}
}
