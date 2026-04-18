package metrics

import (
	"testing"
	"time"
)

func TestSelectGranularity(t *testing.T) {
	cases := []struct {
		span time.Duration
		want Granularity
	}{
		{1 * time.Hour, GranularityRaw},
		{6 * time.Hour, GranularityRaw},
		{3 * 24 * time.Hour, GranularityRaw},
		{7 * 24 * time.Hour, GranularityHourly},
		{60 * 24 * time.Hour, GranularityHourly},
		{120 * 24 * time.Hour, GranularityDaily},
		{400 * 24 * time.Hour, GranularityDaily},
	}
	for _, c := range cases {
		got := SelectGranularity(c.span)
		if got != c.want {
			t.Errorf("span=%v want=%s got=%s", c.span, c.want, got)
		}
	}
}
