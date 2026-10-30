package main

import "errors"

// Config holds the process configuration read from environment variables.
type Config struct {
	EventID string
	// ProjectID is required: Firebase ID tokens are checked against it (their "aud" claim).
	ProjectID string
	// DatabaseID is the Firestore database; "(default)" unless FIRESTORE_DATABASE says otherwise.
	DatabaseID string
	Port       string
}

// loadConfig reads the configuration using getenv (os.Getenv in production). Errors name the
// variable that failed but never include its value.
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
	if c.ProjectID == "" {
		return Config{}, errors.New("falta la variable GOOGLE_CLOUD_PROJECT")
	}
	return c, nil
}
