package developmentnotifications

import (
	"fmt"

	collectionquery "github.com/sipeed/picoclaw/pkg/collectionquery"
)

var notificationCollectionQuerySchema = mustNotificationCollectionQuerySchema()

func mustNotificationCollectionQuerySchema() collectionquery.Schema {
	equality := []collectionquery.Operator{
		collectionquery.OperatorEqual,
		collectionquery.OperatorNotEqual,
		collectionquery.OperatorIn,
		collectionquery.OperatorNotIn,
	}
	text := []collectionquery.Operator{
		collectionquery.OperatorEqual,
		collectionquery.OperatorNotEqual,
		collectionquery.OperatorContains,
		collectionquery.OperatorNotContains,
		collectionquery.OperatorIn,
		collectionquery.OperatorNotIn,
	}
	timestamp := []collectionquery.Operator{
		collectionquery.OperatorEqual,
		collectionquery.OperatorNotEqual,
		collectionquery.OperatorGreater,
		collectionquery.OperatorGreaterEq,
		collectionquery.OperatorLess,
		collectionquery.OperatorLessEq,
	}
	fields := []collectionquery.FieldSchema{
		{Name: collectionquery.Field(FieldStatus), Type: collectionquery.TypeEnum, Operators: equality, Sortable: true,
			SuggestedValues: []string{string(StatusOpen), string(StatusResolved), string(StatusArchived)}},
		{Name: collectionquery.Field(FieldRead), Type: collectionquery.TypeBoolean, Operators: equality, Sortable: true,
			SuggestedValues: []string{"false", "true"}},
		{Name: collectionquery.Field(FieldSnoozed), Type: collectionquery.TypeBoolean, Operators: equality, Sortable: true,
			SuggestedValues: []string{"false", "true"}},
		{Name: collectionquery.Field(FieldPriority), Type: collectionquery.TypeEnum, Operators: equality, Sortable: true,
			SuggestedValues: []string{string(PriorityCritical), string(PriorityHigh), string(PriorityMedium), string(PriorityLow)}},
		{Name: collectionquery.Field(FieldReason), Type: collectionquery.TypeEnum, Operators: equality, Sortable: true,
			SuggestedValues: []string{
				string(ReasonCharterAmbiguity), string(ReasonScopeException), string(ReasonSteeringScopeChange),
				string(ReasonImplementationBlocked), string(ReasonProviderOutcomeUnknown), string(ReasonPublicationApproval),
			}},
		{Name: collectionquery.Field(FieldRepository), Type: collectionquery.TypeString, Operators: text, Sortable: true},
		{Name: collectionquery.Field(FieldWorkspace), Type: collectionquery.TypeString, Operators: text, Sortable: true},
		{Name: collectionquery.Field(FieldIntent), Type: collectionquery.TypeEnum, Operators: equality, Sortable: true,
			SuggestedValues: []string{string(IntentImplementFeature), string(IntentPickupPR)}},
		{Name: collectionquery.Field(FieldSource), Type: collectionquery.TypeEnum, Operators: equality, Sortable: true,
			SuggestedValues: []string{string(SourceIssue), string(SourceBrief), string(SourcePullRequest)}},
		{Name: collectionquery.Field(FieldPhase), Type: collectionquery.TypeString, Operators: text, Sortable: true},
		{Name: collectionquery.Field(FieldCreated), Type: collectionquery.TypeTimestamp, Operators: timestamp, Sortable: true},
		{Name: collectionquery.Field(FieldUpdated), Type: collectionquery.TypeTimestamp, Operators: timestamp, Sortable: true},
		{Name: collectionquery.Field(FieldText), Type: collectionquery.TypeString, Operators: text, Sortable: false},
	}
	schema, err := collectionquery.NewSchema(fields, []collectionquery.SortField{{
		Field: collectionquery.Field(FieldUpdated), Direction: collectionquery.Descending,
	}})
	if err != nil {
		panic(err)
	}
	return schema
}

// QuerySchema returns a detached schema projection suitable for API
// autocomplete metadata.
func QuerySchema() collectionquery.Schema {
	return notificationCollectionQuerySchema.Clone()
}

func notificationQueryFromCollection(parsed collectionquery.Query) (Query, error) {
	filter, err := notificationExpressionFromCollection(parsed.Filter)
	if err != nil {
		return Query{}, &QueryError{Position: 0, Message: "invalid query"}
	}
	order := make([]SortField, len(parsed.Order))
	for index, field := range parsed.Order {
		order[index] = SortField{Field: Field(field.Field), Direction: Direction(field.Direction)}
	}
	query := Query{Filter: filter, Order: order}
	if err := query.Validate(); err != nil {
		return Query{}, &QueryError{Position: 0, Message: "invalid query"}
	}
	return query, nil
}

