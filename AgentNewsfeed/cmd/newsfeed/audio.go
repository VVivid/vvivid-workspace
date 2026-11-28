package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"agentnewsfeed/internal/audio"
)

// loadAudioConfig reads the optional ElevenLabs settings. Without an API key
// file the server offers no audio. The key is read once at start; it is never
// logged or returned.
func loadAudioConfig() (*audio.Config, error) {
	keyFile := setting("NEWSFEED_ELEVENLABS_API_KEY_FILE", "")
	if keyFile == "" {
		return nil, nil
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("NEWSFEED_ELEVENLABS_API_KEY_FILE: %w", err)
	}
	cfg := audio.Config{
		APIKey:       strings.TrimSpace(string(key)),
		Model:        setting("NEWSFEED_ELEVENLABS_MODEL", "eleven_v4"),
		OutputFormat: setting("NEWSFEED_ELEVENLABS_OUTPUT_FORMAT", "mp3_44100_64"),
		Dir:          setting("NEWSFEED_AUDIO_DIR", "data/audio"),
		BaseURL:      setting("NEWSFEED_ELEVENLABS_BASE_URL", audio.DefaultBaseURL),
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("NEWSFEED_ELEVENLABS_API_KEY_FILE is empty")
	}
	// A JSON list of {"id","name","description","elevenlabs_voice_id"}.
	if err := json.Unmarshal([]byte(setting("NEWSFEED_AUDIO_VOICES", "")), &cfg.Voices); err != nil {
		return nil, fmt.Errorf("NEWSFEED_AUDIO_VOICES must be a JSON list of voices: %w", err)
	}
	return &cfg, nil
}
