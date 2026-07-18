package search

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestASTCanonicalizesClosedSchemaScopeAndCommutativeChildren(t *testing.T) {
	pointA := strings.Repeat("a", 32)
	pointB := strings.Repeat("b", 32)
	request := SearchRequest{
		SchemaVersion: QuerySchemaVersion,
		Root: QueryNode{Op: QueryOpAnd, Children: []QueryNode{
			{Op: QueryOpType, Values: []string{"file", "directory", "file"}},
			{Op: QueryOpTerm, Field: SearchFieldName, Text: "ＲＥＰＯＲＴ"},
			{Op: QueryOpTerm, Field: SearchFieldName, Text: "report"},
		}},
		Scope: SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{pointB, pointA, pointB}},
		Sort:  SearchSortRelevance,
		Limit: 25,
	}
	canonical, err := ValidateAndCanonicalize(request, DefaultQueryLimits())
	if err != nil {
		t.Fatalf("ValidateAndCanonicalize: %v", err)
	}
	if len(canonical.Request.Root.Children) != 2 || canonical.Request.Root.Children[0].Op > canonical.Request.Root.Children[1].Op {
		t.Fatalf("commutative children were not deduplicated/sorted: %#v", canonical.Request.Root.Children)
	}
	if got := canonical.Request.Scope.RecoveryPointIDs; len(got) != 2 || got[0] != pointA || got[1] != pointB {
		t.Fatalf("exact scope IDs were not sorted/deduplicated: %v", got)
	}
	if bytes.Contains(canonical.JSON, []byte("ＲＥＰＯＲＴ")) || !bytes.Contains(canonical.JSON, []byte("report")) {
		t.Fatalf("canonical JSON did not normalize term text: %s", canonical.JSON)
	}

	reversed := request
	reversed.Root.Children[0], reversed.Root.Children[1] = reversed.Root.Children[1], reversed.Root.Children[0]
	reversed.Scope.RecoveryPointIDs = []string{pointA, pointB}
	other, err := ValidateAndCanonicalize(reversed, DefaultQueryLimits())
	if err != nil {
		t.Fatalf("ValidateAndCanonicalize reversed: %v", err)
	}
	if !bytes.Equal(canonical.JSON, other.JSON) {
		t.Fatalf("canonical bytes drifted by input order:\n%s\n%s", canonical.JSON, other.JSON)
	}
}

