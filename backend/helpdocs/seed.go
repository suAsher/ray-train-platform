package helpdocs

import (
	_ "embed"
	"encoding/json"
	"ray-train-platform-backend/domain"
)

//go:embed seed.json
var seed []byte

//go:embed lifecycle.json
var lifecycleSeed []byte

func Documents() ([]domain.HelpDocument, error) {
	var documents []domain.HelpDocument
	if err := json.Unmarshal(seed, &documents); err != nil {
		return nil, err
	}
	var lifecycle []domain.HelpDocument
	if err := json.Unmarshal(lifecycleSeed, &lifecycle); err != nil {
		return nil, err
	}
	documents = append(documents, lifecycle...)
	return documents, nil
}
