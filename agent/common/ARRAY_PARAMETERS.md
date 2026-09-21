# Array Parameter Support in NewToolParameters

## Overview
The `NewToolParameters` function now supports array-type parameters with proper JSON Schema validation. This eliminates the need for manual schema construction when defining tools with array parameters.

## Changes Made

### 1. Enhanced `ToolProperty` Struct
Added two new optional fields:
- `Description`: Optional description for the parameter
- `Items`: For array types, specifies the schema for array elements

```go
type ToolProperty struct {
    Name        string
    Type        string
    Required    bool
    Description string         // Optional description for the parameter
    Items       *ToolProperty  // For array types, specifies the type of array elements
}
```

### 2. Updated `NewToolParameters` Function
The function now automatically generates the `items` schema for array types and includes descriptions when provided.

## Usage Examples

### Basic Array Parameter
```go
Parameters: common.NewToolParameters(
    common.ToolProperty{
        Name:        "file_paths",
        Type:        "array",
        Required:    true,
        Description: "Array of file paths to analyze",
        Items:       &common.ToolProperty{Type: "string"},
    },
)
```

Generated JSON Schema:
```json
{
  "type": "object",
  "properties": {
    "file_paths": {
      "type": "array",
      "description": "Array of file paths to analyze",
      "items": {
        "type": "string"
      }
    }
  },
  "required": ["file_paths"]
}
```

### Multiple Parameters with Arrays
```go
Parameters: common.NewToolParameters(
    common.ToolProperty{
        Name:        "keywords",
        Type:        "array",
        Required:    true,
        Description: "Keywords to search for",
        Items:       &common.ToolProperty{Type: "string"},
    },
    common.ToolProperty{
        Name:        "scores",
        Type:        "array",
        Required:    false,
        Description: "Score thresholds",
        Items:       &common.ToolProperty{Type: "number"},
    },
    common.ToolProperty{
        Name:        "limit",
        Type:        "integer",
        Required:    false,
        Description: "Maximum number of results",
    },
)
```

### Array of Numbers
```go
Parameters: common.NewToolParameters(
    common.ToolProperty{
        Name:        "values",
        Type:        "array",
        Required:    true,
        Items:       &common.ToolProperty{Type: "number"},
    },
)
```

## Backward Compatibility

The changes are fully backward compatible. Existing code using `NewToolParameters` without array types continues to work without any modifications:

```go
// This still works exactly as before
Parameters: common.NewToolParameters(
    common.ToolProperty{Name: "location", Type: "string", Required: true},
    common.ToolProperty{Name: "date", Type: "string", Required: true},
)
```

## Fixing the Original Error

The error you encountered:
```
Invalid schema for function 'file_content_summary_tool':
In context=('properties', 'file_paths'), array schema missing items.
```

Can now be fixed by using the enhanced `NewToolParameters`:

**Before (Manual Schema):**
```go
Parameters: map[string]any{
    "type": "object",
    "properties": map[string]any{
        "file_paths": map[string]any{
            "type": "array",
            "items": map[string]any{
                "type": "string",
            },
        },
    },
    "required": []string{"file_paths"},
}
```

**After (Using NewToolParameters):**
```go
Parameters: common.NewToolParameters(
    common.ToolProperty{
        Name:     "file_paths",
        Type:     "array",
        Required: true,
        Items:    &common.ToolProperty{Type: "string"},
    },
)
```

## Testing

Run the test suite to verify the implementation:
```bash
go test ./agent/common -v -run TestNewToolParameters
```

All tests pass successfully, confirming proper array support with JSON Schema validation.
