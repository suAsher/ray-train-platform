package domain

import "time"

type DesiredState string

const (
	DesiredActive   DesiredState = "ACTIVE"
	DesiredCanceled DesiredState = "CANCELED"
)

type OutboxEvent struct {
	ID          string
	AggregateID string
	EventType   string
	Payload     []byte
	Attempts    int
	NextAttempt time.Time
}
