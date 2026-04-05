package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	AuthToken string `json:"authToken"`
	Owner     int64  `json:"owner"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func runWizard(path string) {
	r := bufio.NewReader(os.Stdin)
	fmt.Println("=== Shell Bot Configuration Wizard ===")
	fmt.Print("Telegram Bot Token: ")
	token, _ := r.ReadString('\n')
	token = strings.TrimSpace(token)

	var ownerID int64
	fmt.Print("Your Telegram user ID (owner): ")
	fmt.Scan(&ownerID)

	cfg := Config{AuthToken: token, Owner: ownerID}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(path, data, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write config: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Config saved to", path)
}
