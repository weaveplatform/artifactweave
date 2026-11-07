package specoracle

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// Two JFrog pages documenting variants of one route must fold into a single
// union, so the comparison does not depend on which page loaded last.
func TestUnitAddMergesOperationsSharingAKey(t *testing.T) {
	page := func(header string, required bool, media, prop, code string) map[string]any {
		return map[string]any{
			"servers": []any{map[string]any{"url": "https://h/artifactory"}},
			"paths": map[string]any{"/{repoKey}/{filePath}": map[string]any{"put": map[string]any{
				"operationId": header,
				"parameters": []any{
					map[string]any{"name": "repoKey", "in": "path", "required": true},
					map[string]any{"name": header, "in": "header", "required": required},
				},
				"requestBody": map[string]any{"content": map[string]any{media: map[string]any{
					"schema": map[string]any{
						"type":       "object",
						"properties": map[string]any{prop: map[string]any{"type": "string"}},
					},
				}}},
				"responses": map[string]any{code: map[string]any{}},
			}}},
		}
	}
	for _, order := range [][2]int{{0, 1}, {1, 0}} {
		pages := []map[string]any{
			page("X-Checksum-Deploy", true, "application/octet-stream", "a", "201"),
			page("X-Explode-Archive", true, "application/zip", "b", "409"),
		}
		o := Oracle{Ops: map[string]Operation{}}
		o.add(pages[order[0]], "f", "s")
		o.add(pages[order[1]], "f", "s")
		op := o.Ops["PUT /artifactory/{}/{}"]
		var required []string
		for _, p := range op.Params {
			if p.Required {
				required = append(required, p.Name)
			}
		}
		if diff := cmp.Diff([]string{"repoKey"}, required); diff != "" {
			t.Errorf("required params (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(
			[]string{"application/octet-stream", "application/zip"},
			op.RequestMedia,
		); diff != "" {
			t.Errorf("media (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]string{"a", "b"}, op.RequestProps); diff != "" {
			t.Errorf("props (-want +got):\n%s", diff)
		}
		if len(op.Responses) != 2 || op.ID != "X-Checksum-Deploy" {
			t.Errorf("responses %v id %q", op.Responses, op.ID)
		}
	}
	if unionStrings(nil, nil) != nil {
		t.Error("empty union should be nil")
	}
}
