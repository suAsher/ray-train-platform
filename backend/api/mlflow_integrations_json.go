package api

import (
	"encoding/json"
	"ray-train-platform-backend/integrations"
)

// Management uses camelCase fields, unlike the MLflow protocol's lowercase
// fields. Require their exact spelling and reject duplicate object members.
func checkIntegrationManagementJSON(decoder *json.Decoder, depth int) error {
	if depth > 8 {
		return integrations.ErrInvalid
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return integrations.ErrInvalid
			}
			switch key {
			case "name", "allowCreateExperiments", "scopes", "expiresInDays", "experimentId", "permissions":
			default:
				return integrations.ErrInvalid
			}
			seen[key] = true
			if err := checkIntegrationManagementJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := checkIntegrationManagementJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return integrations.ErrInvalid
	}
	_, err = decoder.Token()
	return err
}
