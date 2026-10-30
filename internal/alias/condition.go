package alias

import (
	"fmt"
	"strconv"
	"strings"
)

var conditionOperators = []string{"==", "!=", "<=", ">=", "<", ">"}

// Splits on the operator before substitution, values stay opaque
func EvaluateCondition(condition string, ctx *Context) (bool, error) {
	var lhs, operator, rhs string
	for _, op := range conditionOperators {
		if parts := strings.SplitN(condition, op, 2); len(parts) == 2 {
			lhs, operator, rhs = parts[0], op, parts[1]
			break
		}
	}
	if operator == "" {
		return false, fmt.Errorf("no operator in condition %q", condition)
	}
	actual := strings.TrimSpace(Substitute(lhs, ctx))
	expected := strings.TrimSpace(Substitute(rhs, ctx))
	return compareValues(actual, operator, expected)
}

// Numeric when both sides parse, string equality otherwise
func compareValues(actual, operator, expected string) (bool, error) {
	actualNum, actualErr := strconv.ParseFloat(actual, 64)
	expectedNum, expectedErr := strconv.ParseFloat(expected, 64)
	if actualErr == nil && expectedErr == nil {
		switch operator {
		case "==":
			return actualNum == expectedNum, nil
		case "!=":
			return actualNum != expectedNum, nil
		case "<":
			return actualNum < expectedNum, nil
		case ">":
			return actualNum > expectedNum, nil
		case "<=":
			return actualNum <= expectedNum, nil
		case ">=":
			return actualNum >= expectedNum, nil
		}
	}
	switch operator {
	case "==":
		return strings.EqualFold(actual, expected), nil
	case "!=":
		return !strings.EqualFold(actual, expected), nil
	}
	return false, fmt.Errorf("operator %s needs numeric values", operator)
}
