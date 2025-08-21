package util

import (
	"fmt"
	"sort"
)

func EqualSlices[T any](a, b []T, equal func(x, y T) bool, ignoreOrder bool) bool {
	if len(a) != len(b) {
		return false
	}

	if ignoreOrder {
		aCopy := append([]T(nil), a...)
		bCopy := append([]T(nil), b...)

		sort.Slice(aCopy, func(i, j int) bool {
			return fmt.Sprint(aCopy[i]) < fmt.Sprint(aCopy[j])
		})
		sort.Slice(bCopy, func(i, j int) bool {
			return fmt.Sprint(bCopy[i]) < fmt.Sprint(bCopy[j])
		})

		a, b = aCopy, bCopy
	}

	for i := range a {
		if !equal(a[i], b[i]) {
			return false
		}
	}
	return true
}
