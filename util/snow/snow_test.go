package snow

import (
	"strconv"
	"testing"
)

func TestGenerateSnowflakeIDs(t *testing.T) {
	first := GenerateSnowflakeID()
	second := GenerateSnowflakeID()
	if first <= 0 || second <= first {
		t.Fatalf("generated IDs are not increasing: %d, %d", first, second)
	}
	text := GenerateSnowflakeIDString()
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value <= second {
		t.Fatalf("string ID = %q, %v", text, err)
	}
}
