package repositories

import (
	"context"
	"encoding/json"
	"sort"

	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/helpdocs"
)

func (r *GormRepository) ListHelpArticles(ctx context.Context) ([]domain.HelpArticle, error) {
	var records []HelpDocumentRecord
	if err := r.db.WithContext(ctx).Where("published_json <> ?", "").Find(&records).Error; err != nil {
		return nil, err
	}
	documents := make([]domain.HelpDocument, 0, len(records))
	for _, record := range records {
		var document domain.HelpDocument
		if err := json.Unmarshal([]byte(record.PublishedJSON), &document); err != nil {
			return nil, err
		}
		if isPublicAdminHelpDocument(document) {
			continue
		}
		documents = append(documents, document)
	}
	articles := helpdocs.ProjectHelpArticles(documents)
	sort.SliceStable(articles, func(i, j int) bool {
		if articles[i].SortOrder != articles[j].SortOrder {
			return articles[i].SortOrder < articles[j].SortOrder
		}
		return articles[i].ID < articles[j].ID
	})
	return articles, nil
}
