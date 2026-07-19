package content

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestRangePlansFullNormalOpenAndSuffixRepresentations(t *testing.T) {
	modified := time.Date(2026, 7, 18, 8, 9, 10, 987_654_321, time.UTC)
	base := RepresentationRequest{
		Method:           http.MethodGet,
		Size:             100,
		ETag:             `"strong-v1"`,
		LastModified:     &modified,
		RangePolicy:      RangeSingle,
		Seekable:         true,
		FullAllowed:      true,
		MaxResponseBytes: 1_000,
	}

	tests := []struct {
		name         string
		rangeHeaders []string
		wantStatus   int
		wantKind     HTTPRangeKind
		wantOffset   int64
		wantLength   int64
		wantRange    string
	}{
		{name: "full", wantStatus: http.StatusOK, wantKind: HTTPRangeFull, wantLength: 100},
		{name: "normal", rangeHeaders: []string{"bytes=10-19"}, wantStatus: http.StatusPartialContent, wantKind: HTTPRangeNormal, wantOffset: 10, wantLength: 10, wantRange: "bytes 10-19/100"},
		{name: "normal end clamps", rangeHeaders: []string{"bytes=95-999"}, wantStatus: http.StatusPartialContent, wantKind: HTTPRangeNormal, wantOffset: 95, wantLength: 5, wantRange: "bytes 95-99/100"},
		{name: "open ended", rangeHeaders: []string{"bytes=75-"}, wantStatus: http.StatusPartialContent, wantKind: HTTPRangeOpenEnded, wantOffset: 75, wantLength: 25, wantRange: "bytes 75-99/100"},
		{name: "suffix", rangeHeaders: []string{"bytes=-12"}, wantStatus: http.StatusPartialContent, wantKind: HTTPRangeSuffix, wantOffset: 88, wantLength: 12, wantRange: "bytes 88-99/100"},
		{name: "oversized suffix", rangeHeaders: []string{"bytes=-500"}, wantStatus: http.StatusPartialContent, wantKind: HTTPRangeSuffix, wantLength: 100, wantRange: "bytes 0-99/100"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			request := base
			request.RangeHeaders = testCase.rangeHeaders
			plan, err := PlanRepresentation(request)
			if err != nil {
				t.Fatalf("plan representation: %v", err)
			}
			if plan.Status != testCase.wantStatus || plan.Range.Kind != testCase.wantKind ||
				plan.Range.Offset != testCase.wantOffset || plan.Range.Length != testCase.wantLength {
				t.Fatalf("plan=%+v", plan)
			}
			if plan.ContentRange != testCase.wantRange || plan.ContentLength != testCase.wantLength {
				t.Fatalf("headers plan=%+v", plan)
			}
			if plan.AcceptRanges != "bytes" || plan.ETag != base.ETag ||
				plan.LastModified != modified.Truncate(time.Second).Format(http.TimeFormat) || !plan.WriteBody {
				t.Fatalf("representation headers/body plan=%+v", plan)
			}
		})
	}
}

func TestRangeRejectsMalformedDuplicateMultipartOverflowAndUnsatisfiedInputs(t *testing.T) {
	base := RepresentationRequest{
		Method: http.MethodGet, Size: 100, ETag: `"strong-v1"`, RangePolicy: RangeSingle,
		Seekable: true, FullAllowed: true, MaxResponseBytes: 1_000,
	}
	tests := []struct {
		name    string
		headers []string
		size    int64
	}{
		{name: "duplicate fields", headers: []string{"bytes=0-1", "bytes=2-3"}},
		{name: "multipart", headers: []string{"bytes=0-1,2-3"}},
		{name: "wrong unit", headers: []string{"items=0-1"}},
		{name: "unit case", headers: []string{"Bytes=0-1"}},
		{name: "whitespace", headers: []string{"bytes= 0-1"}},
		{name: "plus", headers: []string{"bytes=+1-2"}},
		{name: "negative normal", headers: []string{"bytes=-1-2"}},
		{name: "zero suffix", headers: []string{"bytes=-0"}},
		{name: "empty", headers: []string{"bytes="}},
		{name: "end before start", headers: []string{"bytes=9-2"}},
		{name: "start overflow", headers: []string{"bytes=9223372036854775808-"}},
		{name: "end overflow", headers: []string{"bytes=0-9223372036854775808"}},
		{name: "suffix overflow", headers: []string{"bytes=-9223372036854775808"}},
		{name: "start at eof", headers: []string{"bytes=100-"}},
		{name: "start past eof", headers: []string{"bytes=101-102"}},
		{name: "zero size", headers: []string{"bytes=0-0"}, size: -1},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			request := base
			request.RangeHeaders = testCase.headers
			if testCase.size < 0 {
				request.Size = 0
			}
			plan, err := PlanRepresentation(request)
			if err != nil {
				t.Fatalf("invalid client Range must produce a frozen HTTP plan, got %v", err)
			}
			if plan.Status != http.StatusRequestedRangeNotSatisfiable ||
				plan.ContentRange != "bytes */"+formatRangeInteger(request.Size) ||
				plan.ContentLength != 0 || plan.WriteBody || plan.FailureCode != RequestFailureInvalidRange {
				t.Fatalf("invalid range plan=%+v", plan)
			}
		})
	}
}

