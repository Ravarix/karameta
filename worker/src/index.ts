import type { Env } from './types';
import { Scraper } from './scraper';
import { GitHubStorage } from './storage';

export default {
    async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
        const url = new URL(request.url);

        // Manual trigger endpoint
        if (url.pathname === '/scrape' && request.method === 'POST') {
            return handleScrape(env, ctx);
        }

        return new Response('Karameta Scraper Worker', { status: 200 });
    },

    async scheduled(event: ScheduledEvent, env: Env, ctx: ExecutionContext): Promise<void> {
        // Cron trigger - runs every minute
        ctx.waitUntil(handleScrape(env, ctx));
    },
};

async function handleScrape(env: Env, ctx: ExecutionContext): Promise<Response> {
    try {
        const storage = new GitHubStorage(
            env.GITHUB_TOKEN,
            env.GITHUB_OWNER || 'Ravarix',
            env.GITHUB_REPO || 'karameta',
            env.GITHUB_BRANCH || 'main'
        );

        const scraper = new Scraper(
            {
                apiURL: env.API_URL || 'https://api.karabast.net/api/ongoing-games',
                dedupGames: true,
            },
            storage
        );

        await scraper.initialize();
        await scraper.scrape();
        await scraper.save();

        return new Response(JSON.stringify({ success: true }), {
            headers: { 'Content-Type': 'application/json' },
        });
    } catch (error) {
        console.error('Scrape error:', error);
        return new Response(
            JSON.stringify({
                success: false,
                error: error instanceof Error ? error.message : 'Unknown error',
            }),
            {
                status: 500,
                headers: { 'Content-Type': 'application/json' },
            }
        );
    }
}
