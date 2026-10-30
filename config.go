package main

import (
	"encoding/base64"
	"errors"
	"fmt"
)

const sessionKeyBytes = 32

// Config holds the process configuration read from environment variables.
type Config struct {
	SessionKey []byte
	EventID    string
	// ProjectID is optional: on Cloud Run it is detected from the metadata server.
	ProjectID string
	// DatabaseID is the Firestore database; "(default)" unless FIRESTORE_DATABASE says otherwise.
	DatabaseID string
	Port       string
}

// loadConfig reads the configuration using getenv (os.Getenv in production). Errors name the
// variable that failed but never include its value, because some of them are secrets.
func loadConfig(getenv func(string) string) (Config, error) {
	c := Config{
		EventID:    getenv("EVENT_ID"),
		ProjectID:  getenv("GOOGLE_CLOUD_PROJECT"),
		DatabaseID: getenv("FIRESTORE_DATABASE"),
		Port:       getenv("PORT"),
	}
	if c.Port == "" {
		c.Port = "8080"
	}
	if c.DatabaseID == "" {
		c.DatabaseID = "(default)"
	}
	if c.EventID == "" {
		return Config{}, errors.New("falta la variable EVENT_ID")
	}

	rawKey := getenv("SESSION_KEY")
	if rawKey == "" {
		return Config{}, errors.New("falta la variable SESSION_KEY")
	}
	key, err := base64.StdEncoding.DecodeString(rawKey)
	if err != nil {
		return Config{}, errors.New("SESSION_KEY no es base64 válido")
	}
	if len(key) != sessionKeyBytes {
		return Config{}, fmt.Errorf("SESSION_KEY debe decodificar a %d bytes", sessionKeyBytes)
	}
	c.SessionKey = key
	return c, nil
}
