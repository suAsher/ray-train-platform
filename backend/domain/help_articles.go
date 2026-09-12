package domain

type HelpArticleLegacyAnchor struct {
	TopicID          string `json:"topicId"`
	SectionID        string `json:"sectionId"`
	ArticleSectionID string `json:"articleSectionId"`
}

type HelpArticle struct {
	HelpDocument
	CategoryID    string                    `json:"categoryId"`
	Summary       string                    `json:"summary"`
	Keywords      []string                  `json:"keywords"`
	RelatedIDs    []string                  `json:"relatedIds"`
	LegacyAnchors []HelpArticleLegacyAnchor `json:"legacyAnchors,omitempty"`
}
