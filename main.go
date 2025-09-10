package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"io"
	"regexp"
	"strings"
	"syscall"
	"time"
	"os/exec"
	"slices"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/hpcloud/tail"
)

var AdminList = []int64{1971451950}

func isCmd(s string) bool { // determines if the message starts with `!`
	re := regexp.MustCompile("^!")
	return re.MatchString(s)
}

func screenStuff(session, cmd string) error { // interacts with the screen session
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	send := cmd + "\r"
	c := exec.CommandContext(ctx, "screen",
		"-S", session,
		"-p", "0",
		"-X", "stuff", send,
	)
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("screen stuff failed: %w; output: %s", err, string(out))
	}
	return nil
}

// readConsole runs like `tail -F` but is cancelable.
// It sends each new line to the provided channel until ctx is canceled.
func readConsole(ctx context.Context, path string, out chan<- string) error {
	t, err := tail.TailFile(path, tail.Config{
		Follow:   true,
		ReOpen:   true,                        // handle rotation
		MustExist: true,                       // fail fast if missing
		Location: &tail.SeekInfo{Offset: 0, Whence: io.SeekEnd}, // like -n0
		Logger:   tail.DiscardingLogger,       // quiet internal logs
	})
	if err != nil {
		return err
	}
	defer func() {
	    t.Stop()
	    t.Cleanup()
	}()


	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-t.Lines:
			if !ok {
				return nil // tail ended
			}
			if line.Err != nil {
				log.Printf("tail read error: %v", line.Err)
				continue
			}
			out <- line.Text
		}
	}
}

// runConsoleGoroutine starts readConsole in a goroutine and returns its cancel + output channel.
func runConsoleGoroutine(parent context.Context, path string) (cancel func(), lines <-chan string) {
	ctx, cancel := context.WithCancel(parent)
	ch := make(chan string, 256) // buffer to smooth bursts

	go func() {
		defer close(ch)
		if err := readConsole(ctx, path, ch); err != nil && ctx.Err() == nil {
			log.Printf("readConsole error: %v", err)
		}
	}()
	return cancel, ch
}

func main() {
	// Replace with a fresh token from BotFather or read from env.
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	path := os.Getenv("MC_LOG_PATH")
	backend := os.Getenv("TG_API_BACKEND")
	if token == "" && path == ""{
		log.Fatal("TELEGRAM_BOT_TOKEN and MC_LOG_PATH not set")
	} else if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN not set")
	} else if path == "" {
		log.Fatal("MC_LOG_PATH not set")
	}
	if backend == "" {
		backend = "https://api.telegram.org"
	} else {
		log.Printf("Custom backend: %s", backend)
	}

	endpoint := backend + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithAPIEndpoint(token, endpoint)
	if err != nil {
		log.Panic(err)
	}
	bot.Debug = true
	log.Printf("Authorized on account %s", bot.Self.UserName)

	// Root context canceled by OS signals
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start the console tail goroutine
	cancelTail, tailLines := runConsoleGoroutine(ctx, path)
	defer cancelTail()

	// Telegram updates (long polling)
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	// Fan-out helper to send a line to all admins (or filter as needed)
	sendToAdmins := func(text string) {
		for _, chatID := range AdminList {
			msg := tgbotapi.NewMessage(chatID, text)
			if _, err := bot.Send(msg); err != nil {
				log.Printf("send error to %d: %v", chatID, err)
			}
		}
	}

	// Multiplex: handle both tailed lines and Telegram updates
	for {
		select {
		case <-ctx.Done():
			log.Println("shutting down...")
			return

		case ln, ok := <-tailLines:
			if !ok {
				log.Println("tail ended")
				return
			}
			// Decide what to forward. Example: forward every line.
			// You can add filters here (e.g., only important events).
			sendToAdmins(ln)

		case update := <-updates:
			// updates channel may close; guard nils
			if update.UpdateID == 0 && update.Message == nil && update.EditedMessage == nil {
				continue
			}

			if update.Message != nil && slices.Contains(AdminList, update.Message.Chat.ID) {
				if isCmd(update.Message.Text) {
					log.Printf("[MATCHED] @%s: %s", update.Message.From.UserName, update.Message.Text)
					cmd := strings.TrimPrefix(update.Message.Text, "!")
					if err := screenStuff("mc", cmd); err != nil {
						log.Printf("screenStuff error: %v", err)
						_, _ = bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "Error: "+err.Error()))
					}
				} else {
					log.Printf("[NOT MATCHED] @%s: %s", update.Message.From.UserName, update.Message.Text)
				}
			} else if update.Message != nil && !slices.Contains(AdminList, update.Message.Chat.ID) {
				log.Printf("[UNAUTHORIZED] @%s: %s", update.Message.From.UserName, update.Message.Text)
			}
		}
	}
}
