// Package i18n provides the message catalog and language selection for mkbox.
//
// Messages are keyed by their Chinese source text so that the default
// (zh-CN) output is unchanged and any message missing from the English
// catalog falls back to Chinese instead of rendering an empty string.
package i18n

import (
	"fmt"
	"strings"
	"sync"
)

const (
	// Chinese is the default and fallback language.
	Chinese = "zh-CN"
	// English selects the English catalog.
	English = "en"
)

var (
	mu       sync.RWMutex
	language = Chinese
)

// Normalize maps a language tag to a supported identifier.
func Normalize(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "zh", "cn", "zh-cn", "zh_cn", "zh-hans", "chinese":
		return Chinese, true
	case "en", "en-us", "en_us", "en-gb", "english":
		return English, true
	}
	return "", false
}

// SetLanguage switches the active language.
func SetLanguage(raw string) error {
	code, ok := Normalize(raw)
	if !ok {
		return fmt.Errorf("不支持的界面语言 %q（可选 zh-CN、en）", raw)
	}
	mu.Lock()
	language = code
	mu.Unlock()
	return nil
}

// Language returns the active language identifier.
func Language() string {
	mu.RLock()
	defer mu.RUnlock()
	return language
}

// IsEnglish reports whether English output is active.
func IsEnglish() bool {
	return Language() == English
}

// Translate returns the catalog entry for msg in the active language. When no
// translation exists the original message is returned.
func Translate(msg string) string {
	if msg == "" {
		return msg
	}
	mu.RLock()
	lang := language
	mu.RUnlock()
	if lang == Chinese {
		return msg
	}
	if tr, ok := catalogs[lang][msg]; ok {
		return tr
	}
	return msg
}

// T translates a message key.
func T(key string) string {
	return Translate(key)
}

// Tf translates a format string and applies the arguments.
func Tf(format string, args ...any) string {
	return fmt.Sprintf(Translate(format), args...)
}
