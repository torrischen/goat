package milvus

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

const fieldsIndexNameLimit = 120

type JSONFieldCastType string

const (
	JSONFieldCastInt     JSONFieldCastType = "int"
	JSONFieldCastDouble  JSONFieldCastType = "double"
	JSONFieldCastVarchar JSONFieldCastType = "varchar"
	JSONFieldCastBool    JSONFieldCastType = "bool"
)

type FieldsIndex struct {
	JSONPath  string
	CastType  JSONFieldCastType
	IndexType index.IndexType
	IndexName string
}

func NewFieldsIndex(fieldName string, castType JSONFieldCastType) FieldsIndex {
	return FieldsIndex{
		JSONPath: FieldsPath(fieldName),
		CastType: castType,
	}
}

func NewFieldsPathIndex(jsonPath string, castType JSONFieldCastType) FieldsIndex {
	return FieldsIndex{
		JSONPath: jsonPath,
		CastType: castType,
	}
}

func FieldsPath(keys ...string) string {
	path := ColumnFields
	for _, key := range keys {
		path = appendJSONPath(path, key)
	}

	return path
}

func FieldsIndexesFromElements(elements []*Element) ([]FieldsIndex, error) {
	indexesByPath := make(map[string]FieldsIndex)
	for _, element := range elements {
		if element == nil {
			continue
		}
		for name, value := range element.Fields {
			if err := validateFieldName(name); err != nil {
				return nil, err
			}
			collectFieldsIndexes(FieldsPath(name), value, indexesByPath)
		}
	}

	indexes := make([]FieldsIndex, 0, len(indexesByPath))
	for _, idx := range indexesByPath {
		indexes = append(indexes, idx)
	}

	return indexes, nil
}

func NormalizeFieldsIndexes(indexes []FieldsIndex) ([]FieldsIndex, error) {
	normalized := make([]FieldsIndex, 0, len(indexes))
	seen := make(map[string]struct{}, len(indexes))
	for _, idx := range indexes {
		item, err := normalizeFieldsIndex(idx)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[item.IndexName]; ok {
			continue
		}
		seen[item.IndexName] = struct{}{}
		normalized = append(normalized, item)
	}

	return normalized, nil
}

func CreateFieldsIndexes(ctx context.Context, milvusClient *milvusclient.Client, collectionName string, indexes []FieldsIndex) error {
	manager := NewFieldsIndexManager(collectionName, milvusClient)
	return manager.Ensure(ctx, indexes)
}

type FieldsIndexManager struct {
	collectionName string
	milvusClient   *milvusclient.Client

	mu    sync.Mutex
	known map[string]struct{}
}

func NewFieldsIndexManager(collectionName string, milvusClient *milvusclient.Client) *FieldsIndexManager {
	return &FieldsIndexManager{
		collectionName: collectionName,
		milvusClient:   milvusClient,
	}
}

func (m *FieldsIndexManager) EnsureFromElements(ctx context.Context, elements []*Element) error {
	indexes, err := FieldsIndexesFromElements(elements)
	if err != nil {
		return err
	}

	return m.Ensure(ctx, indexes)
}

func (m *FieldsIndexManager) Ensure(ctx context.Context, indexes []FieldsIndex) error {
	if m == nil || m.milvusClient == nil || len(indexes) == 0 {
		return nil
	}

	normalized, err := NormalizeFieldsIndexes(indexes)
	if err != nil {
		return err
	}
	if len(normalized) == 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.loadKnownLocked(ctx); err != nil {
		return err
	}

	for _, idx := range normalized {
		if _, ok := m.known[idx.IndexName]; ok {
			continue
		}
		if err := m.createLocked(ctx, idx); err != nil {
			return err
		}
		m.known[idx.IndexName] = struct{}{}
	}

	return nil
}

func (m *FieldsIndexManager) loadKnownLocked(ctx context.Context) error {
	if m.known != nil {
		return nil
	}

	names, err := m.milvusClient.ListIndexes(ctx, milvusclient.NewListIndexOption(m.collectionName))
	if err != nil {
		return err
	}

	m.known = make(map[string]struct{}, len(names))
	for _, name := range names {
		m.known[name] = struct{}{}
	}

	return nil
}

func (m *FieldsIndexManager) createLocked(ctx context.Context, idx FieldsIndex) error {
	jsonIndex := index.NewJSONPathIndex(idx.IndexType, string(idx.CastType), idx.JSONPath)
	opt := milvusclient.
		NewCreateIndexOption(m.collectionName, ColumnFields, jsonIndex).
		WithIndexName(idx.IndexName)

	task, err := m.milvusClient.CreateIndex(ctx, opt)
	if err != nil {
		if m.refreshKnownContains(ctx, idx.IndexName) {
			return nil
		}
		return fmt.Errorf("create fields json index %q: %w", idx.IndexName, err)
	}
	if err := task.Await(ctx); err != nil {
		return fmt.Errorf("create fields json index %q: %w", idx.IndexName, err)
	}

	return nil
}

func (m *FieldsIndexManager) refreshKnownContains(ctx context.Context, indexName string) bool {
	names, err := m.milvusClient.ListIndexes(ctx, milvusclient.NewListIndexOption(m.collectionName))
	if err != nil {
		return false
	}
	if m.known == nil {
		m.known = make(map[string]struct{}, len(names))
	}
	for _, name := range names {
		m.known[name] = struct{}{}
	}
	_, ok := m.known[indexName]

	return ok
}

