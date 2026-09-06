package domain

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type HelpDocument struct {
	ID               string    `json:"id"`
	Title            string    `json:"title"`
	Category         string    `json:"category"`
	SortOrder        int       `json:"sortOrder"`
	Markdown         string    `json:"markdown"`
	Version          int64     `json:"version"`
	PublishedVersion int64     `json:"publishedVersion"`
	UpdatedAt        time.Time `json:"updatedAt"`
	UpdatedBy        string    `json:"updatedBy"`
	Action           string    `json:"action,omitempty"`
}

var helpID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)

func ValidHelpID(id string) bool { return helpID.MatchString(id) }
func (d HelpDocument) Validate() error {
	if !ValidHelpID(d.ID) {
		return errors.New("id 必须是 1–80 位小写字母、数字或连字符")
	}
	if strings.TrimSpace(d.Title) == "" || utf8.RuneCountInString(d.Title) > 200 {
		return errors.New("标题必填，最多 200 字")
	}
	if strings.TrimSpace(d.Category) == "" || utf8.RuneCountInString(d.Category) > 100 {
		return errors.New("分类必填，最多 100 字")
	}
	if strings.TrimSpace(d.Markdown) == "" || len(d.Markdown) > 512*1024 || !utf8.ValidString(d.Markdown) {
		return errors.New("正文必填，最多 512 KiB UTF-8 文本")
	}
	if d.SortOrder < -100000 || d.SortOrder > 100000 {
		return errors.New("排序必须在 -100000 到 100000 之间")
	}
	return nil
}
