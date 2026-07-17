package workflows

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var expressionPattern = regexp.MustCompile(`\$\{\{\s*([^}]+?)\s*\}\}`)

type expressionContext struct {
	Inputs   map[string]any
	Secrets  map[string]string
	Event    map[string]any
	Steps    map[string]StepExecution
	Needs    map[string]JobExecution
	Jobs     map[string]JobExecution
	Delivery Delivery
	Session  string
}

func renderValue(value any, ctx expressionContext) (any, error) {
	switch v := value.(type) {
	case string:
		return renderString(v, ctx)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			rendered, err := renderValue(item, ctx)
			if err != nil {
				return nil, err
			}
			out[key] = rendered
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			rendered, err := renderValue(item, ctx)
			if err != nil {
				return nil, err
			}
			out = append(out, rendered)
		}
		return out, nil
	default:
		return value, nil
	}
}

func renderMap(values map[string]any, ctx expressionContext) (map[string]any, error) {
	out := make(map[string]any, len(values))
	for key, value := range values {
		rendered, err := renderValue(value, ctx)
		if err != nil {
			return nil, err
		}
		out[key] = rendered
	}
	return out, nil
}

func renderString(input string, ctx expressionContext) (any, error) {
	matches := expressionPattern.FindAllStringSubmatch(input, -1)
	if len(matches) == 0 {
		return input, nil
	}
	if len(matches) == 1 && strings.TrimSpace(matches[0][0]) == strings.TrimSpace(input) {
		return evalExpression(matches[0][1], ctx)
	}
	var firstErr error
	out := expressionPattern.ReplaceAllStringFunc(input, func(match string) string {
		if firstErr != nil {
			return ""
		}
		sub := expressionPattern.FindStringSubmatch(match)
		value, err := evalExpression(sub[1], ctx)
		if err != nil {
			firstErr = err
			return ""
		}
		return fmt.Sprint(value)
	})
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func evalIf(expr string, ctx expressionContext) (bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true, nil
	}
	if strings.HasPrefix(expr, "${{") && strings.HasSuffix(expr, "}}") {
		expr = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(expr, "${{"), "}}"))
	}
	value, err := evalExpression(expr, ctx)
	if err != nil {
		return false, err
	}
	return truthy(value), nil
}

func evalExpression(expr string, ctx expressionContext) (any, error) {
	expr = strings.TrimSpace(expr)
	for _, op := range []string{" == ", " != ", " >= ", " <= ", " > ", " < "} {
		if idx := strings.Index(expr, op); idx >= 0 {
			left, err := evalExpression(expr[:idx], ctx)
			if err != nil {
				return nil, err
			}
			right, err := evalExpression(expr[idx+len(op):], ctx)
			if err != nil {
				return nil, err
			}
			return compareValues(left, strings.TrimSpace(op), right), nil
		}
	}
	if strings.HasPrefix(expr, "not ") {
		value, err := evalExpression(strings.TrimSpace(strings.TrimPrefix(expr, "not ")), ctx)
		if err != nil {
			return nil, err
		}
		return !truthy(value), nil
	}
	if strings.HasPrefix(expr, "'") && strings.HasSuffix(expr, "'") && len(expr) >= 2 {
		return strings.Trim(expr, "'"), nil
	}
	if strings.HasPrefix(expr, `"`) && strings.HasSuffix(expr, `"`) && len(expr) >= 2 {
		return strings.Trim(expr, `"`), nil
	}
	switch expr {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	if n, err := strconv.ParseFloat(expr, 64); err == nil {
		return n, nil
	}
	return lookupPath(expr, ctx)
}

func lookupPath(path string, ctx expressionContext) (any, error) {
	parts := strings.Split(strings.TrimSpace(path), ".")
	if len(parts) == 0 || parts[0] == "" {
		return nil, fmt.Errorf("empty expression")
	}
	var cur any
	switch parts[0] {
	case "inputs":
		cur = ctx.Inputs
	case "secrets":
		cur = stringMapAny(ctx.Secrets)
	case "event":
		cur = ctx.Event
	case "steps":
		if len(parts) < 2 {
			return nil, fmt.Errorf("step id is required")
		}
		step, ok := ctx.Steps[parts[1]]
		if !ok {
			return nil, fmt.Errorf("step %q not found", parts[1])
		}
		cur = map[string]any{"outputs": step.Outputs, "status": step.Status, "error": step.Error}
		parts = append([]string{parts[0]}, parts[2:]...)
	case "needs":
		if len(parts) < 2 {
			return nil, fmt.Errorf("needed job id is required")
		}
		job, ok := ctx.Needs[parts[1]]
		if !ok {
			return nil, fmt.Errorf("needed job %q not found", parts[1])
		}
		cur = map[string]any{"outputs": job.Outputs, "status": job.Status, "error": job.Error}
		parts = append([]string{parts[0]}, parts[2:]...)
	case "jobs":
		if len(parts) < 2 {
			return nil, fmt.Errorf("job id is required")
		}
		job, ok := ctx.Jobs[parts[1]]
		if !ok {
			return nil, fmt.Errorf("job %q not found", parts[1])
		}
		cur = map[string]any{"outputs": job.Outputs, "status": job.Status, "error": job.Error}
		parts = append([]string{parts[0]}, parts[2:]...)
	case "delivery":
		cur = deliveryMap(ctx.Delivery)
	case "session":
		cur = ctx.Session
	default:
		return nil, fmt.Errorf("unknown expression root %q", parts[0])
	}
	for _, part := range parts[1:] {
		if part == "" {
			continue
		}
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cannot access %q on non-object", part)
		}
		cur, ok = obj[part]
		if !ok {
			return nil, fmt.Errorf("expression path %q not found", path)
		}
	}
	return cur, nil
}

func stringMapAny(values map[string]string) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func compareValues(left any, op string, right any) bool {
	switch op {
	case "==":
		return fmt.Sprint(left) == fmt.Sprint(right)
	case "!=":
		return fmt.Sprint(left) != fmt.Sprint(right)
	case ">", ">=", "<", "<=":
		lf, lok := asFloat(left)
		rf, rok := asFloat(right)
		if !lok || !rok {
			return false
		}
		switch op {
		case ">":
			return lf > rf
		case ">=":
			return lf >= rf
		case "<":
			return lf < rf
		case "<=":
			return lf <= rf
		}
	}
	return false
}

func asFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case string:
		n, err := strconv.ParseFloat(v, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return strings.TrimSpace(v) != "" && v != "false"
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	default:
		return true
	}
}

func deliveryMap(d Delivery) map[string]any {
	return map[string]any{
		"channel":             d.Channel,
		"chat_id":             d.ChatID,
		"topic_id":            d.TopicID,
		"thread_ts":           d.ThreadTS,
		"message_id":          d.MessageID,
		"reply_to_message_id": d.ReplyToMessageID,
		"reply_handles":       d.ReplyHandles,
	}
}
