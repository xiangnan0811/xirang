package content

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidRepresentationRequest = errors.New("invalid content representation request")

type HTTPRangeKind string

const (
	HTTPRangeFull      HTTPRangeKind = "full"
	HTTPRangeNormal    HTTPRangeKind = "normal"
	HTTPRangeOpenEnded HTTPRangeKind = "open_ended"
	HTTPRangeSuffix    HTTPRangeKind = "suffix"
)

type RequestFailureCode string

const (
	RequestFailureInvalidRange         RequestFailureCode = "invalid_range"
	RequestFailureRangeNotAllowed      RequestFailureCode = "range_not_allowed"
	RequestFailureIfRangeFullForbidden RequestFailureCode = "if_range_full_forbidden"
	RequestFailureRequestTooLarge      RequestFailureCode = "request_too_large"
	RequestFailureBudgetExhausted      RequestFailureCode = "budget_exhausted"
	RequestFailureClientCanceled       RequestFailureCode = "client_canceled"
	RequestFailureWriteFailed          RequestFailureCode = "write_failed"
	RequestFailureSourceFailed         RequestFailureCode = "source_failed"
	RequestFailureReconciledCrash      RequestFailureCode = "reconciled_crash"
	RequestFailureInternal             RequestFailureCode = "internal_failure"
)

type HTTPRange struct {
	Kind         HTTPRangeKind
	Start        *int64
	EndExclusive *int64
	SuffixLength *int64
	Offset       int64
	Length       int64
}

type RepresentationRequest struct {
	Method           string
	RangeHeaders     []string
	IfRangeHeaders   []string
	Size             int64
	ETag             string
	LastModified     *time.Time
	RangePolicy      RangePolicy
	Seekable         bool
	FullAllowed      bool
	MaxResponseBytes int64
}

type RepresentationPlan struct {
	Status        int
	Range         HTTPRange
	ContentLength int64
	ContentRange  string
	AcceptRanges  string
	ETag          string
	LastModified  string
	WriteBody     bool
	FailureCode   RequestFailureCode
}

func PlanRepresentation(request RepresentationRequest) (RepresentationPlan, error) {
	if !validRepresentationRequest(request) {
		return RepresentationPlan{}, ErrInvalidRepresentationRequest
	}

	base := RepresentationPlan{
		AcceptRanges: acceptRanges(request),
		ETag:         request.ETag,
		LastModified: formatLastModified(request.LastModified),
	}
	if len(request.RangeHeaders) == 0 {
		return planFullRepresentation(request, base, false), nil
	}

	selected, ok := parseSingleRange(request.RangeHeaders, request.Size)
	if !ok {
		return planRangeFailure(request, base, RequestFailureInvalidRange), nil
	}
	if request.RangePolicy != RangeSingle || !request.Seekable {
		return planRangeFailure(request, base, RequestFailureRangeNotAllowed), nil
	}
	if len(request.IfRangeHeaders) > 0 && !ifRangeMatches(request) {
		return planFullRepresentation(request, base, true), nil
	}
	if selected.Length > request.MaxResponseBytes {
		base.Status = http.StatusRequestEntityTooLarge
		base.FailureCode = RequestFailureRequestTooLarge
		return base, nil
	}

	base.Status = http.StatusPartialContent
	base.Range = selected
	base.ContentLength = selected.Length
	base.ContentRange = "bytes " + formatRangeInteger(selected.Offset) + "-" +
		formatRangeInteger(selected.Offset+selected.Length-1) + "/" + formatRangeInteger(request.Size)
	base.WriteBody = request.Method == http.MethodGet
	return base, nil
}

func validRepresentationRequest(request RepresentationRequest) bool {
	return (request.Method == http.MethodGet || request.Method == http.MethodHead) &&
		request.Size >= 0 && validEntityTag(request.ETag) && request.MaxResponseBytes > 0 &&
		(request.RangePolicy == RangeNone || request.RangePolicy == RangeSingle)
}

