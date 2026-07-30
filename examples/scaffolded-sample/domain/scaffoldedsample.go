package domain

import "time"

// ScaffoldedSample is the core domain entity for the scaffolded-sample feature.
type ScaffoldedSample struct {
	ID        string
	Name      string
	CreatedAt time.Time
}
