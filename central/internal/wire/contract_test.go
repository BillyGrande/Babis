package wire

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const wireDir = "../../../wire"

func compile(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	s, err := jsonschema.NewCompiler().Compile(filepath.Join(wireDir, name))
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	return s
}

func schemaErr(s *jsonschema.Schema, data []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return s.Validate(inst)
}

// decode strictly enough to surface type errors; unknown fields are ignored.
func decode[T any](data []byte) (*T, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

type validator interface{ Validate() error }

func checkValid[T any, P interface {
	*T
	validator
}](t *testing.T, schema *jsonschema.Schema, data []byte) {
	t.Helper()
	first, err := decode[T](data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := P(first).Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	if err := schemaErr(schema, data); err != nil {
		t.Errorf("schema: %v", err)
	}
	enc, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	second, err := decode[T](enc)
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("round trip differs:\n first: %+v\nsecond: %+v", first, second)
	}
}

func checkInvalid[T any, P interface {
	*T
	validator
}](t *testing.T, schema *jsonschema.Schema, data []byte) {
	t.Helper()
	// A decode (type) error counts as rejection by code.
	if v, err := decode[T](data); err == nil && P(v).Validate() == nil {
		t.Error("Validate accepted an invalid fixture")
	}
	if schemaErr(schema, data) == nil {
		t.Error("schema accepted an invalid fixture")
	}
}

func TestContract(t *testing.T) {
	reqSchema := compile(t, "checkin.request.schema.json")
	respSchema := compile(t, "checkin.response.schema.json")

	files, err := filepath.Glob(filepath.Join(wireDir, "testdata", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	all, _ := os.ReadDir(filepath.Join(wireDir, "testdata"))
	if len(all) != len(files) {
		t.Fatalf("testdata has non-.json files")
	}
	for _, f := range files {
		name := filepath.Base(f)
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(name, func(t *testing.T) {
			switch {
			case strings.HasPrefix(name, "request-"):
				checkValid[CheckinRequest](t, reqSchema, data)
			case strings.HasPrefix(name, "response-"):
				checkValid[CheckinResponse](t, respSchema, data)
			case strings.HasPrefix(name, "invalid-request-"):
				checkInvalid[CheckinRequest](t, reqSchema, data)
			case strings.HasPrefix(name, "invalid-response-"):
				checkInvalid[CheckinResponse](t, respSchema, data)
			default:
				t.Fatalf("fixture %s matches no category", name)
			}
		})
	}
}
