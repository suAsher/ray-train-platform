package helpdocs

import (
	_ "embed"
	"encoding/json"
	"ray-train-platform-backend/domain"
)

//go:embed seed.json
var seed []byte

func Documents() ([]domain.HelpDocument, error) {
	var documents []domain.HelpDocument
	if err := json.Unmarshal(seed, &documents); err != nil {
		return nil, err
	}
	return documents, nil
}
