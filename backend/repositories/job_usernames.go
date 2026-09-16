package repositories

import (
	"context"
	"fmt"
	"sort"
)

// JobUsernames enriches already-authorized job results for display. Callers
// supply only the submitter IDs in those results; it does not grant access to
// the user directory or change job ownership. Lifecycle reads do not use it.
func (r *GormRepository) JobUsernames(ctx context.Context, userIDs []string) (map[string]string, error) {
	unique := make(map[string]struct{}, len(userIDs))
	for _, id := range userIDs {
		if id != "" {
			unique[id] = struct{}{}
		}
	}
	names := make(map[string]string, len(unique))
	if len(unique) == 0 {
		return names, nil
	}
	ids := make([]string, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var users []struct {
		ID       string
		Username string
	}
	if err := r.db.WithContext(ctx).Model(&UserRecord{}).Select("id", "username").Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, fmt.Errorf("read job submitter names: %w", err)
	}
	for _, user := range users {
		names[user.ID] = user.Username
	}
	return names, nil
}
