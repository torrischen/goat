package milvus

import (
	"context"
	"fmt"

	"github.com/torrischen/goat/util"

	"github.com/bytedance/sonic"
	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

const DefaultLimit = 8

func ResolveSearchMode(args *SearchArgs, defaultMode SearchMode) SearchMode {
	if args == nil {
		return SearchModeQuery
	}
	if args.SearchMode != SearchModeAuto {
		return args.SearchMode
	}
	if args.Text == "" {
		return SearchModeQuery
	}

	return defaultMode
}

func SearchLimit(args *SearchArgs) int {
	if args == nil {
		return DefaultLimit
	}
	if args.Limit > 0 {
		return args.Limit
	}

	return DefaultLimit
}

func MergeOutputFields(base []string, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	fields := make([]string, 0, len(base)+len(extra))
	for _, field := range append(base, extra...) {
		if field == "" {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		fields = append(fields, field)
	}

	return fields
}

func MergeOutputFieldsWithFieldsJSON(base []string, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra)+1)
	fields := make([]string, 0, len(base)+len(extra)+1)
	add := func(field string) {
		if field == "" {
			return
		}
		if _, ok := seen[field]; ok {
			return
		}
		seen[field] = struct{}{}
		fields = append(fields, field)
	}

	for _, field := range base {
		add(field)
	}
	add(ColumnFields)
	for _, field := range extra {
		if isKnownOutputField(field) {
			add(field)
			continue
		}
		add(ColumnFields)
	}

	return fields
}

func FieldsJSONColumn(elements []*Element) (*column.ColumnJSONBytes, error) {
	values := make([][]byte, 0, len(elements))
	for _, element := range elements {
		if element == nil || len(element.Fields) == 0 {
			values = append(values, []byte("{}"))
			continue
		}
		if err := validateFields(element.Fields); err != nil {
			return nil, err
		}
		data, err := sonic.Marshal(element.Fields)
		if err != nil {
			return nil, err
		}
		values = append(values, data)
	}

	return column.NewColumnJSONBytes(ColumnFields, values), nil
}

func AppendFieldsJSONColumn(ctx context.Context, cols []column.Column, elements []*Element, indexManager *FieldsIndexManager, autoIndex bool) ([]column.Column, error) {
	fieldsColumn, err := FieldsJSONColumn(elements)
	if err != nil {
		return nil, err
	}
	if autoIndex && indexManager != nil {
		if err := indexManager.EnsureFromElements(ctx, elements); err != nil {
			return nil, err
		}
	}

	return append(cols, fieldsColumn), nil
}

func FieldsFromColumn(col column.Column, index int) (Fields, error) {
	if col == nil {
		return nil, nil
	}
	value, err := columnValue(col, index)
	if err != nil {
		return nil, err
	}
	values, ok := value.(map[string]any)
	if !ok {
		return nil, nil
	}

	fields := NewFields()
	for key, item := range values {
		fields.Set(key, item)
	}

	return fields, nil
}

func RetrievalsFromResultSets(resultSets []milvusclient.ResultSet, contentField string, useScores bool) (Retrievals, error) {
	result := make([]*Retrieval, 0)
	for i := range resultSets {
		if resultSets[i].Err != nil {
			return nil, resultSets[i].Err
		}
		retrievals, err := RetrievalsFromResultSet(resultSets[i], contentField, useScores)
		if err != nil {
			return nil, err
		}
		result = append(result, retrievals...)
	}

	return result, nil
}

func RetrievalsFromResultSet(resultSet milvusclient.ResultSet, contentField string, useScores bool) (Retrievals, error) {
	idColumn := resultSet.GetColumn(ColumnID)
	if idColumn == nil {
		idColumn = resultSet.IDs
	}
	tagColumn := resultSet.GetColumn(ColumnTag)
	contentColumn := resultSet.GetColumn(contentField)

	if idColumn == nil || tagColumn == nil {
		return nil, fmt.Errorf("required result column missing, may be empty results")
	}

	result := make([]*Retrieval, 0, idColumn.Len())
	for i := 0; i < idColumn.Len(); i++ {
		id, err := idColumn.GetAsInt64(i)
		if err != nil {
			return nil, err
		}
		tag, err := stringSliceValue(tagColumn, i)
		if err != nil {
			return nil, err
		}
		fields, err := extraFields(resultSet.Fields, i, contentField)
		if err != nil {
			return nil, err
		}

		retrieval := &Retrieval{
			ID:     id,
			Tag:    tag,
			Fields: fields,
		}
		if contentColumn != nil {
			content, err := contentColumn.GetAsString(i)
			if err != nil {
				return nil, err
			}
			retrieval.Content = content
		}
		if useScores && i < len(resultSet.Scores) {
			retrieval.Distance = util.AbsFloat32(resultSet.Scores[i])
		}
		result = append(result, retrieval)
	}

	return result, nil
}

func validateFields(fields Fields) error {
	for name := range fields {
		if err := validateFieldName(name); err != nil {
			return err
		}
	}

	return nil
}

func validateFieldName(name string) error {
	if name == "" {
		return fmt.Errorf("field name is empty")
	}

	return nil
}

func stringSliceValue(col column.Column, index int) ([]string, error) {
	value, err := col.Get(index)
	if err != nil {
		return nil, err
	}
	tags, ok := value.([]string)
	if !ok {
		return nil, fmt.Errorf("tag column type error")
	}

	return tags, nil
}

func extraFields(columns []column.Column, index int, contentField string) (Fields, error) {
	fields := NewFields()
	for _, col := range columns {
		name := col.Name()
		if name == ColumnFields {
			values, err := FieldsFromColumn(col, index)
			if err != nil {
				continue
			}
			for key, item := range values {
				fields.Set(key, item)
			}
			continue
		}
		if isBaseResultField(name, contentField) {
			continue
		}
		value, err := columnValue(col, index)
		if err != nil {
			fields.Set(name, nil)
			continue
		}
		fields.Set(name, value)
	}
	if len(fields) == 0 {
		return nil, nil
	}

	return fields, nil
}

func isBaseResultField(name string, contentField string) bool {
	switch name {
	case ColumnID, ColumnTag:
		return true
	}

	return contentField != "" && name == contentField
}

func isKnownOutputField(name string) bool {
	switch name {
	case ColumnID, ColumnTag, ColumnContent, ColumnFields, ColumnEmbedding, ColumnText, ColumnSparse:
		return true
	default:
		return false
	}
}

func columnValue(col column.Column, index int) (any, error) {
	value, err := col.Get(index)
	if err != nil {
		return nil, err
	}

	switch col.(type) {
	case *column.ColumnJSONBytes:
		raw, ok := value.([]byte)
		if !ok {
			return value, nil
		}
		return jsonBytesValue(raw)
	default:
		return value, nil
	}
}

func jsonBytesValue(raw []byte) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value any
	if err := sonic.Unmarshal(raw, &value); err != nil {
		return util.ByteToString(raw), nil
	}

	return value, nil
}
