# Hyperion

A lightweight Telegram ↔ Minecraft console bridge written in Go.
It tails your Minecraft server log and forwards new lines to Telegram, and lets an **admin** run server commands via Telegram messages.

---

## Features

- **Real-time log forwarding** (`tail -F` equivalent with reopen on rotation)
- **Admin-only commands** from Telegram (prefix with `!`)
- **Graceful restart** helper (`!restart` → `stop` → wait 20s → run your start script)
- **Pluggable Telegram backend** (defaults to official API)

---

## Requirements

- Go **1.21+**
- Minecraft server running inside a **GNU Screen** session named **`mc`**
- A Telegram bot token from [@BotFather](https://t.me/BotFather)
- Go modules:
  - `github.com/go-telegram-bot-api/telegram-bot-api/v5`
  - `github.com/hpcloud/tail`

---

## Installation

```bash
git clone https://github.com/BIGGASSS/Hyperion.git
cd Hyperion

# Build
go build -o hyperion .
````

---

## Configuration

### Environment variables

| Variable             | Required | Example                           | Description                                             |
| -------------------- | :------: | --------------------------------- | ------------------------------------------------------- |
| `TELEGRAM_BOT_TOKEN` |     ✅    | `123456:ABC-DEF...`               | Bot token from @BotFather                               |
| `MC_LOG_PATH`        |     ✅    | `/home/mc/server/logs/latest.log` | Path to the server log to tail                          |
| `ADMIN_CHAT_ID`      |     ✅    | `123456789`                       | Telegram **chat ID** allowed to issue commands          |
| `START_SCRIPT`       |     ✅    | `/home/mc/start.sh`               | Script used to start the server (run by `!restart`)     |
| `TG_API_BACKEND`     |     ❌    | `https://api.telegram.org`        | Custom Telegram API base; defaults to official if unset |

> **Note:** The Screen session name is currently hardcoded as `mc`.
> Change the calls `screenStuff("mc", ...)` in code if your session name differs.

---

## Running

### Local (shell)

```bash
export TELEGRAM_BOT_TOKEN=123456:ABC-DEF...
export MC_LOG_PATH=/home/mc/server/logs/latest.log
export ADMIN_CHAT_ID=123456789
export START_SCRIPT=/home/mc/start.sh
# export TG_API_BACKEND=https://api.telegram.org  # optional

./hyperion
```

### systemd (optional)

Create `/etc/systemd/system/hyperion.service`:

```ini
[Unit]
Description=Telegram ↔ Minecraft bridge
After=network.target

[Service]
Type=simple
ExecStart=/home/mc/hyperion
WorkingDirectory=/home/mc
Environment=TELEGRAM_BOT_TOKEN=123456:ABC-DEF...
Environment=MC_LOG_PATH=/home/mc/server/logs/latest.log
Environment=ADMIN_CHAT_ID=123456789
Environment=START_SCRIPT=/home/mc/start.sh
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Enable & start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable hyperion
sudo systemctl start hyperion
```

---

## Usage

* All **new lines** from `MC_LOG_PATH` are forwarded to the **admin chat**.
* In the admin chat:

  * Prefix with `!` to send commands to the Screen session `mc`.

    * Example: `!say Server restarting in 60s`
    * Example: `!time set day`
  * `!restart` performs:

    1. `stop` in the `mc` Screen
    2. wait **20s**
    3. `bash $START_SCRIPT`
  * `!help` is currently blocked/reserved due to spamming.
* Messages from non-admin chats are ignored (logged as unauthorized).

---

## How it works (brief)

* **Tailer:** uses `hpcloud/tail` with `Follow`, `ReOpen`, `MustExist`, and starts at EOF (like `tail -n 0 -F`).
* **Telegram:** long polling with `u.Timeout = 60`; only processes updates from `ADMIN_CHAT_ID`.
* **Screen control:** `screen -S mc -p 0 -X stuff "<cmd>\r"` with a 5s context timeout and error capture.

---

## Security Notes

* Keep your **bot token** secret.
* Ensure `START_SCRIPT` and the `mc` Screen session are owned by trusted users.
* Consider running under a dedicated non-privileged user.

---

## Troubleshooting

* **`Missing required env`** → ensure all ✅ variables are set.
* **No log lines arrive** → verify `MC_LOG_PATH` exists and is being written to.
* **Commands not executed** → confirm Screen session is named `mc` and is running.
* **`ADMIN_CHAT_ID not valid`** → must be a base-10 integer (fits in int64).
