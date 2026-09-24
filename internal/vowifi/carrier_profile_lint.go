package vowifi

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// The profile loader decodes with encoding/json, which ignores any key it does
// not recognise. That is the right choice for forward compatibility -- an
// override written for a newer VoCat must not stop an older one from starting
// -- but on its own it makes a typo indistinguishable from a working file: the
// document parses, the profile loads, and the misspelled setting is silently
// absent. A hand-written override is exactly where that happens.
//
// So the keys are reported instead of enforced. Two reasons it is a warning
// rather than an error:
//
//   - A malformed profile aborts startup, and with restart: unless-stopped
//     that becomes a restart loop. A misspelled key does not deserve one.
//   - The shipped database itself carries a top-level "metadata" block, and a
//     future VoCat may add fields this one has never heard of. Refusing to
//     start on an unknown key would make every forward-dated override fatal.
//
// The known-key set is derived from the structs by reflection rather than
// listed here, because a hand-maintained list is one more thing to forget when
// a field is added -- and forgetting it would reintroduce the silence this
// exists to break.

var carrierProfileLint = struct {
	sync.RWMutex
	warnings []string
}{}

// CarrierProfileWarnings reports the keys the last directory load did not
// recognise, one line each, ready to log. Empty when everything was
// understood.
//
// Deliberately a separate accessor rather than a second return value from
// LoadCarrierProfileDirectory: that function's signature is upstream's, and
// leaving it alone keeps the next merge clean.
func CarrierProfileWarnings() []string {
	carrierProfileLint.RLock()
	defer carrierProfileLint.RUnlock()
	return append([]string(nil), carrierProfileLint.warnings...)
}

func setCarrierProfileWarnings(warnings []string) {
	carrierProfileLint.Lock()
	defer carrierProfileLint.Unlock()
	carrierProfileLint.warnings = warnings
}

// carrierProfileUnknownFields lists the JSON keys in one document that no
// struct field claims, as "file: path.to.key" lines.
//
// A document that will not parse at all yields nothing: the loader reports
// that itself, with a better message than this could give.
func carrierProfileUnknownFields(source string, encoded []byte) []string {
	found := unknownJSONFields(encoded, reflect.TypeOf(carrierProfileDocument{}), "")
	if len(found) == 0 {
		return nil
	}
	sort.Strings(found)
	warnings := make([]string, 0, len(found))
	for _, key := range found {
		warnings = append(warnings, fmt.Sprintf("%s: unknown key %q (ignored)", source, key))
	}
	return warnings
}

// unknownJSONFields walks a decoded value against the struct that models it
// and returns the dotted paths of keys the struct has no field for.
//
// Recursion stops at json.RawMessage, which is how a field says its contents
// are deliberately unmodelled -- the document's "metadata" block being the
// case in point.
func unknownJSONFields(encoded []byte, target reflect.Type, path string) []string {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}

	switch target.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if json.Unmarshal(encoded, &object) != nil {
			// Not an object here. Either the document is malformed, which the
			// loader reports, or the field holds a scalar -- neither is an
			// unknown key.
			return nil
		}
		var unknown []string
		for key, value := range object {
			field, ok := structFieldForJSONKey(target, key)
			if !ok {
				unknown = append(unknown, join(path, key))
				continue
			}
			unknown = append(unknown, unknownJSONFields(value, field.Type, join(path, key))...)
		}
		return unknown

	case reflect.Slice, reflect.Array:
		var items []json.RawMessage
		if json.Unmarshal(encoded, &items) != nil {
			return nil
		}
		var unknown []string
		for index, item := range items {
			// Indexed so a warning names which entry of match_any or profiles
			// carries the bad key, which is the difference between an
			// actionable message and a scavenger hunt.
			unknown = append(unknown,
				unknownJSONFields(item, target.Elem(), fmt.Sprintf("%s[%d]", path, index))...)
		}
		return unknown

	case reflect.Map:
		// A map accepts any key by definition, so nothing here is unknown.
		return nil
	}
	return nil
}

// structFieldForJSONKey finds the field a JSON key decodes into, following the
// same rules encoding/json does: the tag name when present, otherwise a
// case-insensitive match on the field name, and embedded structs searched
// through.
func structFieldForJSONKey(target reflect.Type, key string) (reflect.StructField, bool) {
	for index := 0; index < target.NumField(); index++ {
		field := target.Field(index)
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			if field.Anonymous {
				if nested, ok := structFieldForJSONKey(field.Type, key); ok {
					return nested, true
				}
				continue
			}
			name = field.Name
		}
		if strings.EqualFold(name, key) {
			return field, true
		}
	}
	return reflect.StructField{}, false
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