func normalizeFieldsIndex(idx FieldsIndex) (FieldsIndex, error) {
	idx.JSONPath = strings.TrimSpace(idx.JSONPath)
	if idx.JSONPath == "" {
		return FieldsIndex{}, fmt.Errorf("fields json index path is empty")
	}
	if idx.CastType == "" {
		return FieldsIndex{}, fmt.Errorf("fields json index %q cast type is empty", idx.JSONPath)
	}
	if idx.IndexType == "" {
		idx.IndexType = defaultFieldsIndexType(idx.CastType)
	}
	if idx.IndexName == "" {
		idx.IndexName = fieldsIndexName(idx.JSONPath, idx.CastType)
	}

	return idx, nil
}

func collectFieldsIndexes(jsonPath string, value any, indexes map[string]FieldsIndex) {
	value, ok := unwrapFieldValue(value)
	if !ok {
		return
	}

	if isStringKeyMap(value) {
		mapValue := reflect.ValueOf(value)
		for _, key := range mapValue.MapKeys() {
			collectFieldsIndexes(appendJSONPath(jsonPath, key.String()), mapValue.MapIndex(key).Interface(), indexes)
		}
		return
	}

	castType, ok := fieldCastType(value)
	if !ok {
		return
	}

	if existing, ok := indexes[jsonPath]; ok {
		existing.CastType = mergeFieldCastType(existing.CastType, castType)
		existing.IndexType = defaultFieldsIndexType(existing.CastType)
		indexes[jsonPath] = existing
		return
	}

	indexes[jsonPath] = FieldsIndex{
		JSONPath:  jsonPath,
		CastType:  castType,
		IndexType: defaultFieldsIndexType(castType),
	}
}

func unwrapFieldValue(value any) (any, bool) {
	if value == nil {
		return nil, false
	}

	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Interface || rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, false
		}
		rv = rv.Elem()
	}

	return rv.Interface(), true
}

func isStringKeyMap(value any) bool {
	rv := reflect.ValueOf(value)
	return rv.Kind() == reflect.Map && rv.Type().Key().Kind() == reflect.String
}

func fieldCastType(value any) (JSONFieldCastType, bool) {
	switch v := value.(type) {
	case string:
		return JSONFieldCastVarchar, true
	case bool:
		return JSONFieldCastBool, true
	case int, int8, int16, int32, int64:
		return JSONFieldCastInt, true
	case uint, uint8, uint16, uint32, uint64:
		if uint64Value(value) <= maxInt64Uint() {
			return JSONFieldCastInt, true
		}
		return "", false
	case float32:
		return fieldFloatCastType(float64(v))
	case float64:
		return fieldFloatCastType(v)
	case json.Number:
		return fieldNumberCastType(v)
	default:
		return fieldReflectCastType(value)
	}
}

func fieldReflectCastType(value any) (JSONFieldCastType, bool) {
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.String:
		return JSONFieldCastVarchar, true
	case reflect.Bool:
		return JSONFieldCastBool, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return JSONFieldCastInt, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if rv.Uint() <= maxInt64Uint() {
			return JSONFieldCastInt, true
		}
	case reflect.Float32, reflect.Float64:
		return fieldFloatCastType(rv.Float())
	}

	return "", false
}

func fieldNumberCastType(value json.Number) (JSONFieldCastType, bool) {
	if _, err := strconv.ParseInt(value.String(), 10, 64); err == nil {
		return JSONFieldCastInt, true
	}
	f, err := strconv.ParseFloat(value.String(), 64)
	if err != nil {
		return "", false
	}

	return fieldFloatCastType(f)
}

func fieldFloatCastType(value float64) (JSONFieldCastType, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "", false
	}
	if math.Trunc(value) == value && value >= minInt64Float() && value <= maxInt64Float() {
		return JSONFieldCastInt, true
	}

	return JSONFieldCastDouble, true
}

func mergeFieldCastType(left JSONFieldCastType, right JSONFieldCastType) JSONFieldCastType {
	if left == right {
		return left
	}
	if isNumericFieldCastType(left) && isNumericFieldCastType(right) {
		return JSONFieldCastDouble
	}

	return left
}

func isNumericFieldCastType(castType JSONFieldCastType) bool {
	return castType == JSONFieldCastInt || castType == JSONFieldCastDouble
}

func defaultFieldsIndexType(castType JSONFieldCastType) index.IndexType {
	if castType == JSONFieldCastBool {
		return index.BITMAP
	}

	return index.Inverted
}

func appendJSONPath(parent string, key string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(key)
	return parent + `["` + escaped + `"]`
}

func fieldsIndexName(jsonPath string, castType JSONFieldCastType) string {
	raw := sanitizeIndexName(jsonPath) + "_" + sanitizeIndexName(string(castType)) + "_idx"
	if len(raw) <= fieldsIndexNameLimit {
		return raw
	}

	sum := sha1.Sum([]byte(raw))
	hash := hex.EncodeToString(sum[:])[:10]
	prefixLen := fieldsIndexNameLimit - len(hash) - 1

	return raw[:prefixLen] + "_" + hash
}

func sanitizeIndexName(value string) string {
	var builder strings.Builder
	lastUnderscore := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if valid {
			builder.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}

	result := strings.Trim(builder.String(), "_")
	if result == "" {
		result = "field"
	}
	if result[0] >= '0' && result[0] <= '9' {
		result = "field_" + result
	}

	return result
}

func uint64Value(value any) uint64 {
	switch v := value.(type) {
	case uint:
		return uint64(v)
	case uint8:
		return uint64(v)
	case uint16:
		return uint64(v)
	case uint32:
		return uint64(v)
	case uint64:
		return v
	default:
		return 0
	}
}

func maxInt64Uint() uint64 {
	return uint64(^uint64(0) >> 1)
}

func minInt64Float() float64 {
	return float64(-1 << 63)
}

func maxInt64Float() float64 {
	return float64(1<<63 - 1)
}
