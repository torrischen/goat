package milvus

import (
	"log"

	"github.com/torrischen/goat/util"

	"github.com/bytedance/sonic"
)

const (
	DefaultPartition = "_default"

	ColumnID        = "id"
	ColumnTag       = "tag"
	ColumnContent   = "content"
	ColumnFields    = "fields"
	ColumnEmbedding = "embedding"
	ColumnText      = "text"
	ColumnSparse    = "sparse"
)

type SearchArgs struct {
	Text          string
	Limit         int
	Offset        int
	Filter        RetrieveFilterOption
	OutputFields  []string
	SearchMode    SearchMode
	RerankWeights []float64
}

type SearchMode uint8

const (
	SearchModeAuto   SearchMode = 0
	SearchModeQuery  SearchMode = 1 << 0
	SearchModeVector SearchMode = 1 << 1
	SearchModeBM25   SearchMode = 1 << 2
	SearchModeHybrid            = SearchModeVector | SearchModeBM25
)

func (s SearchMode) Has(mode SearchMode) bool {
	return s&mode == mode
}

type Fields map[string]any

func NewFields() Fields {
	return make(Fields)
}

func NewFieldsFromObject(obj any) Fields {
	b, err := sonic.Marshal(obj)
	if err != nil {
		log.Println(err)
		return nil
	}

	return NewFieldsFromJSON(b)
}

func NewFieldsFromJSON(data []byte) Fields {
	fields := make(Fields)
	if err := sonic.Unmarshal(data, &fields); err != nil {
		log.Println(err)
		return nil
	}

	return fields
}

func NewFieldsFromJSONString(data string) Fields {
	return NewFieldsFromJSON(util.StringToByte(data))
}

func (f Fields) ToJSON() []byte {
	data, err := sonic.Marshal(f)
	if err != nil {
		log.Println(err)
		return nil
	}

	return data
}

func (f Fields) ToJSONString() string {
	return util.ByteToString(f.ToJSON())
}

func (f Fields) Get(key string) any {
	if v, ok := f[key]; ok {
		return v
	}

	return nil
}

func (f Fields) Set(key string, value any) {
	f[key] = value
}

type Element struct {
	ID          int64
	TextToEmbed string
	Tag         []string
	Fields      Fields
}

func NewElement(id int64, textToEmbed string, tags []string, fields Fields) *Element {
	return &Element{
		ID:          id,
		TextToEmbed: textToEmbed,
		Tag:         tags,
		Fields:      fields,
	}
}

func NewElementWithFields(id int64, textToEmbed string, tags []string, fields Fields) *Element {
	return NewElement(id, textToEmbed, tags, fields)
}

func (e *Element) GetField(key string) any {
	if e.Fields == nil {
		return nil
	}

	return e.Fields.Get(key)
}

func (e *Element) SetField(key string, value any) {
	if e.Fields == nil {
		e.Fields = NewFields()
	}

	e.Fields.Set(key, value)
}

type Retrieval struct {
	ID       int64
	Tag      []string
	Distance float32
	Content  string
	Fields   Fields
}

type Retrievals []*Retrieval

func (r Retrievals) Len() int {
	return len(r)
}

func (r Retrievals) Index(i int) *Retrieval {
	if i < 0 || i >= r.Len() {
		return nil
	}

	return r[i]
}

func (r *Retrievals) Max() *Retrieval {
	if r.Len() > 0 {
		return r.Index(0)
	}

	return nil
}

func (r *Retrievals) Min() *Retrieval {
	if r.Len() > 0 {
		return r.Index(r.Len() - 1)
	}

	return nil
}
