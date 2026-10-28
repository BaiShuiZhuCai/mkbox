package i18n

import (
	"fmt"
	"regexp"
	"testing"
)

func TestNormalize(t *testing.T) {
	ok := map[string]string{
		"":        Chinese,
		"zh":      Chinese,
		"zh-CN":   Chinese,
		"ZH_cn":   Chinese,
		"en":      English,
		"en-US":   English,
		"English": English,
	}
	for in, want := range ok {
		got, valid := Normalize(in)
		if !valid || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, valid, want)
		}
	}
	for _, in := range []string{"fr", "ja", "zh-TW", "en-UK"} {
		if got, valid := Normalize(in); valid {
			t.Errorf("Normalize(%q) = %q, true; want unsupported", in, got)
		}
	}
}

func TestTranslateFallback(t *testing.T) {
	t.Cleanup(func() { _ = SetLanguage(Chinese) })
	if err := SetLanguage(Chinese); err != nil {
		t.Fatal(err)
	}
	if got := Translate("总结"); got != "总结" {
		t.Fatalf("Chinese mode should not translate, got %q", got)
	}
	if err := SetLanguage(English); err != nil {
		t.Fatal(err)
	}
	if got := Translate("总结"); got != "Summary" {
		t.Fatalf("Translate(总结) = %q, want Summary", got)
	}
	if got := Translate("未收录的消息"); got != "未收录的消息" {
		t.Fatalf("missing key should fall back to the source, got %q", got)
	}
	if got := Tf("计划动作 %d 个，运行时发现 %d 个，journal %d 条", 1, 2, 3); got != "Planned actions: 1, runtime findings: 2, journal entries: 3" {
		t.Fatalf("Tf = %q", got)
	}
}

func TestSetLanguageRejectsUnknown(t *testing.T) {
	if err := SetLanguage("de"); err == nil {
		t.Fatal("unknown language should error")
	}
}

func TestCatalogFormatVerbsMatch(t *testing.T) {
	verb := regexp.MustCompile(`%[-+ #0-9.*]*[a-zA-Z%]`)
	for zh, en := range englishCatalog {
		want := verb.FindAllString(zh, -1)
		got := verb.FindAllString(en, -1)
		if fmt.Sprint(want) != fmt.Sprint(got) {
			t.Errorf("format verbs differ for %q:\n  zh: %v\n  en: %v", zh, want, got)
		}
		if en == "" {
			t.Errorf("empty translation for %q", zh)
		}
	}
}
