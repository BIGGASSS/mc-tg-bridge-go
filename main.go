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
	"strconv"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/hpcloud/tail"
)

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
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	path := os.Getenv("MC_LOG_PATH")
	backend := os.Getenv("TG_API_BACKEND")
	admin := os.Getenv("ADMIN_CHAT_ID")
	startScript := os.Getenv("START_SCRIPT")
	must := map[string]string {
    "TELEGRAM_BOT_TOKEN": token,
    "MC_LOG_PATH":        path,
    "ADMIN_CHAT_ID":      admin,
    "START_SCRIPT":       startScript,
	}
	var missing []string
	for k, v := range must { if v == "" { missing = append(missing, k) } }
	if len(missing) > 0 {
	    log.Fatalf("Missing required env: %s", strings.Join(missing, ", "))
	}

	if backend == "" {
		log.Printf("Using official API backend")
		backend = "https://api.telegram.org"
	} else {
		log.Printf("Custom backend: %s", backend)
	}

	adminID, err := strconv.ParseInt(admin, 10, 64)
	if err != nil {
	    log.Panic("ADMIN_CHAT_ID not valid")
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

	// Fan-out helper to send a line to admins (or filter as needed)
	sendToAdmin := func(text string) {
		msg := tgbotapi.NewMessage(adminID, text)
		if _, err := bot.Send(msg); err != nil {
			log.Printf("send error to %s: %v", admin, err)
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
			sendToAdmin(ln)

		case update := <-updates:
			// updates channel may close; guard nils
			if update.UpdateID == 0 && update.Message == nil && update.EditedMessage == nil {
				continue
			}

			if update.Message != nil && adminID == update.Message.Chat.ID {
				if isCmd(update.Message.Text) {
					cmd := strings.TrimPrefix(update.Message.Text, "!")
					if cmd == "help" {
						log.Printf("[BLOCKED] @%s: %s", update.Message.From.UserName, update.Message.Text)
						continue
					} else if cmd == "restart" {
						log.Printf("[MATCHED] @%s: %s", update.Message.From.UserName, update.Message.Text)
						if err := screenStuff("mc", "stop"); err !=nil {
							log.Printf("screenStuff error: %v", err)
							_, _ = bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "Error: "+err.Error()))
						}
						log.Printf("Restarting in 20 seconds")
						_, _ = bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "Restarting in 20 seconds"))
						time.Sleep(20 * time.Second)
						if err := screenStuff("mc", "bash " + startScript); err !=nil {
							log.Printf("screenStuff error: %v", err)
							_, _ = bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "Error: "+err.Error()))
						}
						continue
					}
					log.Printf("[MATCHED] @%s: %s", update.Message.From.UserName, update.Message.Text)
					if err := screenStuff("mc", cmd); err != nil {
						log.Printf("screenStuff error: %v", err)
						_, _ = bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "Error: "+err.Error()))
					}
				} else {
					log.Printf("[NOT MATCHED] @%s: %s", update.Message.From.UserName, update.Message.Text)
				}
			} else if update.Message != nil && adminID != update.Message.Chat.ID {
				log.Printf("[UNAUTHORIZED] @%s: %s", update.Message.From.UserName, update.Message.Text)
			}
		}
	}
}
