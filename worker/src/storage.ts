import type { GameStats, OngoingGames } from './types';

interface GitHubFileResponse {
    content: string;
    sha: string;
}

interface GitHubTree {
    sha: string;
    tree: Array<{
        path: string;
        mode: string;
        type: string;
        sha?: string;
        content?: string;
    }>;
}

export class GitHubStorage {
    private baseUrl: string;
    private pendingWrites: Map<string, string> = new Map();

    constructor(
        private token: string,
        private owner: string,
        private repo: string,
        private branch: string = 'main'
    ) {
        this.baseUrl = `https://api.github.com/repos/${owner}/${repo}`;
    }

    async loadStats(filename: string): Promise<GameStats> {
        // Always use Blob API for stats files (handles any size, more consistent)
        const url = `${this.baseUrl}/contents/data/${filename}?ref=${this.branch}`;
        const response = await fetch(url, {
            headers: {
                'Authorization': `Bearer ${this.token}`,
                'Accept': 'application/vnd.github.v3+json',
                'User-Agent': 'Karameta-Scraper'
            }
        });

        if (!response.ok) {
            await response.text();
            if (response.status === 404) {
                console.log(`File ${filename} not found, creating new`);
                return { stats: {} };
            }
            throw new Error(`GitHub API error loading ${filename}: ${response.status} ${response.statusText}`);
        }

        const data = await response.json() as GitHubFileResponse;

        if (data.content.length > 0) {
            return JSON.parse(atob(data.content.replace(/\n/g, ''))) as GameStats;
        }

        if (!data.sha) {
            throw new Error(`GitHub API returned no SHA for ${filename}`);
        }

        // Get content via Blob API
        const content = await this.loadBlob(data.sha);

        try {
            return JSON.parse(content) as GameStats;
        } catch (error) {
            throw new Error(`Failed to parse JSON for ${filename}: ${error}. Content length: ${content.length}`);
        }
    }

    /**
     * Load a blob by SHA (used for all stats files)
     */
    private async loadBlob(sha: string): Promise<string> {
        const url = `${this.baseUrl}/git/blobs/${sha}`;
        const response = await fetch(url, {
            headers: {
                'Authorization': `Bearer ${this.token}`,
                'Accept': 'application/vnd.github.v3+json',
                'User-Agent': 'Karameta-Scraper'
            }
        });

        if (!response.ok) {
            const error = await response.text();
            throw new Error(`Failed to load blob ${sha}: ${response.status} ${error}`);
        }

        const data = await response.json() as { content: string; encoding: string; size: number };
        return atob(data.content.replace(/\n/g, ''));
    }

    async loadOngoingGames(): Promise<OngoingGames> {
        // ongoing_games.json should never be >1MB, use simple Contents API
        const url = `${this.baseUrl}/contents/data/ongoing_games.json?ref=${this.branch}`;
        const response = await fetch(url, {
            headers: {
                'Authorization': `Bearer ${this.token}`,
                'Accept': 'application/vnd.github.v3+json',
                'User-Agent': 'Karameta-Scraper'
            }
        });

        if (!response.ok) {
            await response.text();
            if (response.status === 404) {
                console.log('ongoing_games.json not found, creating new');
                return { timestamp: new Date().toISOString(), games: {} };
            }
            throw new Error(`GitHub API error loading ongoing_games.json: ${response.status}`);
        }

        const data = await response.json() as GitHubFileResponse;

        if (!data.content) {
            throw new Error(`GitHub API returned no content for ongoing_games.json`);
        }

        const content = atob(data.content.replace(/\n/g, ''));

        try {
            return JSON.parse(content) as OngoingGames;
        } catch (error) {
            throw new Error(`Failed to parse JSON for ongoing_games.json: ${error}`);
        }
    }

    /**
     * Queue a file for writing (will be committed in batch)
     */
    async saveStats(filename: string, stats: GameStats): Promise<void> {
        const content = JSON.stringify(stats, null, 2);
        this.pendingWrites.set(filename, content);
    }

    async saveOngoingGames(games: OngoingGames): Promise<void> {
        const content = JSON.stringify(games, null, 2);
        this.pendingWrites.set('ongoing_games.json', content);
    }

