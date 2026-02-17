package wsbase

import (
	"fmt"
	"regexp"
)

// CompileFilters compiles optional include/exclude regex strings for a named filter pair.
// Returns nil for empty strings (no filter). Returns error for invalid regex.
func CompileFilters(fieldName, includeStr, excludeStr string) (*regexp.Regexp, *regexp.Regexp, error) {
	var include, exclude *regexp.Regexp
	if includeStr != "" {
		var err error
		include, err = regexp.Compile(includeStr)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid %s include filter: %v", fieldName, err)
		}
	}
	if excludeStr != "" {
		var err error
		exclude, err = regexp.Compile(excludeStr)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid %s exclude filter: %v", fieldName, err)
		}
	}
	return include, exclude, nil
}

// CompileSessionFilters compiles optional include/exclude regex strings for session names.
// Returns nil for empty strings (no filter). Returns error for invalid regex.
func CompileSessionFilters(includeStr, excludeStr string) (*regexp.Regexp, *regexp.Regexp, error) {
	return CompileFilters("session", includeStr, excludeStr)
}

// CompilePathFilters compiles optional include/exclude regex strings for paths.
// Returns nil for empty strings (no filter). Returns error for invalid regex.
func CompilePathFilters(includeStr, excludeStr string) (*regexp.Regexp, *regexp.Regexp, error) {
	return CompileFilters("path", includeStr, excludeStr)
}

// PassesFilter checks if a value passes the include/exclude regex filters.
func PassesFilter(value string, include, exclude *regexp.Regexp) bool {
	if include != nil && !include.MatchString(value) {
		return false
	}
	if exclude != nil && exclude.MatchString(value) {
		return false
	}
	return true
}
