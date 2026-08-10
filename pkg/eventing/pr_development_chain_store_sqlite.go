//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"database/sql"
	"errors"
)

type prDevelopmentControllerChainLink struct {
	ordinal      int
	previousHash string
	finalHash    string
	resolved     bool
}

type prDevelopmentControllerChainSpec[T any] struct {
	initialHash       string
	maximum           int
	scan              func(rowScanner) (T, error)
	link              func(T) prDevelopmentControllerChainLink
	discontinuousText string
	unresolvedText    string
	capacityText      string
}

func scanPRDevelopmentControllerChainRows[T any](
	rows *sql.Rows,
	spec prDevelopmentControllerChainSpec[T],
) ([]T, error) {
	items := make([]T, 0)
	previousHash := spec.initialHash
	for rows.Next() {
		item, err := spec.scan(rows)
		if err != nil {
			return nil, err
		}
		link := spec.link(item)
		if link.ordinal != len(items) || link.previousHash != previousHash {
			return nil, errors.New(spec.discontinuousText)
		}
		if len(items) > 0 && !spec.link(items[len(items)-1]).resolved {
			return nil, errors.New(spec.unresolvedText)
		}
		items = append(items, item)
		if len(items) > spec.maximum {
			return nil, errors.New(spec.capacityText)
		}
		previousHash = link.finalHash
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
