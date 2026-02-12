# Karameta

https://ravarix.github.io/karameta/

A game statistics scraper and dashboard for tracking leader/base combinations in a card game. The scraper runs as a Cloudflare Worker on a cron schedule, collecting data from the game API and committing it back to this GitHub repository.

## Architecture

The scraper is written in TypeScript and runs natively in Cloudflare Workers. It uses the GitHub repository as the source of truth for all state - reading existing stats files and committing updates directly to the repo. This means:

- **Zero infrastructure cost** (GitHub API is free, Workers free tier is sufficient)
- **Frontend unchanged** (still reads from `raw.githubusercontent.com`)
- **Git history** of all stat changes
- **Simple** and transparent

### Components

- `worker/src/`: TypeScript scraper for Cloudflare Workers
  - `index.ts`: Worker entry point with cron and HTTP handlers
  - `scraper.ts`: Core scraping logic and stats tracking
  - `storage.ts`: GitHub API integration for reading/writing files
  - `types.ts`: TypeScript type definitions
- `data/`: JSON stats files (source of truth)
  - `cards.json`: Card database for leader/base lookups
  - `stats_*.json`: Game count statistics by format/mode
  - `playtime_stats_*.json`: Playtime statistics
  - `ongoing_games.json`: Currently tracked games
- `docs/`: Frontend dashboard (GitHub Pages)

## Setup

### Prerequisites

- [Node.js](https://nodejs.org/) (v18+)
- [Wrangler CLI](https://developers.cloudflare.com/workers/wrangler/install-and-update/): `npm install -g wrangler`
- Cloudflare account (free tier is fine)
- GitHub Personal Access Token with `repo` scope

### First-time Setup

1. **Install dependencies**:
   ```bash
   make install
   # or: cd worker && npm install
   ```

2. **Authenticate with Cloudflare**:
   ```bash
   wrangler login
   ```

3. **Create GitHub Personal Access Token**:
   - Go to https://github.com/settings/tokens
   - Click "Generate new token" → "Generate new token (classic)"
   - Name: `Karameta Scraper`
   - Scopes: Check `repo` (Full control of private repositories)
   - Click "Generate token" and copy it

4. **Set GitHub token as secret**:
   ```bash
   make setup
   # or: cd worker && wrangler secret put GITHUB_TOKEN
   # Paste your GitHub token when prompted
   ```

5. **Update `wrangler.toml`** (if needed):
   - Open `worker/wrangler.toml`
   - Verify `GITHUB_OWNER`, `GITHUB_REPO`, and `GITHUB_BRANCH` are correct

## Deployment

Deploy the worker to Cloudflare:

```bash
make deploy
# or: cd worker && npm run deploy
```

The worker will automatically:
- Run every minute via cron trigger
- Scrape ongoing games from the API  
- Update stats and commit changes to GitHub
- Frontend reads stats from GitHub (no changes needed!)

## Local Development

### Test Locally

Run the worker in dev mode:

```bash
make dev
# or: cd worker && npm run dev
```

This starts a local server at `http://localhost:8787`

**Note**: Local dev will make real commits to your GitHub repo! Use with caution or test on a fork.

### Manual Trigger

Trigger a scrape manually via HTTP POST:

```bash
curl -X POST http://localhost:8787/scrape
```

Or in production:

```bash
curl -X POST https://karameta-scraper.YOUR_SUBDOMAIN.workers.dev/scrape
```

### View Logs

Watch live worker logs:

```bash
make logs
# or: cd worker && wrangler tail
```

## How It Works

1. **Every minute** (cron trigger):
   - Worker reads current stats files from GitHub via API
   - Fetches ongoing games from game API
   - Updates stats for new games
   - Tracks playtime for finished games
   - Commits all updated files to GitHub in a **single atomic commit**

2. **Frontend** (unchanged):
   - Reads stats from `https://raw.githubusercontent.com/Ravarix/karameta/main/data/stats_*.json`
   - No changes needed!

**Efficiency**: Uses GitHub Tree API to batch all file changes into one commit, creating clean git history with single "Update stats" commits instead of 15 individual commits per run.

## Data Files

The scraper manages these files in `data/`:

- **Game counts**: `stats_<format>_<mode>.json`
- **Playtime**: `playtime_stats_<format>_<mode>.json`
- **Ongoing games**: `ongoing_games.json`

**Formats**: `premier`, `open`, `nextSetPreview`  
**Modes**: `bestOfOne`, `bestOfThree`

## Configuration

Environment variables (set in `wrangler.toml`):

- `API_URL`: Game API endpoint (default: `https://api.karabast.net/api/ongoing-games`)
- `GITHUB_OWNER`: Repository owner (default: `Ravarix`)
- `GITHUB_REPO`: Repository name (default: `karameta`)
- `GITHUB_BRANCH`: Branch to commit to (default: `main`)

Secrets (set via `wrangler secret put`):

- `GITHUB_TOKEN`: GitHub Personal Access Token (required)

## Cost Analysis

**Total monthly cost: $0** 🎉

- **Workers**: Free tier (100K requests/day, you use ~43K/month)
- **GitHub API**: Free (5K requests/hour, you use ~60/hour)
- **Storage**: GitHub repo (free)

## Makefile Commands

- `make install` - Install npm dependencies
- `make setup` - Set GitHub token (one-time)
- `make build` - Build TypeScript
- `make deploy` - Deploy to Cloudflare Workers
- `make dev` - Run locally for testing
- `make logs` - View live worker logs
- `make clean` - Remove build artifacts

## Troubleshooting

**Worker not running on schedule?**
- Check cron triggers are enabled in Cloudflare dashboard
- Verify the cron expression in `wrangler.toml`: `* * * * *` (every minute)

**GitHub API errors?**
- Ensure `GITHUB_TOKEN` secret is set: `make setup`
- Verify token has `repo` scope
- Check rate limits: https://github.com/settings/tokens (shouldn't hit limits)

**Card lookup errors?**
- Ensure `data/cards.json` is up to date with latest card IDs
- Check worker logs for specific missing card IDs: `make logs`

**Commits not appearing?**
- Check GitHub token permissions
- Verify `GITHUB_OWNER`, `GITHUB_REPO`, `GITHUB_BRANCH` in `wrangler.toml`
- Check worker logs for commit errors

## Project History

This project was originally implemented in Go with WASM compilation for Cloudflare Workers, then briefly considered using R2 storage, but ultimately settled on using the GitHub repository as the source of truth for simplicity and zero cost. The Go/WASM code has been archived to `archive/go-wasm/`.