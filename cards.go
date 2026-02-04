package main

import (
	_ "embed"
	"encoding/json"
	"log"
)

type CardData struct {
	Leaders []LeaderInfo `json:"leaders"`
	Bases   []BaseInfo   `json:"bases"`
}

type LeaderInfo struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Aspects []string `json:"aspects"`
}

type BaseInfo struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	DataKey string   `json:"dataKey"`
	Aspects []string `json:"aspects"`
}

//go:embed data/cards.json
var cardData []byte

var cardInfo CardData

func init() {
	err := json.Unmarshal(cardData, &cardInfo)
	if err != nil {
		log.Fatalf("error unmarshalling JSON: %v", err)
	}

	for _, b := range cardInfo.Bases {
		baseMap[b.ID] = b
	}

	for _, l := range cardInfo.Leaders {
		leaderMap[l.ID] = l
	}
}

var baseMap = map[string]BaseInfo{}

var leaderMap = map[string]LeaderInfo{}
