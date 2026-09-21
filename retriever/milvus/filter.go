package milvus

import (
	"fmt"
	"strconv"
	"strings"
)

type RetrieveFilterOption string

func (r RetrieveFilterOption) String() string {
	return string(r)
}

func EmptyRetrieverFilterOption() RetrieveFilterOption {
	return RetrieveFilterOption("")
}

func IntEquals(elem string, value int64) RetrieveFilterOption {
	return RetrieveFilterOption(fmt.Sprintf(`%s == %d`, elem, value))
}

func IntNotEquals(elem string, value int64) RetrieveFilterOption {
	return RetrieveFilterOption(fmt.Sprintf(`%s != %d`, elem, value))
}

func IntGreaterThan(elem string, value int64) RetrieveFilterOption {
	return RetrieveFilterOption(fmt.Sprintf(`%s > %d`, elem, value))
}

func IntLessThan(elem string, value int64) RetrieveFilterOption {
	return RetrieveFilterOption(fmt.Sprintf(`%s < %d`, elem, value))
}

func IntIn(elem string, values []int64) RetrieveFilterOption {
	list := make([]string, 0)
	for _, v := range values {
		list = append(list, strconv.FormatInt(v, 10))
	}
	return RetrieveFilterOption(fmt.Sprintf(`%s in %s`, elem, "["+strings.Join(list, ", ")+"]"))
}

func IntNotIn(elem string, values []int64) RetrieveFilterOption {
	list := make([]string, 0)
	for _, v := range values {
		list = append(list, strconv.FormatInt(v, 10))
	}
	return RetrieveFilterOption(fmt.Sprintf(`%s not in %s`, elem, "["+strings.Join(list, ", ")+"]"))
}

func StringEquals(elem string, value string) RetrieveFilterOption {
	return RetrieveFilterOption(fmt.Sprintf(`%s == "%s"`, elem, value))
}

func StringNotEquals(elem string, value string) RetrieveFilterOption {
	return RetrieveFilterOption(fmt.Sprintf(`%s != "%s"`, elem, value))
}

func StringLike(elem string, value string) RetrieveFilterOption {
	return RetrieveFilterOption(fmt.Sprintf(`%s like "%s"`, elem, value))
}

func StringIn(elem string, values []string) RetrieveFilterOption {
	return RetrieveFilterOption(fmt.Sprintf(`%s in %s`, elem, `["`+strings.Join(values, `", "`)+`"]`))
}

func StringNotIn(elem string, values []string) RetrieveFilterOption {
	return RetrieveFilterOption(fmt.Sprintf(`%s not in %s`, elem, `["`+strings.Join(values, `", "`)+`"]`))
}

func ArrayContainsAll(elem string, tags []string) RetrieveFilterOption {
	tagList := `["` + strings.Join(tags, `", "`) + `"]`
	return RetrieveFilterOption(fmt.Sprintf(`array_contains_all(%s, %s)`, elem, tagList))
}

func ArrayContainsAny(elem string, tags []string) RetrieveFilterOption {
	tagList := `["` + strings.Join(tags, `", "`) + `"]`
	return RetrieveFilterOption(fmt.Sprintf(`array_contains_any(%s, %s)`, elem, tagList))
}

func Or(ops []RetrieveFilterOption) RetrieveFilterOption {
	items := make([]string, 0)
	for _, op := range ops {
		items = append(items, "("+op.String()+")")
	}

	return RetrieveFilterOption(strings.Join(items, " || "))
}

func And(ops []RetrieveFilterOption) RetrieveFilterOption {
	items := make([]string, 0)
	for _, op := range ops {
		items = append(items, "("+op.String()+")")
	}

	return RetrieveFilterOption(strings.Join(items, " && "))
}
