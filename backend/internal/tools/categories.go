package tools

import (
	"context"
	"encoding/json"
)

const listCategoriesDescription = `List the six spending categories used by UniFinSG with their English and Chinese labels. ` +
	`Call this when you need to choose a category for a transaction or explain categories to the user.`

func listCategoriesTool() Tool {
	return Tool{
		Name:        "list_categories",
		Description: listCategoriesDescription,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`),
		ReadOnly: true,
		Handler: func(_ context.Context, _ UserContext, args json.RawMessage) (any, error) {
			var input struct{}
			if err := decodeArgs(args, &input); err != nil {
				return nil, err
			}
			return map[string]any{"categories": Categories}, nil
		},
	}
}
