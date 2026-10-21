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

// UserlistRoutingAssets identifies USERLIST files actually used by permanent
// proxy rules. A DNS reference alone does not replace dynamic routing.
func (reader Reader) UserlistRoutingAssets(ctx context.Context) (map[string]bool, error) {
	doc, err := reader.readDocument(ctx)
	if err != nil {
		return nil, err
	}
	routing, _ := doc.value["routing"].(map[string]any)
	rules, _ := routing["rules"].([]any)
	assets := map[string]bool{}
	for tag, field := range map[string]string{"proxy-domains-permanent": "domain", "proxy-ip-permanent": "ip"} {
		var selected map[string]any
		count := 0
		for _, raw := range rules {
			rule, _ := raw.(map[string]any)
			if rule["ruleTag"] == tag {
				selected = rule
				count++
			}
		}
		if count != 1 || selected["outboundTag"] != "proxy" {
			continue
		}
		values, _ := selected[field].([]any)
		for _, raw := range values {
			value, _ := raw.(string)
			parts := strings.Split(value, ":")
			if len(parts) == 3 && parts[0] == "ext" && parts[1] != "" && parts[2] == "USERLIST" {
				assets[parts[1]] = true
			}
		}
	}
	return assets, nil
}
