package search

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
)

type CanonicalQuery struct {
	Request SearchRequest
	JSON    []byte
}

type queryValidationState struct {
	limits QueryLimits
	nodes  int
}

func DecodeAndCanonicalize(raw []byte, limits QueryLimits) (CanonicalQuery, error) {
	if len(raw) == 0 || len(raw) > limits.MaxBodyBytes {
		return CanonicalQuery{}, ErrInvalidQuery
	}
	var request SearchRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return CanonicalQuery{}, fmt.Errorf("%w: request schema", ErrInvalidQuery)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return CanonicalQuery{}, fmt.Errorf("%w: trailing request data", ErrInvalidQuery)
	}
	return ValidateAndCanonicalize(request, limits)
}

func ValidateAndCanonicalize(request SearchRequest, limits QueryLimits) (CanonicalQuery, error) {
	if !validQueryLimits(limits) || request.SchemaVersion != QuerySchemaVersion ||
		request.Limit <= 0 || request.Limit > limits.MaxPageSize || len(request.Cursor) > 8192 ||
		!validSearchSort(request.Sort) {
		return CanonicalQuery{}, ErrInvalidQuery
	}
	raw, err := json.Marshal(request)
	if err != nil || len(raw) > limits.MaxBodyBytes {
		return CanonicalQuery{}, ErrInvalidQuery
	}
	state := queryValidationState{limits: limits}
	root, _, err := state.canonicalizeNode(request.Root, 1)
	if err != nil {
		return CanonicalQuery{}, err
	}
	scope, err := canonicalizeScope(request.Scope)
	if err != nil {
		return CanonicalQuery{}, err
	}
	request.Root = root
	request.Scope = scope
	queryMaterial := request
	queryMaterial.Cursor = ""
	canonicalJSON, err := json.Marshal(queryMaterial)
	if err != nil || len(canonicalJSON) > limits.MaxBodyBytes {
		return CanonicalQuery{}, ErrInvalidQuery
	}
	return CanonicalQuery{Request: request, JSON: canonicalJSON}, nil
}

func (state *queryValidationState) canonicalizeNode(node QueryNode, depth int) (QueryNode, []byte, error) {
	state.nodes++
	if depth > state.limits.MaxDepth || state.nodes > state.limits.MaxNodes {
		return QueryNode{}, nil, ErrInvalidQuery
	}
	if len(node.Values) > state.limits.MaxValuesPerNode {
		return QueryNode{}, nil, ErrInvalidQuery
	}
	for _, value := range append(append([]string(nil), node.Values...), node.Text) {
		if value != "" && (!utf8.ValidString(value) || len(value) > state.limits.MaxValueBytes ||
			utf8.RuneCountInString(value) > state.limits.MaxValueRunes || strings.ContainsRune(value, 0)) {
			return QueryNode{}, nil, ErrInvalidQuery
		}
	}

	switch node.Op {
	case QueryOpAnd, QueryOpOr:
		if node.Field != "" || node.Text != "" || len(node.Values) != 0 || node.From != nil || node.To != nil || len(node.Children) < 2 {
			return QueryNode{}, nil, ErrInvalidQuery
		}
		childrenByJSON := make(map[string]QueryNode, len(node.Children))
		keys := make([]string, 0, len(node.Children))
		for _, child := range node.Children {
			canonical, encoded, err := state.canonicalizeNode(child, depth+1)
			if err != nil {
				return QueryNode{}, nil, err
			}
			key := string(encoded)
			if _, exists := childrenByJSON[key]; !exists {
				childrenByJSON[key] = canonical
				keys = append(keys, key)
			}
		}
		if len(keys) < 2 {
			return QueryNode{}, nil, ErrInvalidQuery
		}
		sort.Strings(keys)
		node.Children = make([]QueryNode, len(keys))
		for index, key := range keys {
			node.Children[index] = childrenByJSON[key]
		}
	case QueryOpNot:
		if node.Field != "" || node.Text != "" || len(node.Values) != 0 || node.From != nil || node.To != nil || len(node.Children) != 1 {
			return QueryNode{}, nil, ErrInvalidQuery
		}
		child, _, err := state.canonicalizeNode(node.Children[0], depth+1)
		if err != nil {
			return QueryNode{}, nil, err
		}
		node.Children = []QueryNode{child}
	case QueryOpTerm:
		if !validTermField(node.Field) || strings.TrimSpace(node.Text) == "" || len(node.Values) != 0 ||
			node.From != nil || node.To != nil || len(node.Children) != 0 {
			return QueryNode{}, nil, ErrInvalidQuery
		}
		normalizerField := node.Field
		if normalizerField == SearchFieldAny {
			normalizerField = SearchFieldName
		}
		normalized, err := NormalizeFieldV1(normalizerField, node.Text, NormalizerLimits{
			MaxInputBytes: state.limits.MaxValueBytes, MaxRunes: state.limits.MaxValueRunes,
			MaxTokens: state.limits.MaxValuesPerNode * 16, MaxTokenRunes: state.limits.MaxValueRunes,
		})
		if err != nil {
			return QueryNode{}, nil, ErrInvalidQuery
		}
		node.Text = normalized.Canonical
		node.Children = nil
	case QueryOpType:
		if node.Field != "" || node.Text != "" || len(node.Values) == 0 || node.From != nil || node.To != nil || len(node.Children) != 0 {
			return QueryNode{}, nil, ErrInvalidQuery
		}
		values := dedupeSortedStrings(node.Values)
		for _, value := range values {
			if !validEntryTypeValue(value) {
				return QueryNode{}, nil, ErrInvalidQuery
			}
		}
		node.Values = values
		node.Children = nil
	case QueryOpModifiedTime:
		if node.Field != "" || node.Text != "" || len(node.Values) != 0 || len(node.Children) != 0 || (node.From == nil && node.To == nil) {
			return QueryNode{}, nil, ErrInvalidQuery
		}
		if node.From != nil {
			value := node.From.UTC()
			node.From = &value
		}
		if node.To != nil {
			value := node.To.UTC()
			node.To = &value
		}
		if node.From != nil && node.To != nil && node.From.After(*node.To) {
			return QueryNode{}, nil, ErrInvalidQuery
		}
		node.Children = nil
	default:
		return QueryNode{}, nil, ErrInvalidQuery
	}
	encoded, err := json.Marshal(node)
	if err != nil {
		return QueryNode{}, nil, ErrInvalidQuery
	}
	return node, encoded, nil
}

