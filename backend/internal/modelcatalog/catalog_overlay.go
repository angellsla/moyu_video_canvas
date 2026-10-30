package modelcatalog

import (
	"sort"
	"strings"
)

// MergeCatalogExtras enriches or appends locally discovered vendor entries
// onto a catalog that already came through the outbound /models fetch.
// Extras must not perform HTTP; they only fill IDs the standard catalog omitted.
func MergeCatalogExtras(catalog []ChannelModelCatalogItem, extras []ChannelModelCatalogItem) []ChannelModelCatalogItem {
	if len(extras) == 0 {
		return catalog
	}
	indexByID := make(map[string]int, len(catalog)+len(extras))
	for index := range catalog {
		indexByID[catalog[index].ID] = index
	}
	for _, item := range extras {
		item.ID = strings.TrimPrefix(strings.TrimSpace(item.ID), "models/")
		if item.ID == "" {
			continue
		}
		if index, exists := indexByID[item.ID]; exists {
			catalog[index] = enrichCatalogItem(catalog[index], item)
			continue
		}
		indexByID[item.ID] = len(catalog)
		catalog = append(catalog, item)
	}
	sort.Slice(catalog, func(left int, right int) bool {
		return catalog[left].ID < catalog[right].ID
	})
	return catalog
}

func enrichCatalogItem(current ChannelModelCatalogItem, fallback ChannelModelCatalogItem) ChannelModelCatalogItem {
	if current.DisplayName == "" {
		current.DisplayName = fallback.DisplayName
	}
	if current.ModelType == "" {
		current.ModelType = fallback.ModelType
	}
	if len(current.SupportedEndpointTypes) == 0 {
		current.SupportedEndpointTypes = fallback.SupportedEndpointTypes
	}
	return current
}
