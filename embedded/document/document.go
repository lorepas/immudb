/*
Copyright 2023 Codenotary Inc. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package document

import (
	"errors"
	"fmt"

	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/types/known/structpb"
)

// Document is a json document
type Document struct {
	result gjson.Result
}

// UnmarshalJSON satisfies the json Unmarshaler interface
func (d *Document) UnmarshalJSON(bytes []byte) error {
	doc, err := NewDocumentFromBytes(bytes)
	if err != nil {
		return err
	}
	d.result = doc.result
	return nil
}

// MarshalJSON satisfies the json Marshaler interface
func (d *Document) MarshalJSON() ([]byte, error) {
	return d.Bytes(), nil
}

// NewDocument creates a new json document
func NewDocument() *Document {
	parsed := gjson.Parse("{}")
	return &Document{
		result: parsed,
	}
}

// NewDocumentFromBytes creates a new document from the given json bytes
func NewDocumentFromBytes(json []byte) (*Document, error) {
	if !gjson.ValidBytes(json) {
		return nil, fmt.Errorf("invalid json: %s", string(json))
	}
	d := &Document{
		result: gjson.Result{
			Type: gjson.JSON,
			Raw:  string(json),
		},
	}
	if !d.Valid() {
		return nil, errors.New("invalid document")
	}
	return d, nil
}

// NewDocumentFrom creates a new document from the given struct object
func NewDocumentFrom(value *structpb.Struct) (*Document, error) {
	var err error
	bytes, err := value.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to json encode value: %v", value)
	}
	return NewDocumentFromBytes(bytes)
}

// Valid returns whether the document is valid
func (d *Document) Valid() bool {
	return gjson.ValidBytes(d.Bytes()) && !d.result.IsArray()
}

// String returns the document as a json string
func (d *Document) String() string {
	return d.result.String()
}

// Bytes returns the document as json bytes
func (d *Document) Bytes() []byte {
	return []byte(d.result.Raw)
}

func (d *Document) Get(field string) interface{} {
	if d.result.Get(field).Exists() {
		return d.result.Get(field).Value()
	}
	return nil
}