    /**
     * Commit all pending writes in a single atomic commit using Tree API
     */
    async commitAll(): Promise<void> {
        if (this.pendingWrites.size === 0) {
            console.log('No changes to commit');
            return;
        }

        console.log(`Committing ${this.pendingWrites.size} files in single commit...`);

        try {
            // 1. Get current branch reference
            const refUrl = `${this.baseUrl}/git/refs/heads/${this.branch}`;
            const refResponse = await fetch(refUrl, {
                headers: {
                    'Authorization': `Bearer ${this.token}`,
                    'Accept': 'application/vnd.github.v3+json',
                    'User-Agent': 'Karameta-Scraper'
                }
            });

            if (!refResponse.ok) {
                const error = await refResponse.text();
                throw new Error(`Failed to get branch ref: ${refResponse.status} ${error}`);
            }

            const ref = await refResponse.json() as { object: { sha: string } };
            const currentCommitSha = ref.object.sha;

            // 2. Get current commit to get base tree
            const commitUrl = `${this.baseUrl}/git/commits/${currentCommitSha}`;
            const commitResponse = await fetch(commitUrl, {
                headers: {
                    'Authorization': `Bearer ${this.token}`,
                    'Accept': 'application/vnd.github.v3+json',
                    'User-Agent': 'Karameta-Scraper'
                }
            });

            if (!commitResponse.ok) {
                const error = await commitResponse.text();
                throw new Error(`Failed to get commit: ${commitResponse.status} ${error}`);
            }

            const commit = await commitResponse.json() as { tree: { sha: string } };
            const baseTreeSha = commit.tree.sha;

            // 3. Create new tree with all file changes (inline content)
            const treeItems = Array.from(this.pendingWrites.entries()).map(([filename, content]) => ({
                path: `data/${filename}`,
                mode: '100644',
                type: 'blob',
                content: content  // GitHub will create the blob for us
            }));

            const treeUrl = `${this.baseUrl}/git/trees`;
            const treeResponse = await fetch(treeUrl, {
                method: 'POST',
                headers: {
                    'Authorization': `Bearer ${this.token}`,
                    'Accept': 'application/vnd.github.v3+json',
                    'Content-Type': 'application/json',
                    'User-Agent': 'Karameta-Scraper'
                },
                body: JSON.stringify({
                    base_tree: baseTreeSha,
                    tree: treeItems
                })
            });

            if (!treeResponse.ok) {
                const error = await treeResponse.text();
                throw new Error(`Failed to create tree: ${treeResponse.status} ${error}`);
            }

            const newTree = await treeResponse.json() as { sha: string };

            // 4. Create new commit
            const timestamp = new Date().toISOString();
            const newCommitUrl = `${this.baseUrl}/git/commits`;
            const newCommitResponse = await fetch(newCommitUrl, {
                method: 'POST',
                headers: {
                    'Authorization': `Bearer ${this.token}`,
                    'Accept': 'application/vnd.github.v3+json',
                    'Content-Type': 'application/json',
                    'User-Agent': 'Karameta-Scraper'
                },
                body: JSON.stringify({
                    message: `Update stats - ${timestamp}`,
                    tree: newTree.sha,
                    parents: [currentCommitSha]
                })
            });

            if (!newCommitResponse.ok) {
                const error = await newCommitResponse.text();
                throw new Error(`Failed to create commit: ${newCommitResponse.status} ${error}`);
            }

            const newCommit = await newCommitResponse.json() as { sha: string };

            // 5. Update branch reference
            const updateRefResponse = await fetch(refUrl, {
                method: 'PATCH',
                headers: {
                    'Authorization': `Bearer ${this.token}`,
                    'Accept': 'application/vnd.github.v3+json',
                    'Content-Type': 'application/json',
                    'User-Agent': 'Karameta-Scraper'
                },
                body: JSON.stringify({
                    sha: newCommit.sha,
                    force: false
                })
            });

            if (!updateRefResponse.ok) {
                const error = await updateRefResponse.text();
                throw new Error(`Failed to update ref: ${updateRefResponse.status} ${error}`);
            }

            // Consume the success response
            await updateRefResponse.text();

            console.log(`Successfully committed ${this.pendingWrites.size} files`);
            this.pendingWrites.clear();
        } catch (error) {
            console.error('Failed to commit changes:', error);
            throw error;
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