func planFullRepresentation(request RepresentationRequest, plan RepresentationPlan, ifRangeFallback bool) RepresentationPlan {
	if !request.FullAllowed || request.Size > request.MaxResponseBytes {
		if ifRangeFallback {
			plan.Status = http.StatusPreconditionFailed
			plan.FailureCode = RequestFailureIfRangeFullForbidden
		} else {
			plan.Status = http.StatusRequestEntityTooLarge
			plan.FailureCode = RequestFailureRequestTooLarge
		}
		return plan
	}
	plan.Status = http.StatusOK
	plan.Range = HTTPRange{Kind: HTTPRangeFull, Length: request.Size}
	plan.ContentLength = request.Size
	plan.WriteBody = request.Method == http.MethodGet
	return plan
}

func planRangeFailure(request RepresentationRequest, plan RepresentationPlan, failure RequestFailureCode) RepresentationPlan {
	plan.Status = http.StatusRequestedRangeNotSatisfiable
	plan.ContentRange = "bytes */" + formatRangeInteger(request.Size)
	plan.FailureCode = failure
	return plan
}

func parseSingleRange(headers []string, size int64) (HTTPRange, bool) {
	if len(headers) != 1 || size == 0 {
		return HTTPRange{}, false
	}
	value := headers[0]
	if !strings.HasPrefix(value, "bytes=") || strings.ContainsAny(value, " \t\r\n") {
		return HTTPRange{}, false
	}
	spec := strings.TrimPrefix(value, "bytes=")
	if spec == "" || strings.Contains(spec, ",") || strings.Count(spec, "-") != 1 {
		return HTTPRange{}, false
	}
	if strings.HasPrefix(spec, "-") {
		suffix, ok := parseRangeInteger(strings.TrimPrefix(spec, "-"))
		if !ok || suffix <= 0 {
			return HTTPRange{}, false
		}
		length := min(suffix, size)
		return HTTPRange{
			Kind: HTTPRangeSuffix, SuffixLength: integerPointer(suffix),
			Offset: size - length, Length: length,
		}, true
	}

	parts := strings.SplitN(spec, "-", 2)
	start, ok := parseRangeInteger(parts[0])
	if !ok || start >= size {
		return HTTPRange{}, false
	}
	if parts[1] == "" {
		return HTTPRange{
			Kind: HTTPRangeOpenEnded, Start: integerPointer(start),
			Offset: start, Length: size - start,
		}, true
	}
	end, ok := parseRangeInteger(parts[1])
	if !ok || end < start {
		return HTTPRange{}, false
	}
	endExclusive := size
	if end < size-1 {
		endExclusive = end + 1
	}
	return HTTPRange{
		Kind: HTTPRangeNormal, Start: integerPointer(start), EndExclusive: integerPointer(endExclusive),
		Offset: start, Length: endExclusive - start,
	}, true
}

func parseRangeInteger(value string) (int64, bool) {
	if value == "" {
		return 0, false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return parsed, err == nil
}

func ifRangeMatches(request RepresentationRequest) bool {
	if len(request.IfRangeHeaders) != 1 {
		return false
	}
	validator := strings.TrimSpace(request.IfRangeHeaders[0])
	if validEntityTag(validator) {
		return isStrongEntityTag(validator) && isStrongEntityTag(request.ETag) && validator == request.ETag
	}
	date, err := http.ParseTime(validator)
	if err != nil || request.LastModified == nil {
		return false
	}
	return !request.LastModified.UTC().Truncate(time.Second).After(date.UTC())
}

func validEntityTag(value string) bool {
	opaque := strings.TrimPrefix(value, "W/")
	if len(opaque) < 2 || opaque[0] != '"' || opaque[len(opaque)-1] != '"' {
		return false
	}
	for _, char := range opaque[1 : len(opaque)-1] {
		if char == '"' || char < 0x21 || char == 0x7f {
			return false
		}
	}
	return true
}

func isStrongEntityTag(value string) bool {
	return validEntityTag(value) && !strings.HasPrefix(value, "W/")
}

func acceptRanges(request RepresentationRequest) string {
	if request.RangePolicy == RangeSingle && request.Seekable {
		return "bytes"
	}
	return "none"
}

func formatLastModified(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Truncate(time.Second).Format(http.TimeFormat)
}

func formatRangeInteger(value int64) string {
	return strconv.FormatInt(value, 10)
}

func integerPointer(value int64) *int64 {
	return &value
}