func TestRangePolicyBudgetAndIfRangeStatusMatrix(t *testing.T) {
	modified := time.Date(2026, 7, 18, 8, 9, 10, 0, time.UTC)
	base := RepresentationRequest{
		Method: http.MethodGet, Size: 100, ETag: `"strong-v1"`, LastModified: &modified,
		RangeHeaders: []string{"bytes=10-19"}, RangePolicy: RangeSingle, Seekable: true,
		FullAllowed: true, MaxResponseBytes: 1_000,
	}
	tests := []struct {
		name        string
		edit        func(*RepresentationRequest)
		wantStatus  int
		wantLength  int64
		wantFailure RequestFailureCode
	}{
		{name: "strong etag matches", edit: func(v *RepresentationRequest) { v.IfRangeHeaders = []string{`"strong-v1"`} }, wantStatus: 206, wantLength: 10},
		{name: "date matches", edit: func(v *RepresentationRequest) { v.IfRangeHeaders = []string{modified.Format(http.TimeFormat)} }, wantStatus: 206, wantLength: 10},
		{name: "later date matches", edit: func(v *RepresentationRequest) {
			v.IfRangeHeaders = []string{modified.Add(time.Second).Format(http.TimeFormat)}
		}, wantStatus: 206, wantLength: 10},
		{name: "etag mismatch falls back full", edit: func(v *RepresentationRequest) { v.IfRangeHeaders = []string{`"other"`} }, wantStatus: 200, wantLength: 100},
		{name: "weak request etag never matches", edit: func(v *RepresentationRequest) { v.IfRangeHeaders = []string{`W/"strong-v1"`} }, wantStatus: 200, wantLength: 100},
		{name: "weak current etag never matches entity tag", edit: func(v *RepresentationRequest) { v.ETag = `W/"weak-v1"`; v.IfRangeHeaders = []string{`W/"weak-v1"`} }, wantStatus: 200, wantLength: 100},
		{name: "older date falls back full", edit: func(v *RepresentationRequest) {
			v.IfRangeHeaders = []string{modified.Add(-time.Second).Format(http.TimeFormat)}
		}, wantStatus: 200, wantLength: 100},
		{name: "invalid if range falls back full", edit: func(v *RepresentationRequest) { v.IfRangeHeaders = []string{"not-a-validator"} }, wantStatus: 200, wantLength: 100},
		{name: "mismatch full forbidden", edit: func(v *RepresentationRequest) { v.IfRangeHeaders = []string{`"other"`}; v.FullAllowed = false }, wantStatus: 412, wantFailure: RequestFailureIfRangeFullForbidden},
		{name: "mismatch full over budget", edit: func(v *RepresentationRequest) { v.IfRangeHeaders = []string{`"other"`}; v.MaxResponseBytes = 50 }, wantStatus: 412, wantFailure: RequestFailureIfRangeFullForbidden},
		{name: "range over request budget", edit: func(v *RepresentationRequest) { v.MaxResponseBytes = 5 }, wantStatus: 413, wantFailure: RequestFailureRequestTooLarge},
		{name: "full over request budget", edit: func(v *RepresentationRequest) { v.RangeHeaders = nil; v.MaxResponseBytes = 99 }, wantStatus: 413, wantFailure: RequestFailureRequestTooLarge},
		{name: "range contract none", edit: func(v *RepresentationRequest) { v.RangePolicy = RangeNone }, wantStatus: 416, wantFailure: RequestFailureRangeNotAllowed},
		{name: "source cannot seek", edit: func(v *RepresentationRequest) { v.Seekable = false }, wantStatus: 416, wantFailure: RequestFailureRangeNotAllowed},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			request := base
			testCase.edit(&request)
			plan, err := PlanRepresentation(request)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Status != testCase.wantStatus || plan.ContentLength != testCase.wantLength || plan.FailureCode != testCase.wantFailure {
				t.Fatalf("plan=%+v", plan)
			}
			if plan.Status == http.StatusRequestedRangeNotSatisfiable && plan.ContentRange != "bytes */100" {
				t.Fatalf("416 content range=%q", plan.ContentRange)
			}
		})
	}
}

func TestRangeHEADUsesGETStatusAndHeadersWithoutBody(t *testing.T) {
	for _, rangeHeaders := range [][]string{nil, {"bytes=7-11"}} {
		request := RepresentationRequest{
			Method: http.MethodHead, Size: 20, ETag: `"strong-v1"`, RangeHeaders: rangeHeaders,
			RangePolicy: RangeSingle, Seekable: true, FullAllowed: true, MaxResponseBytes: 100,
		}
		plan, err := PlanRepresentation(request)
		if err != nil {
			t.Fatal(err)
		}
		wantStatus, wantLength := http.StatusOK, int64(20)
		if rangeHeaders != nil {
			wantStatus, wantLength = http.StatusPartialContent, 5
		}
		if plan.Status != wantStatus || plan.ContentLength != wantLength || plan.WriteBody {
			t.Fatalf("HEAD plan=%+v", plan)
		}
	}
}

func TestRangeRejectsInvalidServerRepresentationContracts(t *testing.T) {
	base := RepresentationRequest{
		Method: http.MethodGet, Size: 1, ETag: `"v1"`, RangePolicy: RangeSingle,
		Seekable: true, FullAllowed: true, MaxResponseBytes: 1,
	}
	tests := []RepresentationRequest{
		{},
		func() RepresentationRequest { v := base; v.Method = http.MethodPost; return v }(),
		func() RepresentationRequest { v := base; v.Size = -1; return v }(),
		func() RepresentationRequest { v := base; v.ETag = "bad\r\netag"; return v }(),
		func() RepresentationRequest { v := base; v.RangePolicy = RangePolicy("future"); return v }(),
		func() RepresentationRequest { v := base; v.MaxResponseBytes = 0; return v }(),
	}
	for index, request := range tests {
		if _, err := PlanRepresentation(request); !errors.Is(err, ErrInvalidRepresentationRequest) {
			t.Fatalf("invalid request %d got %v", index, err)
		}
	}
}
