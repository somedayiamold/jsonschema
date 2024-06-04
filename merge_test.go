package jsonschema_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/somedayiamold/jsonschema/v5"
	"github.com/stretchr/testify/assert"
)

func compare(gotResult, wantResult any) error {
	switch got := gotResult.(type) {
	case map[string]any:
		want, ok := wantResult.(map[string]any)
		if !ok {
			return fmt.Errorf("1 Merge() gotResult = %v, want %v", got, wantResult)
		}
		for k, v := range got {
			return compare(v, want[k])
		}
	case []any:
		want, ok := wantResult.([]any)
		if !ok {
			return fmt.Errorf("2 Merge() gotResult = %v, want %v", got, wantResult)
		}
		if len(got) != len(want) {
			return fmt.Errorf("3 Merge() gotResult = %v, want %v", got, want)
		}
		for i, g := range got {
			switch g.(type) {
			case map[string]any:
				w, ok := want[i].(map[string]any)
				if !ok {
					return fmt.Errorf("4 Merge() gotResult = %v, want %v", g, w)
				}
				for _, v := range want {
					if err := compare(g, v); err == nil {
						return nil
					}
				}
				return fmt.Errorf("5 Merge() gotResult = %v, want %v", got, want)
			default:
				for _, v := range want {
					if reflect.DeepEqual(g, v) {
						return nil
					}
				}
				return fmt.Errorf("6 Merge() gotResult = %v, want %v", got, want)
			}
		}
	default:
		if !reflect.DeepEqual(got, wantResult) {
			return fmt.Errorf("7 Merge() gotResult = %v, want %v", got, wantResult)
		}
	}
	return nil
}

func TestSchema_Merge(t *testing.T) {
	type args struct {
		schema string
		source any
		target any
	}
	tests := []struct {
		name       string
		args       args
		wantResult any
		wantErr    bool
	}{
		{
			name: "normal case",
			args: args{
				schema: `{"type": "object", "properties": {"age": {"type": "number"}, "name": {"type": "string"}}}`,
				source: map[string]any{"age": 10, "name": "children"},
				target: map[string]any{"age": 8, "name": "child"},
			},
			wantResult: map[string]any{"age": 8, "name": "child"},
			wantErr:    false,
		},
		{
			name: "array merge",
			args: args{
				schema: `{"type":"object","properties":{"data":{"type":"array","uniqueItems":true,"items":{"type":"string"}}}}`,
				source: map[string]any{"data": []any{"a", "b", "c"}},
				target: map[string]any{"data": []any{"b", "c", "d"}},
			},
			wantResult: map[string]any{"data": []any{"a", "b", "c", "d"}},
			wantErr:    false,
		},
		{
			name: "array object merge",
			args: args{
				schema: `{"type":"object","properties":{"data":{"type":"array","uniqueItems":true,"items":{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"number"}}},"x-patch-merge-keys":["name"]}}}`,
				source: map[string]any{"data": []any{map[string]any{"name": "a", "age": 10}, map[string]any{"name": "b", "age": 10}}},
				target: map[string]any{"data": []any{map[string]any{"name": "b", "age": 8}, map[string]any{"name": "c", "age": 12}}},
			},
			wantResult: map[string]any{"data": []any{map[string]any{"name": "a", "age": 10}, map[string]any{"name": "b", "age": 8}, map[string]any{"name": "c", "age": 12}}},
			wantErr:    false,
		},
		{
			name: "array object merge union unique key",
			args: args{
				schema: `{"type":"object","properties":{"data":{"type":"array","uniqueItems":true,"items":{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"number"}}},"x-patch-merge-keys":["name","age"]}}}`,
				source: map[string]any{"data": []any{map[string]any{"name": "a", "age": 10}, map[string]any{"name": "b", "age": 10}}},
				target: map[string]any{"data": []any{map[string]any{"name": "b", "age": 8}, map[string]any{"name": "c", "age": 12}}},
			},
			wantResult: map[string]any{"data": []any{map[string]any{"name": "a", "age": 10}, map[string]any{"name": "b", "age": 10}, map[string]any{"name": "b", "age": 8}, map[string]any{"name": "c", "age": 12}}},
			wantErr:    false,
		},
		{
			name: "nested array object merge",
			args: args{
				schema: `{"type":"object","properties":{"data":{"type":"array","uniqueItems":true,"items":{"type":"object","properties":{"name":{"type":"string"},"method":{"type":"array","uniqueItems":true,"items":{"type":"object","properties":{"name":{"type":"string"},"version":{"type":"number"},"path":{"type":"array","items":{"type":"string"},"uniqueItems":true,"x-patch-strategy":"merge"}}},"x-patch-merge-keys":["name","version"]}}},"x-patch-merge-keys":["name"]}}}`,
				source: map[string]any{
					"data": []any{
						map[string]any{
							"name": "a",
							"method": []any{
								map[string]any{"name": "get", "version": 1, "path": []any{"/m", "/n"}},
								map[string]any{"name": "post", "version": 1, "path": []any{"/s", "/t"}},
							},
						},
						map[string]any{
							"name": "b",
							"method": []any{
								map[string]any{"name": "post", "version": 1, "path": []any{"/g", "/j"}},
							},
						},
					},
				},
				target: map[string]any{
					"data": []any{
						map[string]any{
							"name": "a",
							"method": []any{
								map[string]any{"name": "get", "version": 1, "path": []any{"/l", "/m"}},
								map[string]any{"name": "post", "version": 2, "path": []any{"/u", "/v"}},
							},
						},
						map[string]any{
							"name": "b",
							"method": []any{
								map[string]any{"name": "get", "version": 1, "path": []any{"/x", "/y"}},
								map[string]any{"name": "post", "version": 1, "path": []any{"/h", "/i"}},
							},
						},
						map[string]any{
							"name": "c",
							"method": []any{
								map[string]any{"name": "delete", "version": 3, "path": []any{"/w"}},
							},
						},
					},
				},
			},
			wantResult: map[string]any{
				"data": []any{
					map[string]any{
						"name": "a",
						"method": []any{
							map[string]any{"name": "get", "version": 1, "path": []any{"/m", "/n", "/l"}},
							map[string]any{"name": "post", "version": 1, "path": []any{"/s", "/t"}},
							map[string]any{"name": "post", "version": 2, "path": []any{"/u", "/v"}},
						},
					},
					map[string]any{
						"name": "b",
						"method": []any{
							map[string]any{"name": "post", "version": 1, "path": []any{"/g", "/j", "/h", "/i"}},
							map[string]any{"name": "get", "version": 1, "path": []any{"/x", "/y"}},
						},
					},
					map[string]any{
						"name": "c",
						"method": []any{
							map[string]any{"name": "delete", "version": 3, "path": []any{"/w"}},
						},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := jsonschema.CompileString("schema.json", tt.args.schema)
			assert.Nil(t, err)
			gotResult, err := s.Merge(tt.args.source, tt.args.target)
			assert.Nil(t, err)
			assert.Nil(t, compare(gotResult, tt.wantResult))
		})
	}
}
