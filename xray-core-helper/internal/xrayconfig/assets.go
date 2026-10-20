package xrayconfig

import (
	"context"
	"sort"
	"strings"
)

// ReferencedExternalAssets returns filenames used by Xray's ext:file:tag
// geodata syntax without exposing configuration contents.
func (reader Reader) ReferencedExternalAssets(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	doc, err := reader.readDocument(ctx)
	if err != nil {
		return nil, err
	}

	referenced := make(map[string]struct{})
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for _, child := range value {
				visit(child)
			}
		case []any:
			for _, child := range value {
				visit(child)
			}
		case string:
			for _, token := range strings.Split(value, ",") {
				token = strings.TrimSpace(token)
				if !strings.HasPrefix(token, "ext:") {
					continue
				}
				remaining := token[len("ext:"):]
				separator := strings.IndexByte(remaining, ':')
				if separator < 0 {
					continue
				}
				if separator > 0 {
					referenced[remaining[:separator]] = struct{}{}
				}
			}
		}
	}
	visit(doc.value)

	assets := make([]string, 0, len(referenced))
	for asset := range referenced {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	return assets, nil
}
