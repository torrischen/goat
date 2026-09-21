package util

import (
	"errors"
	"strings"
	"unsafe"

	"github.com/bytedance/sonic"
)

func StringToByte(s string) []byte {
	return *(*[]byte)(unsafe.Pointer(&struct {
		string
		length int
	}{s, len(s)}))
}

func ByteToString(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}

func Map[T any, U any](src []T, f func(T) U) []U {
	dst := make([]U, len(src))
	for i, v := range src {
		dst[i] = f(v)
	}
	return dst
}

func AbsFloat32(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}

func ExtractAndBeautifyJSON(input string) (string, error) {
	jsonStr, err := extractJSONObject(input)
	if err != nil {
		return "", err
	}

	var value any
	if err := sonic.UnmarshalString(jsonStr, &value); err != nil {
		return "", errors.New("invalid JSON: " + err.Error())
	}
	prettyJSON, err := sonic.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", errors.New("invalid JSON: " + err.Error())
	}
	return string(prettyJSON), nil
}

// extractJSONObject finds and sanitizes the first valid JSON object in the input
func extractJSONObject(input string) (string, error) {
	for i := 0; i < len(input); i++ {
		if input[i] == '{' {
			if jsonStr, ok := tryExtractObject(input, i); ok {
				return jsonStr, nil
			}
		}
	}
	return "", errors.New("no valid JSON object found")
}

// tryExtractObject extracts and sanitizes a JSON object starting at given position
func tryExtractObject(input string, start int) (string, bool) {
	var builder strings.Builder
	builder.Grow(len(input) - start)

	depth := 0
	inString := false
	escaped := false
	needsSanitize := false

	for i := start; i < len(input); i++ {
		ch := input[i]

		if escaped {
			builder.WriteByte(ch)
			escaped = false
			continue
		}

		if ch == '\\' {
			builder.WriteByte(ch)
			if inString {
				escaped = true
			}
			continue
		}

		if ch == '"' {
			builder.WriteByte(ch)
			inString = !inString
			continue
		}

		if inString {
			switch ch {
			case '\r':
				builder.WriteString("\\n")
				needsSanitize = true
				if i+1 < len(input) && input[i+1] == '\n' {
					i++
				}
			case '\n':
				builder.WriteString("\\n")
				needsSanitize = true
			default:
				builder.WriteByte(ch)
			}
			continue
		}

		// Outside string
		builder.WriteByte(ch)

		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				candidate := builder.String()
				if validJSON(candidate) {
					return candidate, true
				}
				// If sanitized version is invalid, try original
				if needsSanitize {
					original := input[start : i+1]
					if validJSON(original) {
						return original, true
					}
				}
				return "", false
			}
		}
	}

	return "", false
}

func validJSON(value string) bool {
	var decoded any
	return sonic.UnmarshalString(value, &decoded) == nil
}

func ToPtr[T any](v T) *T {
	return &v
}

func ToElem[T any](v *T) T {
	if v == nil {
		return *new(T)
	}

	return *v
}
