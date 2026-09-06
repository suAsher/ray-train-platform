package domain

import (
	"strings"
	"testing"
)

func TestHelpDocumentValidation(t *testing.T) {
	valid := HelpDocument{ID: "first-doc", Title: "标题", Category: "入门", Markdown: "正文"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []HelpDocument{valid, valid, valid, valid, valid, valid, valid, valid, valid}
	cases[0].ID = "../x"
	cases[1].Title = " "
	cases[2].Title = strings.Repeat("中", 201)
	cases[3].Category = ""
	cases[4].Category = strings.Repeat("x", 101)
	cases[5].Markdown = " "
	cases[6].Markdown = strings.Repeat("x", 512*1024+1)
	cases[7].SortOrder = 100001
	cases[8].SortOrder = -100001
	for i, d := range cases {
		if err := d.Validate(); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}
