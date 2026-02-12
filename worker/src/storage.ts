import type { GameStats, OngoingGames } from './types';

interface GitHubFileResponse {
    content: string;
    sha: string;
}

interface GitHubCommitRequest {
    message: string;
    content: string;
    sha?: string;
    branch: string;
}

export class GitHubStorage {
    private baseUrl: string;

    constructor(
        private token: string,
        private owner: string,
        private repo: string,
        private branch: string = 'main'
    ) {
        this.baseUrl = `https://api.github.com/repos/${owner}/${repo}/contents/data`;
    }

    async loadStats(filename: string): Promise<GameStats> {
        try {
            const url = `${this.baseUrl}/${filename}?ref=${this.branch}`;
            const response = await fetch(url, {
                headers: {
                    'Authorization': `Bearer ${this.token}`,
                    'Accept': 'application/vnd.github.v3+json',
                    'User-Agent': 'Karameta-Scraper'
                }
            });

            if (!response.ok) {
                if (response.status === 404) {
                    // File doesn't exist yet, return empty stats
                    return { stats: {} };
                }
                throw new Error(`GitHub API error: ${response.status} ${response.statusText}`);
            }

            const data = await response.json() as GitHubFileResponse;
            const content = atob(data.content.replace(/\n/g, '')); // Base64 decode
            return JSON.parse(content) as GameStats;
        } catch (error) {
            console.error(`Failed to load ${filename}:`, error);
            return { stats: {} };
        }
    }

    async saveStats(filename: string, stats: GameStats): Promise<void> {
        const url = `${this.baseUrl}/${filename}`;

        // Get current file SHA (required for updates)
        let sha: string | undefined;
        try {
            const getResponse = await fetch(`${url}?ref=${this.branch}`, {
                headers: {
                    'Authorization': `Bearer ${this.token}`,
                    'Accept': 'application/vnd.github.v3+json',
                    'User-Agent': 'Karameta-Scraper'
                }
            });

            if (getResponse.ok) {
                const data = await getResponse.json() as GitHubFileResponse;
                sha = data.sha;
            }
        } catch (error) {
            // File doesn't exist, will be created
            console.log(`Creating new file: ${filename}`);
        }

        // Update or create file
        const content = btoa(JSON.stringify(stats, null, 2)); // Base64 encode
        const timestamp = new Date().toISOString();

        const body: any = {
            message: `Update ${filename} - ${timestamp}`,
            content,
            branch: this.branch
        };

        if (sha) {
            body.sha = sha;
        }

        const response = await fetch(url, {
            method: 'PUT',
            headers: {
                'Authorization': `Bearer ${this.token}`,
                'Accept': 'application/vnd.github.v3+json',
                'Content-Type': 'application/json',
                'User-Agent': 'Karameta-Scraper'
            },
            body: JSON.stringify(body)
        });

        if (!response.ok) {
            const error = await response.text();
            throw new Error(`Failed to save ${filename}: ${response.status} ${error}`);
        }
    }

    async loadOngoingGames(): Promise<OngoingGames> {
        try {
            const url = `${this.baseUrl}/ongoing_games.json?ref=${this.branch}`;
            const response = await fetch(url, {
                headers: {
                    'Authorization': `Bearer ${this.token}`,
                    'Accept': 'application/vnd.github.v3+json',
                    'User-Agent': 'Karameta-Scraper'
                }
            });

            if (!response.ok) {
                if (response.status === 404) {
                    return { timestamp: new Date().toISOString(), games: {} };
                }
                throw new Error(`GitHub API error: ${response.status}`);
            }

            const data = await response.json() as GitHubFileResponse;
            const content = atob(data.content.replace(/\n/g, ''));
            return JSON.parse(content) as OngoingGames;
        } catch (error) {
            console.error('Failed to load ongoing games:', error);
            return { timestamp: new Date().toISOString(), games: {} };
        }
    }

    async saveOngoingGames(games: OngoingGames): Promise<void> {
        const url = `${this.baseUrl}/ongoing_games.json`;

        let sha: string | undefined;
        try {
            const getResponse = await fetch(`${url}?ref=${this.branch}`, {
                headers: {
                    'Authorization': `Bearer ${this.token}`,
                    'Accept': 'application/vnd.github.v3+json',
                    'User-Agent': 'Karameta-Scraper'
                }
            });

            if (getResponse.ok) {
                const data = await getResponse.json() as GitHubFileResponse;
                sha = data.sha;
            }
        } catch (error) {
            console.log('Creating new ongoing_games.json file');
        }

        const content = btoa(JSON.stringify(games, null, 2));
        const timestamp = new Date().toISOString();

        const body: any = {
            message: `Update ongoing_games.json - ${timestamp}`,
            content,
            branch: this.branch
        };

        if (sha) {
            body.sha = sha;
        }

        const response = await fetch(url, {
            method: 'PUT',
            headers: {
                'Authorization': `Bearer ${this.token}`,
                'Accept': 'application/vnd.github.v3+json',
                'Content-Type': 'application/json',
                'User-Agent': 'Karameta-Scraper'
            },
            body: JSON.stringify(body)
        });

        if (!response.ok) {
            const error = await response.text();
            throw new Error(`Failed to save ongoing_games.json: ${response.status} ${error}`);
        }
    }
}

export function getStatsFilePath(format: string, gamesToWin: string): string {
    return `stats_${format}_${gamesToWin}.json`;
}

export function getPlaytimeStatsFilePath(format: string, gamesToWin: string): string {
    return `playtime_stats_${format}_${gamesToWin}.json`;
}

export function getFacetKey(format: string, gamesToWin: string): string {
    return `${format}_${gamesToWin}`;
}
