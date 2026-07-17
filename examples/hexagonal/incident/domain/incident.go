package domain

import "time"

// Incident represents the core business entity.
type Incident struct {
	ID        string
	Title     string
	Severity  string
	CreatedAt time.Time
}
