package rein

import (
	"bytes"
	"encoding/json"
	"testing"
)

func convertToBytes(i interface{}) []byte {
	buffer := new(bytes.Buffer)
	json.NewEncoder(buffer).Encode(i)

	return buffer.Bytes()
}

func TestEmptyMinAndMaxVersion(t *testing.T) {
	o := Override{
		MinimumVersion: "",
		MaximumVersion: "",
		Value:          false,
	}

	err := o.UnmarshalJSON(convertToBytes(o))

	if err == nil {
		t.Errorf("%q or %q should be an invalid semantic version", o.MinimumVersion, o.MaximumVersion)
	}
}

func TestMinGreaterThanMaxVersion(t *testing.T) {
	o := Override{
		MinimumVersion: "v1.0.0",
		MaximumVersion: "v0.0.1",
		Value:          false,
	}

	err := o.UnmarshalJSON(convertToBytes(o))

	if err == nil {
		t.Errorf("%q should be invalid for being greater than %q", o.MinimumVersion, o.MaximumVersion)
	}
}

func TestValidMinAndMaxVersion(t *testing.T) {
	o := Override{
		MinimumVersion: "v1.0.0",
		MaximumVersion: "v2.0.0",
		Value:          false,
	}

	err := o.UnmarshalJSON(convertToBytes(o))

	if err != nil {
		t.Errorf("%q or %q should be a valid min and max version", o.MinimumVersion, o.MaximumVersion)
	}
}
