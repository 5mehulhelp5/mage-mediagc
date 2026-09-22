package phpconfig

import "fmt"

// Get walks a nested parsed structure and returns the value at path.
//
//	Get(root, "db", "connection", "default", "host")
func Get(root map[string]any, path ...string) (any, bool) {
	var cur any = root
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// GetString returns the string value at path.
// Numeric values are converted, which is handy for config files that quote
// ports inconsistently.
func GetString(root map[string]any, path ...string) (string, bool) {
	v, ok := Get(root, path...)
	if !ok || v == nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	case int64:
		return fmt.Sprintf("%d", t), true
	case float64:
		return fmt.Sprintf("%v", t), true
	case bool:
		if t {
			return "1", true
		}
		return "", true
	default:
		return "", false
	}
}

// GetInt returns the integer value at path.
func GetInt(root map[string]any, path ...string) (int64, bool) {
	v, ok := Get(root, path...)
	if !ok {
		return 0, false
	}
	switch t := v.(type) {
	case int64:
		return t, true
	case float64:
		return int64(t), true
	case string:
		var n int64
		if _, err := fmt.Sscanf(t, "%d", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

// GetMap returns the nested map at path.
func GetMap(root map[string]any, path ...string) (map[string]any, bool) {
	v, ok := Get(root, path...)
	if !ok {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}
