package pull

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type selectionCursor struct {
	Binding string `json:"binding"`
	Peer    string `json:"peer"`
}

func selectionPage(binding string, page kernel.Page) (kernel.Page, error) {
	if len(page.After) > 128<<10 {
		return page, kernel.ErrInvalidCursor
	}
	page, err := page.Normalize()
	if err != nil {
		return page, fmt.Errorf("pull selection page: %w", err)
	}
	if page.After == "" {
		return page, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(page.After)
	var saved selectionCursor
	if err != nil || json.Unmarshal(raw, &saved) != nil || saved.Binding != binding || saved.Peer == "" {
		return page, kernel.ErrInvalidCursor
	}
	page.After = saved.Peer
	page, err = page.Normalize()
	if err != nil {
		return page, fmt.Errorf("pull peer continuation: %w", err)
	}
	return page, nil
}

func selectionNext(binding, next string) string {
	if next == "" {
		return ""
	}
	// Both fields are strings; JSON encoding cannot fail.
	raw, _ := json.Marshal(selectionCursor{Binding: binding, Peer: next})
	return base64.RawURLEncoding.EncodeToString(raw)
}
