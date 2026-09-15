package service

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// normalizeAccountIDWhitelist accepts either a JSON array or a comma/whitespace
// separated list and returns a canonical JSON array of positive numeric IDs.
func normalizeAccountIDWhitelist(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "[]"
	}

	var tokens []string
	if strings.HasPrefix(raw, "[") {
		var ids []int64
		if err := json.Unmarshal([]byte(raw), &ids); err == nil {
			for _, id := range ids {
				if id > 0 {
					tokens = append(tokens, strconv.FormatInt(id, 10))
				}
			}
		}
	} else {
		tokens = strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
		})
	}

	seen := make(map[int64]struct{}, len(tokens))
	ids := make([]int64, 0, len(tokens))
	for _, token := range tokens {
		id, err := strconv.ParseInt(strings.TrimSpace(token), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	encoded, err := json.Marshal(ids)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func parseAccountIDWhitelist(raw string) map[int64]struct{} {
	canonical := normalizeAccountIDWhitelist(raw)
	var ids []int64
	if err := json.Unmarshal([]byte(canonical), &ids); err != nil {
		return nil
	}
	result := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		result[id] = struct{}{}
	}
	return result
}