func canonicalizeScope(scope SearchScope) (SearchScope, error) {
	if scope.Mode != SearchScopeCurrent && scope.Mode != SearchScopeAllRetained && scope.Mode != SearchScopeExactPoints {
		return SearchScope{}, ErrInvalidQuery
	}
	scope.RepositoryIDs = dedupeSortedStrings(scope.RepositoryIDs)
	scope.RecoveryPointIDs = dedupeSortedStrings(scope.RecoveryPointIDs)
	sort.Slice(scope.TaskIDs, func(left, right int) bool { return scope.TaskIDs[left] < scope.TaskIDs[right] })
	if len(scope.TaskIDs) > 1 {
		unique := scope.TaskIDs[:1]
		for _, id := range scope.TaskIDs[1:] {
			if id != unique[len(unique)-1] {
				unique = append(unique, id)
			}
		}
		scope.TaskIDs = unique
	}
	for _, id := range append(append([]string(nil), scope.RepositoryIDs...), scope.RecoveryPointIDs...) {
		if backupasset.ValidateOpaqueID(id) != nil {
			return SearchScope{}, ErrInvalidQuery
		}
	}
	for _, id := range scope.TaskIDs {
		if id == 0 {
			return SearchScope{}, ErrInvalidQuery
		}
	}
	if scope.Mode == SearchScopeExactPoints {
		if len(scope.RecoveryPointIDs) == 0 || len(scope.RepositoryIDs) != 0 || len(scope.TaskIDs) != 0 {
			return SearchScope{}, ErrInvalidQuery
		}
	} else if len(scope.RecoveryPointIDs) != 0 {
		return SearchScope{}, ErrInvalidQuery
	}
	return scope, nil
}

func dedupeSortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	if len(result) < 2 {
		return result
	}
	unique := result[:1]
	for _, value := range result[1:] {
		if value != unique[len(unique)-1] {
			unique = append(unique, value)
		}
	}
	return unique
}

func validQueryLimits(limits QueryLimits) bool {
	return limits.MaxBodyBytes > 0 && limits.MaxBodyBytes <= 1024*1024 && limits.MaxDepth > 0 &&
		limits.MaxDepth <= 64 && limits.MaxNodes > 0 && limits.MaxNodes <= 4096 &&
		limits.MaxValuesPerNode > 0 && limits.MaxValuesPerNode <= 1024 && limits.MaxValueBytes > 0 &&
		limits.MaxValueRunes > 0 && limits.MaxValueRunes <= limits.MaxValueBytes && limits.MaxPageSize > 0 &&
		limits.MaxCandidates >= limits.MaxPageSize && limits.MaxExecutionTime > 0 && limits.MaxSuggestions >= 0
}

func validTermField(field SearchField) bool {
	switch field {
	case SearchFieldAny, SearchFieldName, SearchFieldPath, SearchFieldExtension, SearchFieldTag, SearchFieldContent, SearchFieldOCR:
		return true
	default:
		return false
	}
}

func validEntryTypeValue(value string) bool {
	switch backupasset.CatalogEntryType(value) {
	case backupasset.CatalogEntryFile, backupasset.CatalogEntryDirectory, backupasset.CatalogEntrySymlink,
		backupasset.CatalogEntryHardlink, backupasset.CatalogEntrySpecial, backupasset.CatalogEntryUnknown:
		return true
	default:
		return false
	}
}

func validSearchSort(value SearchSort) bool {
	return value == SearchSortRelevance || value == SearchSortNameAsc || value == SearchSortModifiedDesc
}
