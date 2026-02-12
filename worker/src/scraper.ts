import type {
    Game,
    GameStats,
    OngoingGames,
    ScraperConfig,
    APIResponse,
    LeaderInfo,
    BaseInfo,
    CardData,
    CombinationStats,
    TrackedGame,
} from './types';
import { GitHubStorage, getStatsFilePath, getPlaytimeStatsFilePath, getFacetKey } from './storage';
import cardData from '../../data/cards.json';

export class Scraper {
    private stats: Map<string, GameStats> = new Map();
    private playtimeStats: Map<string, GameStats> = new Map();
    private ongoingGames: OngoingGames;
    private leaderMap: Map<string, LeaderInfo> = new Map();
    private baseMap: Map<string, BaseInfo> = new Map();

    constructor(
        private config: ScraperConfig,
        private storage: GitHubStorage
    ) {
        // Initialize card lookup maps
        const cards = cardData as CardData;
        for (const leader of cards.leaders) {
            this.leaderMap.set(leader.id, leader);
        }
        for (const base of cards.bases) {
            this.baseMap.set(base.id, base);
        }

        this.ongoingGames = { timestamp: new Date().toISOString(), games: {} };
    }

    async initialize(): Promise<void> {
        const formats = ['premier', 'open', 'nextSetPreview'];
        const gamesToWinModes = ['bestOfOne', 'bestOfThree'];

        // Load all stats files
        for (const format of formats) {
            for (const gamesToWin of gamesToWinModes) {
                const facetKey = getFacetKey(format, gamesToWin);

                const statsPath = getStatsFilePath(format, gamesToWin);
                const stats = await this.storage.loadStats(statsPath);
                this.stats.set(facetKey, stats);

                const playtimePath = getPlaytimeStatsFilePath(format, gamesToWin);
                const playtimeStats = await this.storage.loadStats(playtimePath);
                this.playtimeStats.set(facetKey, playtimeStats);
            }
        }

        // Load ongoing games
        this.ongoingGames = await this.storage.loadOngoingGames();
        console.log(`Loaded ${Object.keys(this.ongoingGames.games).length} ongoing games`);
    }

    async scrape(): Promise<void> {
        console.log('Starting scrape...');

        const response = await fetch(this.config.apiURL);
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }

        const apiResp: APIResponse = await response.json();

        let newGames = 0;
        const errors: string[] = [];
        const currentGames = new Set<string>();

        for (const game of apiResp.ongoingGames) {
            currentGames.add(game.id);

            if (this.config.dedupGames && this.ongoingGames.games[game.id]) {
                continue; // Already seen
            }

            try {
                this.addGame(game);
                newGames++;
            } catch (error) {
                errors.push((error as Error).message);
            }
        }

        // Identify finished games
        const now = new Date();
        let finishedCount = 0;

        for (const [id, trackedGame] of Object.entries(this.ongoingGames.games)) {
            if (!currentGames.has(id)) {
                // Game ended
                const startTime = new Date(trackedGame.startTime);
                const timestamp = new Date(this.ongoingGames.timestamp);
                const durationMinutes = Math.floor((timestamp.getTime() - startTime.getTime()) / (1000 * 60));

                if (durationMinutes >= 1) {
                    try {
                        this.addPlaytime(trackedGame.game, durationMinutes);
                    } catch (error) {
                        errors.push((error as Error).message);
                    }
                }

                delete this.ongoingGames.games[id];
                finishedCount++;
            }
        }

        // Update ongoing games
        this.ongoingGames.timestamp = now.toISOString();
        for (const game of apiResp.ongoingGames) {
            if (!this.ongoingGames.games[game.id]) {
                this.ongoingGames.games[game.id] = {
                    game,
                    startTime: now.toISOString(),
                };
            }
        }

        // Log errors as warnings
        for (const error of errors) {
            console.warn(`::warning::${error}`);
        }

        console.log(`Scrape complete: ${apiResp.ongoingGames.length} total games, ${newGames} new games, ${finishedCount} finished games`);
    }

    async save(): Promise<void> {
        const formats = ['premier', 'open', 'nextSetPreview'];
        const gamesToWinModes = ['bestOfOne', 'bestOfThree'];

        // Queue all file writes
        for (const format of formats) {
            for (const gamesToWin of gamesToWinModes) {
                const facetKey = getFacetKey(format, gamesToWin);

                // Save stats
                const stats = this.stats.get(facetKey);
                if (stats) {
                    const statsPath = getStatsFilePath(format, gamesToWin);
                    await this.storage.saveStats(statsPath, stats);
                }

                // Save playtime stats
                const playtimeStats = this.playtimeStats.get(facetKey);
                if (playtimeStats) {
                    const playtimePath = getPlaytimeStatsFilePath(format, gamesToWin);
                    await this.storage.saveStats(playtimePath, playtimeStats);
                }
            }
        }

        await this.storage.saveOngoingGames(this.ongoingGames);

        // Commit all changes in a single atomic commit
        await this.storage.commitAll();
        console.log('Save complete');
    }

    private addGame(game: Game): void {
        const format = game.format || 'premier';
        const gamesToWin = game.gamesToWinMode || 'bestOfOne';
        const facetKey = getFacetKey(format, gamesToWin);

        const stats = this.stats.get(facetKey);
        if (!stats) {
            throw new Error(`Unknown facet: ${facetKey}`);
        }

        this.accumulate(stats, game, 1);
    }

    private addPlaytime(game: Game, minutes: number): void {
        const format = game.format || 'premier';
        const gamesToWin = game.gamesToWinMode || 'bestOfOne';
        const facetKey = getFacetKey(format, gamesToWin);

        const playtimeStats = this.playtimeStats.get(facetKey);
        if (!playtimeStats) {
            throw new Error(`Unknown facet: ${facetKey}`);
        }

        this.accumulate(playtimeStats, game, minutes);
    }

    private accumulate(gameStats: GameStats, game: Game, count: number): void {
        const now = this.getNormalizedTime();
        const dayKey = now.toISOString().split('T')[0];

        // Get leader and base info
        const p1Leader = this.leaderMap.get(game.player1Leader.id);
        const p2Leader = this.leaderMap.get(game.player2Leader.id);
        const p1Base = this.baseMap.get(game.player1Base.id);
        const p2Base = this.baseMap.get(game.player2Base.id);

        if (!p1Leader || !p2Leader || !p1Base || !p2Base) {
            const missing = [];
            if (!p1Leader) missing.push(`leader ${game.player1Leader.id}`);
            if (!p2Leader) missing.push(`leader ${game.player2Leader.id}`);
            if (!p1Base) missing.push(`base ${game.player1Base.id}`);
            if (!p2Base) missing.push(`base ${game.player2Base.id}`);
            throw new Error(`Unknown cards: ${missing.join(', ')}`);
        }

        // Track both player combinations
        const combinations = [
            { leader: p1Leader.id, base: p1Base.dataKey },
            { leader: p2Leader.id, base: p2Base.dataKey },
        ];

        for (const combo of combinations) {
            const comboKey = `${combo.leader}/${combo.base}`;

            if (!gameStats.stats[comboKey]) {
                gameStats.stats[comboKey] = { dailyCounts: {} };
            }

            const stats = gameStats.stats[comboKey];
            stats.dailyCounts[dayKey] = (stats.dailyCounts[dayKey] || 0) + count;
        }
    }

    private getNormalizedTime(): Date {
        // Convert to Pacific time (PST = UTC-8)
        const now = new Date();
        const utc = now.getTime();
        const pstOffset = -8 * 60 * 60 * 1000;
        return new Date(utc + pstOffset);
    }
}
