package milvus

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

func TestMilvusConfig(t *testing.T) {
	defaults := NewMilvusConfig()
	if defaults.MilvusAddress != "http://localhost:19530" {
		t.Fatalf("defaults = %+v", defaults)
	}
	cfg := NewMilvusConfig(WithMilvusAddress("addr"), WithMilvusUsername("user"), WithMilvusPassword("pass"))
	if *cfg != (MilvusConfig{MilvusAddress: "addr", MilvusUsername: "user", MilvusPassword: "pass"}) {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestFilters(t *testing.T) {
	tests := []struct {
		got  RetrieveFilterOption
		want string
	}{
		{EmptyRetrieverFilterOption(), ""},
		{IntEquals("age", 1), "age == 1"},
		{IntNotEquals("age", 2), "age != 2"},
		{IntGreaterThan("age", 3), "age > 3"},
		{IntLessThan("age", 4), "age < 4"},
		{IntIn("age", []int64{1, 2}), "age in [1, 2]"},
		{IntNotIn("age", []int64{1, 2}), "age not in [1, 2]"},
		{StringEquals("name", "a"), `name == "a"`},
		{StringNotEquals("name", "a"), `name != "a"`},
		{StringLike("name", "a%"), `name like "a%"`},
		{StringIn("name", []string{"a", "b"}), `name in ["a", "b"]`},
		{StringNotIn("name", []string{"a", "b"}), `name not in ["a", "b"]`},
		{ArrayContainsAll("tags", []string{"a", "b"}), `array_contains_all(tags, ["a", "b"])`},
		{ArrayContainsAny("tags", []string{"a", "b"}), `array_contains_any(tags, ["a", "b"])`},
		{Or([]RetrieveFilterOption{IntEquals("a", 1), IntEquals("b", 2)}), `(a == 1) || (b == 2)`},
		{And([]RetrieveFilterOption{IntEquals("a", 1), IntEquals("b", 2)}), `(a == 1) && (b == 2)`},
	}
	for _, test := range tests {
		if got := test.got.String(); got != test.want {
			t.Errorf("filter = %q, want %q", got, test.want)
		}
	}
}

func TestFieldsElementsAndRetrievals(t *testing.T) {
	fields := NewFields()
	fields.Set("name", "goat")
	if fields.Get("name") != "goat" || fields.Get("missing") != nil {
		t.Fatalf("fields = %v", fields)
	}
	encoded := fields.ToJSON()
	if got := NewFieldsFromJSON(encoded); !reflect.DeepEqual(got, fields) {
		t.Fatalf("JSON round trip = %v", got)
	}
	if got := NewFieldsFromJSONString(fields.ToJSONString()); !reflect.DeepEqual(got, fields) {
		t.Fatalf("string round trip = %v", got)
	}
	if got := NewFieldsFromObject(struct {
		Value int `json:"value"`
	}{3}); got.Get("value") != float64(3) {
		t.Fatalf("object fields = %v", got)
	}
	if NewFieldsFromJSON([]byte("bad")) != nil || NewFieldsFromObject(make(chan int)) != nil {
		t.Fatal("invalid fields input accepted")
	}
	bad := Fields{"channel": make(chan int)}
	if bad.ToJSON() != nil {
		t.Fatal("unmarshalable fields encoded")
	}

	element := NewElement(1, "text", []string{"tag"}, nil)
	if element.GetField("none") != nil {
		t.Fatal("nil fields returned a value")
	}
	element.SetField("score", 2)
	if element.GetField("score") != 2 {
		t.Fatalf("element fields = %v", element.Fields)
	}
	alias := NewElementWithFields(2, "other", nil, fields)
	if alias.ID != 2 || alias.TextToEmbed != "other" {
		t.Fatalf("element = %+v", alias)
	}

	r := Retrievals{{ID: 1}, {ID: 2}}
	if r.Len() != 2 || r.Index(-1) != nil || r.Index(2) != nil || r.Index(0).ID != 1 || r.Max().ID != 1 || r.Min().ID != 2 {
		t.Fatalf("retrieval helpers failed: %v", r)
	}
	empty := Retrievals{}
	if empty.Max() != nil || empty.Min() != nil {
		t.Fatal("empty retrievals returned extrema")
	}
	if !SearchModeHybrid.Has(SearchModeVector) || SearchModeVector.Has(SearchModeBM25) {
		t.Fatal("SearchMode.Has returned wrong result")
	}
}

func TestSearchHelpers(t *testing.T) {
	if ResolveSearchMode(nil, SearchModeVector) != SearchModeQuery || ResolveSearchMode(&SearchArgs{}, SearchModeVector) != SearchModeQuery {
		t.Fatal("query mode resolution failed")
	}
	if ResolveSearchMode(&SearchArgs{Text: "x"}, SearchModeVector) != SearchModeVector || ResolveSearchMode(&SearchArgs{SearchMode: SearchModeBM25}, SearchModeVector) != SearchModeBM25 {
		t.Fatal("explicit/default mode resolution failed")
	}
	if SearchLimit(nil) != DefaultLimit || SearchLimit(&SearchArgs{Limit: 0}) != DefaultLimit || SearchLimit(&SearchArgs{Limit: 3}) != 3 {
		t.Fatal("SearchLimit failed")
	}
	if got := MergeOutputFields([]string{"id", "", "tag"}, []string{"tag", "content"}); !reflect.DeepEqual(got, []string{"id", "tag", "content"}) {
		t.Fatalf("MergeOutputFields = %v", got)
	}
	if got := MergeOutputFieldsWithFieldsJSON([]string{"id"}, []string{"custom", "tag"}); !reflect.DeepEqual(got, []string{"id", "fields", "tag"}) {
		t.Fatalf("MergeOutputFieldsWithFieldsJSON = %v", got)
	}
}

func TestDynamicColumnsAndResults(t *testing.T) {
	elements := []*Element{nil, {Fields: Fields{"name": "goat"}}}
	col, err := FieldsJSONColumn(elements)
	if err != nil || col.Len() != 2 {
		t.Fatalf("FieldsJSONColumn = %v, %v", col, err)
	}
	if _, err := FieldsJSONColumn([]*Element{{Fields: Fields{"": 1}}}); err == nil {
		t.Fatal("empty field name accepted")
	}
	cols, err := AppendFieldsJSONColumn(context.Background(), nil, elements, nil, true)
	if err != nil || len(cols) != 1 {
		t.Fatalf("AppendFieldsJSONColumn = %v, %v", cols, err)
	}

	jsonCol := column.NewColumnJSONBytes(ColumnFields, [][]byte{[]byte(`{"custom":7}`), []byte("not-json"), nil})
	got, err := FieldsFromColumn(jsonCol, 0)
	if err != nil || got.Get("custom") != float64(7) {
		t.Fatalf("FieldsFromColumn = %v, %v", got, err)
	}
	if got, err := FieldsFromColumn(nil, 0); err != nil || got != nil {
		t.Fatalf("FieldsFromColumn(nil) = %v, %v", got, err)
	}
	if value, err := columnValue(jsonCol, 1); err != nil || value != "not-json" {
		t.Fatalf("invalid JSON value = %v, %v", value, err)
	}
	if value, err := columnValue(jsonCol, 2); err != nil || value != nil {
		t.Fatalf("empty JSON value = %v, %v", value, err)
	}
	if _, err := columnValue(jsonCol, 99); err == nil {
		t.Fatal("out-of-range column access succeeded")
	}

	resultSet := milvusclient.ResultSet{
		IDs: column.NewColumnInt64(ColumnID, []int64{10, 20}),
		Fields: []column.Column{
			column.NewColumnVarCharArray(ColumnTag, [][]string{{"a"}, {"b"}}),
			column.NewColumnVarChar(ColumnContent, []string{"one", "two"}),
			jsonCol,
			column.NewColumnInt64("rank", []int64{1, 2}),
		},
		Scores: []float32{-0.5, 0.25},
	}
	retrievals, err := RetrievalsFromResultSet(resultSet, ColumnContent, true)
	if err != nil || len(retrievals) != 2 || retrievals[0].ID != 10 || retrievals[0].Content != "one" || retrievals[0].Distance != 0.5 || retrievals[0].Fields.Get("rank") != int64(1) {
		t.Fatalf("RetrievalsFromResultSet = %+v, %v", retrievals, err)
	}
	combined, err := RetrievalsFromResultSets([]milvusclient.ResultSet{resultSet, resultSet}, ColumnContent, false)
	if err != nil || len(combined) != 4 {
		t.Fatalf("RetrievalsFromResultSets = %d, %v", len(combined), err)
	}
	boom := errors.New("boom")
	if _, err := RetrievalsFromResultSets([]milvusclient.ResultSet{{Err: boom}}, ColumnContent, false); !errors.Is(err, boom) {
		t.Fatalf("result set error = %v", err)
	}
	if _, err := RetrievalsFromResultSet(milvusclient.ResultSet{}, ColumnContent, false); err == nil {
		t.Fatal("missing columns accepted")
	}
	badTags := resultSet
	badTags.Fields = []column.Column{column.NewColumnInt64(ColumnTag, []int64{1, 2})}
	if _, err := RetrievalsFromResultSet(badTags, "", false); err == nil {
		t.Fatal("wrong tag type accepted")
	}
}

func TestFieldsIndexInferenceAndNormalization(t *testing.T) {
	if got := FieldsPath("profile", `a"b`); got != `fields["profile"]["a\"b"]` {
		t.Fatalf("FieldsPath = %q", got)
	}
	indexes, err := FieldsIndexesFromElements([]*Element{
		nil,
		{Fields: Fields{"age": 1, "score": 1.5, "active": true, "name": "goat", "nested": map[string]any{"count": json.Number("2")}, "skip": nil}},
		{Fields: Fields{"age": 2.5}},
	})
	if err != nil || len(indexes) != 5 {
		t.Fatalf("inferred indexes = %+v, %v", indexes, err)
	}
	if _, err := FieldsIndexesFromElements([]*Element{{Fields: Fields{"": 1}}}); err == nil {
		t.Fatal("empty field name accepted")
	}

	normalized, err := NormalizeFieldsIndexes([]FieldsIndex{
		NewFieldsIndex("age", JSONFieldCastInt),
		NewFieldsIndex("age", JSONFieldCastInt),
		{JSONPath: " custom ", CastType: JSONFieldCastBool, IndexName: "custom"},
	})
	if err != nil || len(normalized) != 2 || normalized[0].IndexType != index.Inverted || normalized[1].IndexType != index.BITMAP {
		t.Fatalf("normalized = %+v, %v", normalized, err)
	}
	for _, idx := range []FieldsIndex{{CastType: JSONFieldCastInt}, {JSONPath: "fields[x]"}} {
		if _, err := NormalizeFieldsIndexes([]FieldsIndex{idx}); err == nil {
			t.Fatalf("invalid index accepted: %+v", idx)
		}
	}
	if err := (*FieldsIndexManager)(nil).Ensure(context.Background(), normalized); err != nil {
		t.Fatalf("nil manager Ensure = %v", err)
	}
	manager := NewFieldsIndexManager("collection", nil)
	if err := manager.Ensure(context.Background(), normalized); err != nil || manager.EnsureFromElements(context.Background(), nil) != nil {
		t.Fatal("manager no-op failed")
	}
	if err := CreateFieldsIndexes(context.Background(), nil, "collection", normalized); err != nil {
		t.Fatalf("CreateFieldsIndexes nil client = %v", err)
	}
}

func TestFieldTypeAndIndexNameHelpers(t *testing.T) {
	type namedString string
	type namedUint uint64
	values := []struct {
		value any
		cast  JSONFieldCastType
		ok    bool
	}{
		{"x", JSONFieldCastVarchar, true}, {true, JSONFieldCastBool, true}, {int8(1), JSONFieldCastInt, true},
		{uint16(1), JSONFieldCastInt, true}, {float32(1.5), JSONFieldCastDouble, true}, {float64(2), JSONFieldCastInt, true},
		{json.Number("3"), JSONFieldCastInt, true}, {json.Number("3.2"), JSONFieldCastDouble, true},
		{namedString("x"), JSONFieldCastVarchar, true}, {namedUint(1), JSONFieldCastInt, true},
		{math.NaN(), "", false}, {math.Inf(1), "", false}, {uint64(math.MaxUint64), "", false}, {[]int{1}, "", false},
	}
	for _, test := range values {
		cast, ok := fieldCastType(test.value)
		if cast != test.cast || ok != test.ok {
			t.Errorf("fieldCastType(%v) = %q, %v", test.value, cast, ok)
		}
	}
	if mergeFieldCastType(JSONFieldCastInt, JSONFieldCastDouble) != JSONFieldCastDouble || mergeFieldCastType(JSONFieldCastBool, JSONFieldCastInt) != JSONFieldCastBool {
		t.Fatal("mergeFieldCastType failed")
	}
	if sanitizeIndexName("123 bad---name") != "field_123_bad_name" || sanitizeIndexName("---") != "field" {
		t.Fatal("sanitizeIndexName failed")
	}
	name := fieldsIndexName(strings.Repeat("very-long-path", 20), JSONFieldCastVarchar)
	if len(name) != fieldsIndexNameLimit {
		t.Fatalf("long index name length = %d", len(name))
	}
}