func TestASTRejectsUnknownInvalidAndOverLimitProducts(t *testing.T) {
	now := time.Date(2026, 7, 18, 4, 5, 6, 0, time.UTC)
	valid := SearchRequest{
		SchemaVersion: QuerySchemaVersion,
		Root:          QueryNode{Op: QueryOpTerm, Field: SearchFieldName, Text: "report"},
		Scope:         SearchScope{Mode: SearchScopeCurrent},
		Sort:          SearchSortRelevance,
		Limit:         25,
	}
	testCases := []struct {
		name   string
		mutate func(*SearchRequest, *QueryLimits)
	}{
		{name: "schema", mutate: func(request *SearchRequest, _ *QueryLimits) { request.SchemaVersion++ }},
		{name: "unknown op", mutate: func(request *SearchRequest, _ *QueryLimits) { request.Root.Op = "future" }},
		{name: "unknown field", mutate: func(request *SearchRequest, _ *QueryLimits) { request.Root.Field = "future" }},
		{name: "and arity", mutate: func(request *SearchRequest, _ *QueryLimits) {
			request.Root = QueryNode{Op: QueryOpAnd, Children: []QueryNode{{Op: QueryOpTerm, Field: SearchFieldName, Text: "one"}}}
		}},
		{name: "not arity", mutate: func(request *SearchRequest, _ *QueryLimits) {
			request.Root = QueryNode{Op: QueryOpNot, Children: []QueryNode{valid.Root, valid.Root}}
		}},
		{name: "leaf children", mutate: func(request *SearchRequest, _ *QueryLimits) { request.Root.Children = []QueryNode{valid.Root} }},
		{name: "term missing text", mutate: func(request *SearchRequest, _ *QueryLimits) { request.Root.Text = "" }},
		{name: "term extra values", mutate: func(request *SearchRequest, _ *QueryLimits) { request.Root.Values = []string{"file"} }},
		{name: "type invalid", mutate: func(request *SearchRequest, _ *QueryLimits) {
			request.Root = QueryNode{Op: QueryOpType, Values: []string{"future"}}
		}},
		{name: "time missing bounds", mutate: func(request *SearchRequest, _ *QueryLimits) { request.Root = QueryNode{Op: QueryOpModifiedTime} }},
		{name: "time reverse", mutate: func(request *SearchRequest, _ *QueryLimits) {
			from, to := now, now.Add(-time.Minute)
			request.Root = QueryNode{Op: QueryOpModifiedTime, From: &from, To: &to}
		}},
		{name: "sort", mutate: func(request *SearchRequest, _ *QueryLimits) { request.Sort = "future" }},
		{name: "empty exact", mutate: func(request *SearchRequest, _ *QueryLimits) {
			request.Scope = SearchScope{Mode: SearchScopeExactPoints}
		}},
		{name: "mixed exact", mutate: func(request *SearchRequest, _ *QueryLimits) {
			request.Scope = SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{strings.Repeat("a", 32)}, RepositoryIDs: []string{strings.Repeat("b", 32)}}
		}},
		{name: "dynamic point ids", mutate: func(request *SearchRequest, _ *QueryLimits) {
			request.Scope.RecoveryPointIDs = []string{strings.Repeat("a", 32)}
		}},
		{name: "bad opaque id", mutate: func(request *SearchRequest, _ *QueryLimits) {
			request.Scope.RepositoryIDs = []string{"repository-name"}
		}},
		{name: "page", mutate: func(request *SearchRequest, limits *QueryLimits) { request.Limit = limits.MaxPageSize + 1 }},
		{name: "depth", mutate: func(request *SearchRequest, limits *QueryLimits) {
			limits.MaxDepth = 1
			request.Root = QueryNode{Op: QueryOpNot, Children: []QueryNode{valid.Root}}
		}},
		{name: "nodes", mutate: func(request *SearchRequest, limits *QueryLimits) {
			limits.MaxNodes = 1
			request.Root = QueryNode{Op: QueryOpNot, Children: []QueryNode{valid.Root}}
		}},
		{name: "values", mutate: func(request *SearchRequest, limits *QueryLimits) {
			limits.MaxValuesPerNode = 1
			request.Root = QueryNode{Op: QueryOpType, Values: []string{"file", "directory"}}
		}},
		{name: "value bytes", mutate: func(request *SearchRequest, limits *QueryLimits) {
			limits.MaxValueBytes = 3
			request.Root.Text = "report"
		}},
		{name: "body bytes", mutate: func(_ *SearchRequest, limits *QueryLimits) { limits.MaxBodyBytes = 8 }},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			request := valid
			limits := DefaultQueryLimits()
			testCase.mutate(&request, &limits)
			if _, err := ValidateAndCanonicalize(request, limits); !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("got %v, want ErrInvalidQuery", err)
			}
		})
	}
}

func TestASTStrictDecodeRejectsUnknownTrailingAndInvalidTime(t *testing.T) {
	valid := `{"schema_version":1,"root":{"op":"term","field":"name","text":"report"},"scope":{"mode":"current"},"sort":"relevance","limit":25}`
	if _, err := DecodeAndCanonicalize([]byte(valid), DefaultQueryLimits()); err != nil {
		t.Fatalf("decode valid query: %v", err)
	}
	for _, raw := range []string{
		strings.Replace(valid, `"limit":25`, `"limit":25,"future":true`, 1),
		valid + `{}`,
		`{"schema_version":1,"root":{"op":"modified_time","from":"not-a-time"},"scope":{"mode":"current"},"sort":"relevance","limit":25}`,
	} {
		if _, err := DecodeAndCanonicalize([]byte(raw), DefaultQueryLimits()); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("strict decode accepted %q: %v", raw, err)
		}
	}
}
