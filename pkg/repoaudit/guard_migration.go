package repoaudit

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// unmarshalRepositoryReviewGuardState upgrades the retired collection of
// token/cost/account fields only while reading durable profile/automation
// state. HTTP request decoding remains strict and accepts guard_expression
// only.
func unmarshalRepositoryReviewGuardState(data []byte, destination any) (bool, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return false, err
	}
	rawBudget, exists := root["budget"]
	if !exists || len(rawBudget) == 0 || string(rawBudget) == "null" {
		return false, json.Unmarshal(data, destination)
	}
	var budget map[string]json.RawMessage
	if err := json.Unmarshal(rawBudget, &budget); err != nil {
		return false, err
	}
	retired := false
	for _, field := range []string{
		"max_total_tokens", "max_estimated_cost_usd", "account_ids",
		"min_remaining_percent", "min_remaining_percent_by_window",
		"auto_resume", "pause_on_unknown", "check_interval_seconds",
	} {
		if _, exists := budget[field]; exists {
			retired = true
		}
	}
	var expression string
	_ = json.Unmarshal(budget["guard_expression"], &expression)
	if strings.TrimSpace(expression) == "" {
		expression = legacyRepositoryReviewGuardExpression(budget)
	}
	if !retired {
		return false, json.Unmarshal(data, destination)
	}
	encodedBudget, err := json.Marshal(RepositoryReviewBudgetPolicy{GuardExpression: expression})
	if err != nil {
		return false, err
	}
	root["budget"] = encodedBudget
	upgraded, err := json.Marshal(root)
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(upgraded, destination)
}

func legacyRepositoryReviewGuardExpression(budget map[string]json.RawMessage) string {
	terms := make([]string, 0, 8)
	var pauseOnUnknown bool
	_ = json.Unmarshal(budget["pause_on_unknown"], &pauseOnUnknown)
	var tokens int64
	if json.Unmarshal(budget["max_total_tokens"], &tokens) == nil && tokens > 0 {
		terms = append(terms, "spent.tokens.total < "+strconv.FormatInt(tokens, 10))
	}
	var cost float64
	if json.Unmarshal(budget["max_estimated_cost_usd"], &cost) == nil && cost > 0 {
		terms = append(terms, "spend.total.usd < "+strconv.FormatFloat(cost, 'g', -1, 64))
	}
	var minimum float64
	if json.Unmarshal(budget["min_remaining_percent"], &minimum) == nil && minimum > 0 {
		term := fmt.Sprintf("account.limits.any.remaining_percent >= %s", strconv.FormatFloat(minimum, 'g', -1, 64))
		if !pauseOnUnknown {
			term = "((not account.limits.exhausted_known or not account.limits.exhausted) and (not account.limits.any.observed or account.limits.any.minimum_remaining_percent >= " + strconv.FormatFloat(
				minimum,
				'g',
				-1,
				64,
			) + "))"
		}
		terms = append(terms, term)
	}
	var windows map[string]float64
	if json.Unmarshal(budget["min_remaining_percent_by_window"], &windows) == nil {
		keys := make([]string, 0, len(windows))
		for window, value := range windows {
			if value > 0 {
				keys = append(keys, window)
			}
		}
		sort.Strings(keys)
		for _, window := range keys {
			normalized := normalizeRepositoryReviewGuardLimitWindow(window)
			if strings.TrimSpace(window) == "*" || strings.EqualFold(strings.TrimSpace(window), "any") {
				normalized = "any"
			}
			if normalized == "" {
				continue
			}
			term := fmt.Sprintf("account.limits.%s.remaining_percent >= %s",
				normalized,
				strconv.FormatFloat(windows[window], 'g', -1, 64),
			)
			if !pauseOnUnknown {
				term = "((not account.limits.exhausted_known or not account.limits.exhausted) and (not account.limits." + normalized + ".observed or account.limits." + normalized + ".minimum_remaining_percent >= " + strconv.FormatFloat(
					windows[window],
					'g',
					-1,
					64,
				) + "))"
			}
			terms = append(terms, term)
		}
	}
	var accountIDs []string
	_ = json.Unmarshal(budget["account_ids"], &accountIDs)
	if len(accountIDs) > 0 {
		// Legacy account IDs selected telemetry only, not model execution. The
		// new policy binds both to one account; silently promoting either one or
		// many IDs would change credential authority. Keep it fail-closed until
		// an operator explicitly selects the execution account.
		terms = append(terms, "false")
	}
	if len(terms) == 0 && (len(accountIDs) > 0 || pauseOnUnknown) {
		term := "account.limits.known and not account.limits.exhausted"
		if !pauseOnUnknown {
			term = "not account.limits.exhausted_known or not account.limits.exhausted"
		}
		terms = append(terms, term)
	}
	expression := strings.Join(terms, " and ")
	if ValidateRepositoryReviewGuardExpression(expression) != nil {
		return "false"
	}
	return expression
}