func notificationExpressionFromCollection(expression collectionquery.Expression) (Expression, error) {
	switch node := expression.(type) {
	case nil:
		return nil, nil
	case collectionquery.Predicate:
		values := make([]Value, len(node.Values))
		for index, value := range node.Values {
			values[index] = notificationValueFromCollection(value)
		}
		return Predicate{Field: Field(node.Field), Operator: Operator(node.Operator), Values: values}, nil
	case collectionquery.LogicalExpression:
		left, err := notificationExpressionFromCollection(node.Left)
		if err != nil {
			return nil, err
		}
		right, err := notificationExpressionFromCollection(node.Right)
		if err != nil {
			return nil, err
		}
		return LogicalExpression{Operator: LogicalOperator(node.Operator), Left: left, Right: right}, nil
	case collectionquery.Negation:
		expression, err := notificationExpressionFromCollection(node.Expression)
		if err != nil {
			return nil, err
		}
		return Negation{Expression: expression}, nil
	default:
		return nil, fmt.Errorf("unsupported collection query expression")
	}
}

func notificationValueFromCollection(value collectionquery.Value) Value {
	switch value.Kind {
	case collectionquery.ValueBoolean:
		return Value{Kind: ValueBool, Bool: value.Boolean}
	case collectionquery.ValueTimestamp:
		return Value{Kind: ValueTime, Time: value.Timestamp}
	case collectionquery.ValueRelativeTimestamp:
		return Value{Kind: ValueRelativeTime, Text: value.Text, TimeOffset: value.TimeOffset}
	default:
		return Value{Kind: ValueString, Text: value.Text}
	}
}

func collectionQueryFromNotification(query Query) (collectionquery.Query, error) {
	if err := query.Validate(); err != nil {
		return collectionquery.Query{}, err
	}
	filter, err := collectionExpressionFromNotification(query.Filter)
	if err != nil {
		return collectionquery.Query{}, err
	}
	order := make([]collectionquery.SortField, len(query.Order))
	for index, field := range query.Order {
		order[index] = collectionquery.SortField{
			Field: collectionquery.Field(field.Field), Direction: collectionquery.Direction(field.Direction),
		}
	}
	return collectionquery.NewQuery(notificationCollectionQuerySchema, filter, order)
}

func collectionExpressionFromNotification(expression Expression) (collectionquery.Expression, error) {
	switch node := expression.(type) {
	case nil:
		return nil, nil
	case Predicate:
		return collectionPredicateFromNotification(node)
	case *Predicate:
		if node == nil {
			return nil, fmt.Errorf("nil predicate")
		}
		return collectionPredicateFromNotification(*node)
	case LogicalExpression:
		return collectionLogicalFromNotification(node)
	case *LogicalExpression:
		if node == nil {
			return nil, fmt.Errorf("nil logical expression")
		}
		return collectionLogicalFromNotification(*node)
	case Negation:
		return collectionNegationFromNotification(node)
	case *Negation:
		if node == nil {
			return nil, fmt.Errorf("nil negation")
		}
		return collectionNegationFromNotification(*node)
	default:
		return nil, fmt.Errorf("unsupported notification query expression")
	}
}

func collectionPredicateFromNotification(predicate Predicate) (collectionquery.Expression, error) {
	values := make([]collectionquery.Value, len(predicate.Values))
	for index, value := range predicate.Values {
		values[index] = collectionValueFromNotification(value)
	}
	return collectionquery.Predicate{
		Field: collectionquery.Field(predicate.Field), Operator: collectionquery.Operator(predicate.Operator), Values: values,
	}, nil
}

func collectionLogicalFromNotification(expression LogicalExpression) (collectionquery.Expression, error) {
	left, err := collectionExpressionFromNotification(expression.Left)
	if err != nil {
		return nil, err
	}
	right, err := collectionExpressionFromNotification(expression.Right)
	if err != nil {
		return nil, err
	}
	return collectionquery.LogicalExpression{
		Operator: collectionquery.LogicalOperator(expression.Operator), Left: left, Right: right,
	}, nil
}

func collectionNegationFromNotification(expression Negation) (collectionquery.Expression, error) {
	nested, err := collectionExpressionFromNotification(expression.Expression)
	if err != nil {
		return nil, err
	}
	return collectionquery.Negation{Expression: nested}, nil
}

func collectionValueFromNotification(value Value) collectionquery.Value {
	switch value.Kind {
	case ValueBool:
		return collectionquery.Value{Kind: collectionquery.ValueBoolean, Boolean: value.Bool}
	case ValueTime:
		return collectionquery.Value{Kind: collectionquery.ValueTimestamp, Timestamp: value.Time}
	case ValueRelativeTime:
		return collectionquery.Value{
			Kind: collectionquery.ValueRelativeTimestamp, Text: value.Text, TimeOffset: value.TimeOffset,
		}
	default:
		return collectionquery.Value{Kind: collectionquery.ValueString, Text: value.Text}
	}
}
