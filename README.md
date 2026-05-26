# benreadin

Paste a Goodreads shelf URL, pick some Libby libraries, and see which books are available to borrow, which have holds, and what everything costs on Amazon. Useful if you're trying to decide what to read next without spending money on it.

Built because [OverReader](https://overreader.net) exists but I wanted to run my own copy. Amazon pricing is optional — the app works fine without it.

---

## Running

### Docker Compose (easiest)

```sh
cp .env.example .env
# edit .env if you want Amazon pricing
mkdir -p data
docker compose up -d
```

App is at `http://localhost:3000`. SQLite database persists in `./data/`.

### Systemd

Build the binary and drop everything in `/opt/benreadin`:

```sh
go build -o bin/benreadin ./cmd/benreadin

sudo useradd -r -s /sbin/nologin benreadin
sudo mkdir -p /opt/benreadin/data /opt/benreadin/public
sudo cp bin/benreadin /opt/benreadin/
sudo cp -r public/ /opt/benreadin/
sudo cp .env.example /opt/benreadin/.env
sudo chown -R benreadin:benreadin /opt/benreadin

# edit /opt/benreadin/.env

sudo cp benreadin.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now benreadin
```

### Dev

```sh
cp .env.example .env
go run ./cmd/benreadin
```

Or with [mise](https://mise.jdx.dev): `mise run dev`

---

## Config

All config is via environment variables (or `.env`). See `.env.example` for the full list.

The only things you might actually want to change:

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `3000` | |
| `DB_PATH` | `./data/cache.db` | Directory must exist |
| `AMAZON_ACCESS_KEY` | — | Optional. PA-API 5.0 credentials |
| `AMAZON_SECRET_KEY` | — | |
| `AMAZON_PARTNER_TAG` | — | Your affiliate tag, e.g. `mysite-20` |

Cache TTLs and concurrency limits have sensible defaults and probably don't need touching.

---

## Notes

- The Gutenberg catalog download happens at startup in the background. Free book matches may not appear for the first minute or so after a fresh start.
- Amazon prices are cached for 24 hours. Library availability is cached for 2 hours.
- SSE is used for streaming results — if you're putting this behind nginx, make sure to disable buffering (`proxy_buffering off`).
