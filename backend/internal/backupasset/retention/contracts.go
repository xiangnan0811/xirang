package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"xirang/backend/internal/backupasset"
)

const PolicyRulesVersion1 = 1

type CalendarUnit string

const (
	CalendarDay   CalendarUnit = "day"
	CalendarWeek  CalendarUnit = "week"
	CalendarMonth CalendarUnit = "month"
	CalendarYear  CalendarUnit = "year"
)

type AgeRule struct {
	KeepDays int `json:"keep_days"`
}

type CountRule struct {
	KeepLatest int `json:"keep_latest"`
}

type CalendarRule struct {
	Unit CalendarUnit `json:"unit"`
	Keep int          `json:"keep"`
}

type PolicyRules struct {
	Version  int            `json:"version"`
	Age      *AgeRule       `json:"age,omitempty"`
	Count    *CountRule     `json:"count,omitempty"`
	Calendar []CalendarRule `json:"calendar,omitempty"`
}

func CanonicalizePolicyRules(rules PolicyRules) (string, string, error) {
	canonical := rules
	canonical.Calendar = append([]CalendarRule(nil), rules.Calendar...)
	if err := validatePolicyRules(canonical); err != nil {
		return "", "", err
	}
	sort.Slice(canonical.Calendar, func(left, right int) bool {
		return calendarUnitRank(canonical.Calendar[left].Unit) < calendarUnitRank(canonical.Calendar[right].Unit)
	})
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", "", fmt.Errorf("%w: encode retention policy rules", backupasset.ErrInvalidState)
	}
	digest := sha256.Sum256(payload)
	return string(payload), hex.EncodeToString(digest[:]), nil
}

func ParsePolicyRules(payload string) (PolicyRules, error) {
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	var rules PolicyRules
	if err := decoder.Decode(&rules); err != nil {
		return PolicyRules{}, fmt.Errorf("%w: decode retention policy rules", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return PolicyRules{}, fmt.Errorf("%w: trailing retention policy rules data", backupasset.ErrInvalidState)
	}
	canonical, _, err := CanonicalizePolicyRules(rules)
	if err != nil {
		return PolicyRules{}, err
	}
	if strings.TrimSpace(payload) != canonical {
		return PolicyRules{}, fmt.Errorf("%w: retention policy rules are not canonical", backupasset.ErrInvalidState)
	}
	return rules, nil
}

func validatePolicyRules(rules PolicyRules) error {
	if rules.Version != PolicyRulesVersion1 || rules.Age == nil && rules.Count == nil && len(rules.Calendar) == 0 {
		return fmt.Errorf("%w: invalid retention policy rule version or empty selectors", backupasset.ErrInvalidState)
	}
	if rules.Age != nil && (rules.Age.KeepDays < 1 || rules.Age.KeepDays > 36500) {
		return fmt.Errorf("%w: retention policy age selector is out of bounds", backupasset.ErrInvalidState)
	}
	if rules.Count != nil && (rules.Count.KeepLatest < 1 || rules.Count.KeepLatest > 1000000) {
		return fmt.Errorf("%w: retention policy count selector is out of bounds", backupasset.ErrInvalidState)
	}
	if len(rules.Calendar) > 4 {
		return fmt.Errorf("%w: too many retention policy calendar selectors", backupasset.ErrInvalidState)
	}
	seen := make(map[CalendarUnit]bool, len(rules.Calendar))
	for _, rule := range rules.Calendar {
		if calendarUnitRank(rule.Unit) == 0 || rule.Keep < 1 || rule.Keep > 10000 || seen[rule.Unit] {
			return fmt.Errorf("%w: invalid retention policy calendar selector", backupasset.ErrInvalidState)
		}
		seen[rule.Unit] = true
	}
	return nil
}

func calendarUnitRank(unit CalendarUnit) int {
	switch unit {
	case CalendarDay:
		return 1
	case CalendarWeek:
		return 2
	case CalendarMonth:
		return 3
	case CalendarYear:
		return 4
	default:
		return 0
	}
}
