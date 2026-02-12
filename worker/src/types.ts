// Type definitions for the scraper

export interface Leader {
    id: string;
}

export interface Base {
    id: string;
}

export interface Game {
    id: string;
    player1Leader: Leader;
    player1Base: Base;
    player2Leader: Leader;
    player2Base: Base;
    format: string;
    gamesToWinMode: string;
}

export interface APIResponse {
    ongoingGames: Game[];
}

export interface LeaderBaseCombination {
    leader: string;
    base: string;
}

export interface CombinationStats {
    dailyCounts: Record<string, number>;
}

export interface GameStats {
    stats: Record<string, CombinationStats>;
}

export interface TrackedGame {
    game: Game;
    startTime: string; // ISO string
}

export interface OngoingGames {
    timestamp: string; // ISO string
    games: Record<string, TrackedGame>;
}

export interface LeaderInfo {
    id: string;
    name: string;
    aspects: string[];
}

export interface BaseInfo {
    id: string;
    name: string;
    dataKey: string;
    aspects: string[];
}

export interface CardData {
    leaders: LeaderInfo[];
    bases: BaseInfo[];
}

export interface ScraperConfig {
    apiURL: string;
    dedupGames: boolean;
}

export interface Env {
    GITHUB_TOKEN: string;
    GITHUB_OWNER?: string;
    GITHUB_REPO?: string;
    GITHUB_BRANCH?: string;
    API_URL?: string;
}
