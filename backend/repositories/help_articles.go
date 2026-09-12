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
	articles := make([]domain.HelpArticle, 0, len(records))
	for _, record := range records {
		var document domain.HelpDocument
		if err := json.Unmarshal([]byte(record.PublishedJSON), &document); err != nil {
			return nil, err
		}
		if isPublicAdminHelpDocument(document) {
			continue
		}
		articles = append(articles, helpdocs.ProjectHelpArticle(document))
	}
	sort.SliceStable(articles, func(i, j int) bool {
		if articles[i].SortOrder != articles[j].SortOrder {
			return articles[i].SortOrder < articles[j].SortOrder
		}
		return articles[i].ID < articles[j].ID
	})
	return articles, nil
}
